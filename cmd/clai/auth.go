package main

import (
	"bufio"
	"bytes"
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
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Vedaant-Rajoo/clai/internal/auth"
	"golang.org/x/term"
)

var (
	stdinIsTerminal    = term.IsTerminal
	readTerminalSecret = term.ReadPassword
)

const (
	openRouterAuthorizeURL = "https://openrouter.ai/auth"
	openRouterKeysURL      = "https://openrouter.ai/api/v1/auth/keys"
	callbackPath           = "/callback"

	callbackWaitTimeout = 3 * time.Minute
	exchangeTimeout     = 30 * time.Second
	serverStopTimeout   = 5 * time.Second
	maxCallbackValue    = 4096
	maxExchangeResponse = 64 * 1024
	maxAPIKeyLength     = 16 * 1024
)

type authCommand struct {
	verb        string
	provider    string
	apiKey      string
	providerSet bool
	apiKeySet   bool
}

// parseAuthArgs parses the complete auth grammar without performing any I/O.
func parseAuthArgs(args []string) (authCommand, error) {
	var command authCommand
	if len(args) == 0 {
		return command, errors.New("missing command (want login, status, or logout)")
	}
	if strings.HasPrefix(args[0], "-") {
		return command, errors.New("options must follow the auth command")
	}
	command.verb = args[0]
	switch command.verb {
	case "login", "status", "logout":
	default:
		return authCommand{}, fmt.Errorf("unknown command %q", command.verb)
	}
	command.provider = "openrouter"

	for i := 1; i < len(args); {
		option := args[i]
		if !strings.HasPrefix(option, "--") {
			return authCommand{}, errors.New("unexpected positional argument")
		}
		if option == "--" {
			return authCommand{}, errors.New("unsupported option --")
		}
		if strings.Contains(option, "=") {
			return authCommand{}, errors.New("options require a space-separated value")
		}
		switch option {
		case "--provider":
			if command.providerSet {
				return authCommand{}, errors.New("--provider may be supplied only once")
			}
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return authCommand{}, errors.New("--provider requires a value")
			}
			command.provider = args[i+1]
			command.providerSet = true
			i += 2
		case "--api-key":
			if command.apiKeySet {
				return authCommand{}, errors.New("--api-key may be supplied only once")
			}
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return authCommand{}, errors.New("--api-key requires a value")
			}
			command.apiKey = args[i+1]
			command.apiKeySet = true
			i += 2
		default:
			return authCommand{}, fmt.Errorf("unknown option %q", option)
		}
	}
	if command.apiKeySet && command.verb != "status" {
		return authCommand{}, errors.New("--api-key is valid only for auth status")
	}
	switch command.provider {
	case "openrouter", "anthropic", "openai":
	default:
		return authCommand{}, fmt.Errorf("unknown provider %q (known: openrouter, anthropic, openai)", command.provider)
	}
	return command, nil
}

type authTimer interface {
	Chan() <-chan time.Time
	Stop() bool
}

type systemAuthTimer struct{ timer *time.Timer }

func (t systemAuthTimer) Chan() <-chan time.Time { return t.timer.C }
func (t systemAuthTimer) Stop() bool             { return t.timer.Stop() }

type authHTTPServer interface {
	Serve(net.Listener) error
	Close() error
}

type authHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type authRuntime struct {
	random         io.Reader
	listen         func(network, address string) (net.Listener, error)
	server         func(http.Handler) authHTTPServer
	launchBrowser  func(string) error
	newTimer       func(time.Duration) authTimer
	withTimeout    func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	httpClient     authHTTPDoer
	authorizeURL   string
	keysURL        string
	callbackWait   time.Duration
	exchangeWait   time.Duration
	serverStopWait time.Duration
}

func defaultAuthRuntime() authRuntime {
	return authRuntime{
		random: rand.Reader,
		listen: net.Listen,
		server: func(handler http.Handler) authHTTPServer {
			return &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 16 * 1024}
		},
		launchBrowser: openBrowser,
		newTimer: func(duration time.Duration) authTimer {
			return systemAuthTimer{timer: time.NewTimer(duration)}
		},
		withTimeout:    context.WithTimeout,
		httpClient:     http.DefaultClient,
		authorizeURL:   openRouterAuthorizeURL,
		keysURL:        openRouterKeysURL,
		callbackWait:   callbackWaitTimeout,
		exchangeWait:   exchangeTimeout,
		serverStopWait: serverStopTimeout,
	}
}

