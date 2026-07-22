package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"codeberg.org/newedia/clai/internal/auth"
)

const (
	openRouterAuthorizeURL = "https://openrouter.ai/auth"
	openRouterKeysURL      = "https://openrouter.ai/api/v1/auth/keys"
	callbackPath           = "/callback"
)

// runAuth handles `clai auth <login|status|logout>`.
func (c cli) runAuth(args []string, apiKeyFlag string) int {
	if len(args) == 0 {
		fmt.Fprintln(c.stderr, "usage: clai auth <login|status|logout> [--provider <name>]\nRun 'clai auth help' for usage.")
		return exitUsage
	}

	provider := "openrouter"
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--provider" && i+1 < len(rest) {
			provider = rest[i+1]
		}
	}

	switch args[0] {
	case "login":
		return c.authLogin(provider)
	case "status":
		return c.authStatus(provider, apiKeyFlag)
	case "logout":
		return c.authLogout(provider)
	default:
		fmt.Fprintf(c.stderr, "clai auth: unknown command %q\nRun 'clai auth help' for usage.\n", args[0])
		return exitUsage
	}
}

func (c cli) authLogin(provider string) int {
	switch provider {
	case "openrouter":
		fmt.Fprintln(c.stdout, "Starting OpenRouter sign-in. Press enter to open your browser,")
		fmt.Fprint(c.stdout, "or paste an API key now to skip the browser flow: ")
		line, err := readLine()
		if err != nil {
			fmt.Fprintf(c.stderr, "clai auth login: read key: %v\n", err)
			return 1
		}
		if line != "" {
			return c.storeKey(provider, line)
		}
		key, err := c.pkceLogin()
		if err != nil {
			fmt.Fprintf(c.stderr, "clai auth login: %v\n", err)
			return 1
		}
		return c.storeKey(provider, key)
	case "anthropic", "openai":
		fmt.Fprintf(c.stdout, "Paste your %s API key: ", provider)
		key, err := readLine()
		if err != nil {
			fmt.Fprintf(c.stderr, "clai auth login: read key: %v\n", err)
			return 1
		}
		if key == "" {
			fmt.Fprintln(c.stderr, "clai auth login: empty key")
			return 1
		}
		return c.storeKey(provider, key)
	default:
		fmt.Fprintf(c.stderr, "clai auth login: unknown provider %q (known: openrouter, anthropic, openai)\n", provider)
		return exitUsage
	}
}

func (c cli) storeKey(provider, key string) int {
	if err := auth.Store(provider, key); err != nil {
		fmt.Fprintf(c.stderr, "clai auth login: store key: %v\n", err)
		return 1
	}
	fmt.Fprintf(c.stdout, "Stored %s credentials (%s).\n", provider, auth.Source(provider, ""))
	return 0
}

func (c cli) authStatus(provider, explicit string) int {
	source := auth.Source(provider, explicit)
	if source == "none" {
		fmt.Fprintf(c.stdout, "%s: not authenticated\n", provider)
		return 1
	}
	fmt.Fprintf(c.stdout, "%s: authenticated (%s)\n", provider, source)
	return 0
}

func (c cli) authLogout(provider string) int {
	if err := auth.Delete(provider); err != nil {
		fmt.Fprintf(c.stderr, "clai auth logout: %v\n", err)
		return 1
	}
	fmt.Fprintf(c.stdout, "Removed stored %s credentials.\n", provider)
	return 0
}

// readLine reads a full line from stdin, so pasted keys containing spaces are
// stored verbatim instead of being truncated at the first whitespace token.
// An EOF with no input (Ctrl-D) is reported as an error rather than being
// mistaken for pressing enter.
func readLine() (string, error) {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		if errors.Is(err, io.EOF) {
			return "", errors.New("end of input")
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// pkceLogin runs the OpenRouter PKCE flow: start a localhost listener, open
// the browser to the authorize URL, exchange the returned code for an API key.
func (c cli) pkceLogin() (string, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return "", err
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("start callback listener: %w", err)
	}
	defer listener.Close()

	callbackURL := "http://" + listener.Addr().String() + callbackPath

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != callbackPath {
				http.NotFound(w, r)
				return
			}
			code := r.URL.Query().Get("code")
			if code == "" {
				// Never block the HTTP handler: if the main flow already has a
				// result or timed out, just answer the request.
				select {
				case errCh <- errors.New("callback missing code"):
				default:
				}
				http.Error(w, "missing code", http.StatusBadRequest)
				return
			}
			select {
			case codeCh <- code:
			default:
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><body><h2>clai: sign-in complete</h2><p>You can close this tab.</p></body></html>")
		}),
	}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	authURL := openRouterAuthorizeURL + "?callback_url=" + url.QueryEscape(callbackURL) +
		"&code_challenge=" + challenge + "&code_challenge_method=S256"

	fmt.Fprintf(c.stdout, "Open this URL to sign in:\n\n  %s\n\n", authURL)
	openBrowser(authURL)

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return "", err
	case <-time.After(3 * time.Minute):
		return "", errors.New("timed out waiting for browser sign-in")
	}

	return exchangeCode(code, verifier)
}

func generatePKCE() (verifier, challenge string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func exchangeCode(code, verifier string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"code":                  code,
		"code_verifier":         verifier,
		"code_challenge_method": "S256",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openRouterKeysURL, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("exchange code: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return "", fmt.Errorf("exchange code: status %d: %s", res.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var parsed struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode key response: %w", err)
	}
	if parsed.Key == "" {
		return "", errors.New("exchange returned no key")
	}
	return parsed.Key, nil
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	_ = cmd.Start()
}
