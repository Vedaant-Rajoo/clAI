package main

import (
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
func runAuth(args []string, apiKeyFlag string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: clai auth <login|status|logout> [--provider <name>]\nRun 'clai auth help' for usage.")
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
		return authLogin(provider)
	case "status":
		return authStatus(provider, apiKeyFlag)
	case "logout":
		return authLogout(provider)
	default:
		fmt.Fprintf(os.Stderr, "clai auth: unknown command %q\nRun 'clai auth help' for usage.\n", args[0])
		return exitUsage
	}
}

func authLogin(provider string) int {
	switch provider {
	case "openrouter":
		fmt.Println("Starting OpenRouter sign-in. Press enter to open your browser,")
		fmt.Print("or paste an API key now to skip the browser flow: ")
		var line string
		fmt.Scanln(&line)
		line = strings.TrimSpace(line)
		if line != "" {
			return storeKey(provider, line)
		}
		key, err := pkceLogin()
		if err != nil {
			fmt.Fprintf(os.Stderr, "clai auth login: %v\n", err)
			return 1
		}
		return storeKey(provider, key)
	case "anthropic", "openai":
		fmt.Printf("Paste your %s API key: ", provider)
		var key string
		fmt.Scanln(&key)
		key = strings.TrimSpace(key)
		if key == "" {
			fmt.Fprintln(os.Stderr, "clai auth login: empty key")
			return 1
		}
		return storeKey(provider, key)
	default:
		fmt.Fprintf(os.Stderr, "clai auth login: unknown provider %q (known: openrouter, anthropic, openai)\n", provider)
		return exitUsage
	}
}

func storeKey(provider, key string) int {
	if err := auth.Store(provider, key); err != nil {
		fmt.Fprintf(os.Stderr, "clai auth login: store key: %v\n", err)
		return 1
	}
	fmt.Printf("Stored %s credentials (%s).\n", provider, auth.Source(provider, ""))
	return 0
}

func authStatus(provider, explicit string) int {
	source := auth.Source(provider, explicit)
	if source == "none" {
		fmt.Printf("%s: not authenticated\n", provider)
		return 1
	}
	fmt.Printf("%s: authenticated (%s)\n", provider, source)
	return 0
}

func authLogout(provider string) int {
	if err := auth.Delete(provider); err != nil {
		fmt.Fprintf(os.Stderr, "clai auth logout: %v\n", err)
		return 1
	}
	fmt.Printf("Removed stored %s credentials.\n", provider)
	return 0
}

// pkceLogin runs the OpenRouter PKCE flow: start a localhost listener, open
// the browser to the authorize URL, exchange the returned code for an API key.
func pkceLogin() (string, error) {
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
				errCh <- errors.New("callback missing code")
				http.Error(w, "missing code", http.StatusBadRequest)
				return
			}
			codeCh <- code
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><body><h2>clai: sign-in complete</h2><p>You can close this tab.</p></body></html>")
		}),
	}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	authURL := openRouterAuthorizeURL + "?callback_url=" + url.QueryEscape(callbackURL) +
		"&code_challenge=" + challenge + "&code_challenge_method=S256"

	fmt.Printf("Open this URL to sign in:\n\n  %s\n\n", authURL)
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