func (c cli) authDependencies() authRuntime {
	defaults, deps := defaultAuthRuntime(), c.authRuntime
	if deps.random == nil {
		deps.random = defaults.random
	}
	if deps.listen == nil {
		deps.listen = defaults.listen
	}
	if deps.server == nil {
		deps.server = defaults.server
	}
	if deps.launchBrowser == nil {
		deps.launchBrowser = defaults.launchBrowser
	}
	if deps.newTimer == nil {
		deps.newTimer = defaults.newTimer
	}
	if deps.withTimeout == nil {
		deps.withTimeout = defaults.withTimeout
	}
	if deps.httpClient == nil {
		deps.httpClient = defaults.httpClient
	}
	if deps.authorizeURL == "" {
		deps.authorizeURL = defaults.authorizeURL
	}
	if deps.keysURL == "" {
		deps.keysURL = defaults.keysURL
	}
	if deps.callbackWait <= 0 {
		deps.callbackWait = defaults.callbackWait
	}
	if deps.exchangeWait <= 0 {
		deps.exchangeWait = defaults.exchangeWait
	}
	if deps.serverStopWait <= 0 {
		deps.serverStopWait = defaults.serverStopWait
	}
	return deps
}

func (c cli) authContext() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

func (c cli) runAuth(command authCommand) int {
	switch command.verb {
	case "login":
		return c.authLogin(command.provider)
	case "status":
		return c.authStatus(command.provider, command.apiKey)
	case "logout":
		return c.authLogout(command.provider)
	default:
		panic("runAuth called with unparsed command")
	}
}

func (c cli) authLogin(provider string) int {
	readKey := c.authReadLine
	if readKey == nil {
		readKey = func() (string, error) { return readLine(os.Stdin, c.stdout) }
	}
	switch provider {
	case "openrouter":
		fmt.Fprintln(c.stdout, "Starting OpenRouter sign-in. Press enter to open your browser,")
		fmt.Fprint(c.stdout, "or paste an API key now to skip the browser flow: ")
		line, err := readKey()
		if err != nil {
			fmt.Fprintf(c.stderr, "clai auth login: read key: %v\n", err)
			return exitError
		}
		if line != "" {
			return c.storeKey(provider, line)
		}
		key, err := c.pkceLogin(c.authContext())
		if err != nil {
			fmt.Fprintf(c.stderr, "clai auth login: %v\n", err)
			return exitError
		}
		return c.storeKey(provider, key)
	case "anthropic", "openai":
		fmt.Fprintf(c.stdout, "Paste your %s API key: ", provider)
		key, err := readKey()
		if err != nil {
			fmt.Fprintf(c.stderr, "clai auth login: read key: %v\n", err)
			return exitError
		}
		if key == "" {
			fmt.Fprintln(c.stderr, "clai auth login: empty key")
			return exitError
		}
		return c.storeKey(provider, key)
	default:
		panic("authLogin called with unparsed provider")
	}
}

func (c cli) storeKey(provider, key string) int {
	store := c.authStore
	if store == nil {
		store = auth.Store
	}
	if err := store(provider, key); err != nil {
		fmt.Fprintf(c.stderr, "clai auth login: store key: %v\n", err)
		return exitError
	}
	sourceWithError := c.authSourceWithError
	if sourceWithError == nil {
		sourceWithError = auth.SourceWithError
	}
	source, err := sourceWithError(provider, "")
	if err != nil {
		fmt.Fprintf(c.stderr, "clai auth login: stored credentials but could not inspect source: %v\n", err)
		return exitError
	}
	fmt.Fprintf(c.stdout, "Stored %s credentials (%s).\n", provider, source)
	return exitOK
}

func (c cli) authStatus(provider, explicit string) int {
	sourceWithError := c.authSourceWithError
	if sourceWithError == nil {
		sourceWithError = auth.SourceWithError
	}
	source, err := sourceWithError(provider, explicit)
	if err != nil {
		fmt.Fprintf(c.stderr, "clai auth status: inspect credentials: %v\n", err)
		return exitError
	}
	if source == "none" {
		fmt.Fprintf(c.stdout, "%s: not authenticated; run `clai auth login --provider %s`\n", provider, provider)
		return exitError
	}
	fmt.Fprintf(c.stdout, "%s: authenticated (%s)\n", provider, source)
	return exitOK
}

func (c cli) authLogout(provider string) int {
	deleteKey := c.authDelete
	if deleteKey == nil {
		deleteKey = auth.Delete
	}
	if err := deleteKey(provider); err != nil {
		fmt.Fprintf(c.stderr, "clai auth logout: %v\n", err)
		return exitError
	}
	fmt.Fprintf(c.stdout, "Removed stored %s credentials.\n", provider)
	return exitOK
}

func readLine(input *os.File, output io.Writer) (string, error) {
	if stdinIsTerminal(int(input.Fd())) {
		line, err := readTerminalSecret(int(input.Fd()))
		// ReadPassword disables terminal echo, including the newline. Move the
		// next status or error message onto a fresh line after input completes.
		fmt.Fprintln(output)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(line)), nil
	}

	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		if errors.Is(err, io.EOF) {
			return "", errors.New("end of input")
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}

type callbackDelivery int

const (
	callbackDelivered callbackDelivery = iota
	callbackCompleted
	callbackExpired
)

type callbackReceiver struct {
	mu        sync.Mutex
	expired   bool
	completed bool
	codeCh    chan<- string
}

func newCallbackReceiver(codeCh chan<- string) *callbackReceiver {
	return &callbackReceiver{codeCh: codeCh}
}

func (r *callbackReceiver) deliver(code string) callbackDelivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.expired {
		return callbackExpired
	}
	if r.completed {
		return callbackCompleted
	}
	r.completed = true
	select {
	case r.codeCh <- code:
		return callbackDelivered
	default:
		return callbackCompleted
	}
}

func (r *callbackReceiver) expire() {
	r.mu.Lock()
	r.expired = true
	r.mu.Unlock()
}

func (c cli) pkceLogin(ctx context.Context) (string, error) {
	deps := c.authDependencies()
	verifier, challenge, err := generatePKCE(deps.random)
	if err != nil {
		return "", fmt.Errorf("generate PKCE: %w", err)
	}
	listener, err := deps.listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("start callback listener: %w", err)
	}
	listenerClosed := false
	closeListener := func() {
		if !listenerClosed {
			_ = listener.Close()
			listenerClosed = true
		}
	}
	defer closeListener()

	codeCh := make(chan string, 1)
	receiver := newCallbackReceiver(codeCh)
	server := deps.server(newCallbackHandler(receiver))
	serverClosed := false
	serveDone := make(chan struct{})
	serveErr := make(chan error, 1)
	go func() {
		defer close(serveDone)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && !isClosedNetworkError(err) {
			select {
			case serveErr <- err:
			default:
			}
		}
	}()
	closeServer := func() error {
		if serverClosed {
			return nil
		}
		serverClosed = true
		receiver.expire()
		_ = server.Close()
		closeListener()
		stopTimer := deps.newTimer(deps.serverStopWait)
		defer stopTimer.Stop()
		select {
		case <-serveDone:
			return nil
		case <-stopTimer.Chan():
			return errors.New("callback server did not stop")
		}
	}
	defer func() { _ = closeServer() }()

	callbackURL := "http://" + listener.Addr().String() + callbackPath
	authURL := deps.authorizeURL + "?callback_url=" + url.QueryEscape(callbackURL) + "&code_challenge=" + url.QueryEscape(challenge) + "&code_challenge_method=S256"
	fmt.Fprintf(c.stdout, "Open this URL to sign in:\n\n  %s\n\n", authURL)
	if err := deps.launchBrowser(authURL); err != nil {
		fmt.Fprintf(c.stderr, "clai auth login: could not open browser; open the URL above manually: %v\n", err)
	}
	timer := deps.newTimer(deps.callbackWait)
	defer timer.Stop()

	var code string
	select {
	case code = <-codeCh:
		if err := closeServer(); err != nil {
			return "", err
		}
	case err := <-serveErr:
		receiver.expire()
		return "", fmt.Errorf("callback server: %w", err)
	case <-timer.Chan():
		receiver.expire()
		return "", errors.New("timed out waiting for browser sign-in")
	case <-ctx.Done():
		receiver.expire()
		return "", ctx.Err()
	}
	return exchangeCode(ctx, deps, code, verifier)
}

func generateRandomToken(random io.Reader) (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(random, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func generatePKCE(random io.Reader) (verifier, challenge string, err error) {
	verifier, err = generateRandomToken(random)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func newCallbackHandler(receiver *callbackReceiver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != callbackPath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "invalid callback request", http.StatusMethodNotAllowed)
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			http.Error(w, "invalid callback request", http.StatusBadRequest)
			return
		}
		codes := query["code"]
		// OpenRouter documents only a code callback and does not document a
		// round-tripped state parameter. Reject extra parameters rather than
		// depending on undocumented callback URL query preservation.
		if len(query) != 1 || len(codes) != 1 || codes[0] == "" || len(codes[0]) > maxCallbackValue {
			http.Error(w, "invalid callback request", http.StatusBadRequest)
			return
		}
		switch receiver.deliver(codes[0]) {
		case callbackDelivered:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<html><body><h2>clai: sign-in complete</h2><p>You can close this tab.</p></body></html>")
		case callbackCompleted:
			http.Error(w, "callback already completed", http.StatusConflict)
		case callbackExpired:
			http.Error(w, "callback expired", http.StatusGone)
		}
	})
}

func exchangeCode(parent context.Context, deps authRuntime, code, verifier string) (string, error) {
	withTimeout := deps.withTimeout
	if withTimeout == nil {
		withTimeout = context.WithTimeout
	}
	ctx, cancel := withTimeout(parent, deps.exchangeWait)
	defer cancel()
	body, err := json.Marshal(struct {
		Code                string `json:"code"`
		CodeVerifier        string `json:"code_verifier"`
		CodeChallengeMethod string `json:"code_challenge_method"`
	}{code, verifier, "S256"})
	if err != nil {
		return "", errors.New("encode exchange request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deps.keysURL, bytes.NewReader(body))
	if err != nil {
		return "", errors.New("create exchange request")
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := deps.httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return "", context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", context.DeadlineExceeded
		}
		return "", errors.New("exchange request failed")
	}
	defer res.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(res.Body, maxExchangeResponse+1))
	if readErr != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return "", context.Canceled
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", context.DeadlineExceeded
		}
		return "", errors.New("read exchange response failed")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("exchange code: status %d", res.StatusCode)
	}
	if len(responseBody) > maxExchangeResponse {
		return "", errors.New("exchange response exceeds size limit")
	}
	key, err := decodeExchangeKey(responseBody)
	if err != nil {
		return "", err
	}
	return key, nil
}

func decodeExchangeKey(responseBody []byte) (string, error) {
	if !utf8.Valid(responseBody) {
		return "", errors.New("decode key response: invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	opening, err := decoder.Token()
	if err != nil {
		return "", errors.New("decode key response")
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return "", errors.New("decode key response: expected object")
	}

	var key string
	seenKey := false
	for decoder.More() {
		member, err := decoder.Token()
		if err != nil {
			return "", errors.New("decode key response")
		}
		name, ok := member.(string)
		if !ok {
			return "", errors.New("decode key response")
		}
		if name != "key" {
			// Tolerate unknown members (for example OpenRouter's user_id) so a
			// provider that adds fields to an otherwise well-formed response does
			// not break login. Fully consume and discard the member's value to
			// keep the decoder aligned; the "key" member is still validated
			// strictly below, and duplicate/missing/trailing checks are intact.
			var discard json.RawMessage
			if err := decoder.Decode(&discard); err != nil {
				return "", errors.New("decode key response")
			}
			continue
		}
		if seenKey {
			return "", errors.New("decode key response: duplicate key member")
		}
		seenKey = true
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return "", errors.New("decode key response: key must be a string")
		}
		if err := validateJSONStringUnicode(raw); err != nil {
			return "", err
		}
		if json.Unmarshal(raw, &key) != nil {
			return "", errors.New("decode key response: key must be a string")
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return "", errors.New("decode key response")
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return "", errors.New("decode key response")
	}
	if !seenKey || key == "" {
		return "", errors.New("exchange returned no key")
	}
	if len(key) > maxAPIKeyLength {
		return "", errors.New("exchange returned an oversized key")
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		_ = token
		return "", errors.New("decode key response: trailing data")
	}
	return key, nil
}

func validateJSONStringUnicode(raw []byte) error {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return errors.New("decode key response: key must be a string")
	}
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw)-1 {
			return errors.New("decode key response: key must be a string")
		}
		if raw[i] != 'u' {
			continue
		}
		unit, ok := parseJSONHexUnit(raw, i+1)
		if !ok {
			return errors.New("decode key response: key must be a string")
		}
		i += 4
		switch {
		case unit >= 0xD800 && unit <= 0xDBFF:
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return errors.New("decode key response: invalid UTF-16 escape")
			}
			low, ok := parseJSONHexUnit(raw, i+3)
			if !ok || low < 0xDC00 || low > 0xDFFF {
				return errors.New("decode key response: invalid UTF-16 escape")
			}
			i += 6
		case unit >= 0xDC00 && unit <= 0xDFFF:
			return errors.New("decode key response: invalid UTF-16 escape")
		}
	}
	return nil
}

func parseJSONHexUnit(raw []byte, start int) (uint16, bool) {
	if start < 0 || start+4 > len(raw) {
		return 0, false
	}
	var value uint16
	for _, char := range raw[start : start+4] {
		value <<= 4
		switch {
		case char >= '0' && char <= '9':
			value |= uint16(char - '0')
		case char >= 'a' && char <= 'f':
			value |= uint16(char-'a') + 10
		case char >= 'A' && char <= 'F':
			value |= uint16(char-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "linux":
		command = exec.Command("xdg-open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		return fmt.Errorf("browser launch is not supported on %s", runtime.GOOS)
	}
	return command.Start()
}

func isClosedNetworkError(err error) bool {
	return errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "use of closed network connection")
}
