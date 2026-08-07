package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vedaant-Rajoo/clai/internal/app"
	"github.com/Vedaant-Rajoo/clai/internal/auth"
	"github.com/Vedaant-Rajoo/clai/internal/capability"
	"github.com/Vedaant-Rajoo/clai/internal/config"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
)

func TestParseAuthArgs(t *testing.T) {
	secret := "api-key-secret-value"
	cases := []struct {
		name    string
		args    []string
		want    authCommand
		wantErr string
	}{
		{"login default", []string{"login"}, authCommand{verb: "login", provider: "openrouter"}, ""},
		{"login provider", []string{"login", "--provider", "anthropic"}, authCommand{verb: "login", provider: "anthropic", providerSet: true}, ""},
		{"status all options", []string{"status", "--provider", "openai", "--api-key", secret}, authCommand{verb: "status", provider: "openai", apiKey: secret, providerSet: true, apiKeySet: true}, ""},
		{"status reversed options", []string{"status", "--api-key", secret, "--provider", "openrouter"}, authCommand{verb: "status", provider: "openrouter", apiKey: secret, providerSet: true, apiKeySet: true}, ""},
		{"logout provider", []string{"logout", "--provider", "openrouter"}, authCommand{verb: "logout", provider: "openrouter", providerSet: true}, ""},
		{"missing verb", nil, authCommand{}, "missing command"},
		{"pre-verb provider", []string{"--provider", "openrouter", "status"}, authCommand{}, "options must follow"},
		{"pre-verb help", []string{"--help"}, authCommand{}, "options must follow"},
		{"unknown verb", []string{"show"}, authCommand{}, "unknown command"},
		{"unknown option", []string{"status", "--model", "x"}, authCommand{}, "unknown option"},
		{"missing provider", []string{"status", "--provider"}, authCommand{}, "requires a value"},
		{"missing provider before option", []string{"status", "--provider", "--api-key", secret}, authCommand{}, "requires a value"},
		{"missing api key", []string{"status", "--api-key"}, authCommand{}, "requires a value"},
		{"empty api key", []string{"status", "--api-key", ""}, authCommand{}, "requires a value"},
		{"duplicate provider", []string{"status", "--provider", "openrouter", "--provider", "openai"}, authCommand{}, "only once"},
		{"duplicate api key", []string{"status", "--api-key", secret, "--api-key", "other-secret"}, authCommand{}, "only once"},
		{"api key on login", []string{"login", "--api-key", secret}, authCommand{}, "status"},
		{"api key on logout", []string{"logout", "--api-key", secret}, authCommand{}, "status"},
		{"unknown provider", []string{"status", "--provider", "bogus"}, authCommand{}, "unknown provider"},
		{"trailing argument", []string{"status", "trailing-secret"}, authCommand{}, "unexpected positional"},
		{"equals provider", []string{"status", "--provider=openrouter"}, authCommand{}, "space-separated"},
		{"equals api key", []string{"status", "--api-key=" + secret}, authCommand{}, "space-separated"},
		{"single dash", []string{"status", "-provider", "openrouter"}, authCommand{}, "unexpected positional"},
		{"option terminator", []string{"status", "--"}, authCommand{}, "unsupported option"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAuthArgs(tc.args)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("parseAuthArgs() error = %v", err)
				}
				if got != tc.want {
					t.Fatalf("parseAuthArgs() = %#v, want %#v", got, tc.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "other-secret") || strings.Contains(err.Error(), "trailing-secret") {
				t.Fatalf("error disclosed secret: %v", err)
			}
		})
	}
}

func TestAuthHelpParserOptionsStaySynchronized(t *testing.T) {
	documented := map[string]bool{}
	for _, option := range flagToken.FindAllString(commandLong("auth"), -1) {
		documented[option] = true
	}
	want := map[string]bool{"--provider": true, "--api-key": true, "--help": true}
	if len(documented) != len(want) {
		t.Fatalf("documented auth options = %#v, want %#v", documented, want)
	}
	for option := range want {
		if !documented[option] {
			t.Fatalf("auth help missing parser option %s", option)
		}
	}
}

func TestReadLineUsesHiddenInputForTTY(t *testing.T) {
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()

	originalIsTerminal, originalReadSecret := stdinIsTerminal, readTerminalSecret
	stdinIsTerminal = func(fd int) bool {
		if fd != int(input.Fd()) {
			t.Fatalf("terminal check fd = %d, want %d", fd, input.Fd())
		}
		return true
	}
	readTerminalSecret = func(fd int) ([]byte, error) {
		if fd != int(input.Fd()) {
			t.Fatalf("ReadPassword fd = %d, want %d", fd, input.Fd())
		}
		return []byte("  tty-secret  \r\n"), nil
	}
	t.Cleanup(func() {
		stdinIsTerminal = originalIsTerminal
		readTerminalSecret = originalReadSecret
	})

	var output bytes.Buffer
	got, err := readLine(input, &output)
	if err != nil {
		t.Fatalf("readLine: %v", err)
	}
	if got != "tty-secret" {
		t.Fatalf("key = %q, want trimmed hidden key", got)
	}
	if output.String() != "\n" {
		t.Fatalf("output = %q, want only the replacement newline", output.String())
	}
	if strings.Contains(output.String(), got) {
		t.Fatal("hidden key was written to output")
	}
}

func TestReadLinePreservesNonTTYInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte("  piped-secret  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()

	originalIsTerminal := stdinIsTerminal
	stdinIsTerminal = func(int) bool { return false }
	t.Cleanup(func() { stdinIsTerminal = originalIsTerminal })

	var output bytes.Buffer
	got, err := readLine(input, &output)
	if err != nil {
		t.Fatalf("readLine: %v", err)
	}
	if got != "piped-secret" {
		t.Fatalf("key = %q, want trimmed piped key", got)
	}
	if output.Len() != 0 {
		t.Fatalf("non-TTY output = %q, want empty", output.String())
	}
}

func TestAuthParseErrorsHaveNoSideEffects(t *testing.T) {
	called := false
	mark := func() { called = true }
	c, _, _ := captureCLI()
	c.authReadLine = func() (string, error) { mark(); return "", nil }
	c.authStore = func(string, string) error { mark(); return nil }
	c.authDelete = func(string) error { mark(); return nil }
	c.authSourceWithError = func(string, string) (string, error) { mark(); return "none", nil }
	c.authRuntime.listen = func(string, string) (net.Listener, error) { mark(); return nil, errors.New("unexpected") }
	if got := c.run([]string{"auth", "status", "--api-key", "secret", "trailing"}); got != exitUsage {
		t.Fatalf("exit = %d", got)
	}
	if called {
		t.Fatal("parse error performed auth side effect")
	}
}

func TestGeneratePKCEFixedRandom(t *testing.T) {
	input := bytes.Repeat([]byte{0x42}, 32)
	verifier, challenge, err := generatePKCE(bytes.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if verifier != "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI" {
		t.Fatalf("verifier = %q", verifier)
	}
	if challenge != "dpSIltH1Qleurm3f_xNkUnBTdqwmVVafNnhuYkt-oNE" {
		t.Fatalf("challenge = %q", challenge)
	}
}

func TestCallbackHandlerValidation(t *testing.T) {
	oversized := strings.Repeat("x", maxCallbackValue+1)
	cases := []struct {
		name, method, target string
		want                 int
		code                 string
	}{
		{"wrong path", http.MethodGet, "/other?code=ok", http.StatusNotFound, ""},
		{"wrong method", http.MethodPost, callbackPath + "?code=ok", http.StatusMethodNotAllowed, ""},
		{"missing code", http.MethodGet, callbackPath, http.StatusBadRequest, ""},
		{"empty code", http.MethodGet, callbackPath + "?code=", http.StatusBadRequest, ""},
		{"duplicate code", http.MethodGet, callbackPath + "?code=a&code=b", http.StatusBadRequest, ""},
		{"malformed query", http.MethodGet, callbackPath + "?code=%zz", http.StatusBadRequest, ""},
		{"semicolon query", http.MethodGet, callbackPath + "?code=ok;extra=value", http.StatusBadRequest, ""},
		{"oversized code", http.MethodGet, callbackPath + "?code=" + oversized, http.StatusBadRequest, ""},
		{"state unsupported", http.MethodGet, callbackPath + "?code=ok&state=value", http.StatusBadRequest, ""},
		{"duplicate state unsupported", http.MethodGet, callbackPath + "?code=ok&state=a&state=b", http.StatusBadRequest, ""},
		{"unknown parameter", http.MethodGet, callbackPath + "?code=ok&extra=value", http.StatusBadRequest, ""},
		{"valid", http.MethodGet, callbackPath + "?code=ok", http.StatusOK, "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			codes := make(chan string, 1)
			recorder := httptest.NewRecorder()
			newCallbackHandler(newCallbackReceiver(codes)).ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.target, nil))
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.want)
			}
			select {
			case got := <-codes:
				if got != tc.code {
					t.Fatalf("code = %q, want %q", got, tc.code)
				}
			default:
				if tc.code != "" {
					t.Fatal("valid callback did not deliver code")
				}
			}
			if strings.Contains(recorder.Body.String(), oversized) {
				t.Fatal("response reflected callback text")
			}
		})
	}
}

func TestCallbackHandlerRepeatedValidFirstWins(t *testing.T) {
	codes := make(chan string, 1)
	handler := newCallbackHandler(newCallbackReceiver(codes))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, callbackPath+"?code=first", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d", first.Code)
	}

	secondDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		second := httptest.NewRecorder()
		handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, callbackPath+"?code=second", nil))
		secondDone <- second
	}()
	select {
	case second := <-secondDone:
		if second.Code != http.StatusConflict {
			t.Fatalf("second status = %d, want %d", second.Code, http.StatusConflict)
		}
	case <-time.After(time.Second):
		t.Fatal("repeated callback blocked")
	}
	if got := <-codes; got != "first" {
		t.Fatalf("delivered code = %q, want first", got)
	}
	select {
	case extra := <-codes:
		t.Fatalf("unexpected second code %q", extra)
	default:
	}
}

func TestCallbackHandlerExpiredIsNonblocking(t *testing.T) {
	codes := make(chan string, 1)
	receiver := newCallbackReceiver(codes)
	receiver.expire()
	handler := newCallbackHandler(receiver)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, callbackPath+"?code=late", nil))
		done <- recorder
	}()
	select {
	case recorder := <-done:
		if recorder.Code != http.StatusGone {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusGone)
		}
	case <-time.After(time.Second):
		t.Fatal("expired callback blocked")
	}
	select {
	case code := <-codes:
		t.Fatalf("expired callback delivered %q", code)
	default:
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func exchangeDeps(doer authHTTPDoer) authRuntime {
	return authRuntime{httpClient: doer, keysURL: "https://keys.invalid/exchange", exchangeWait: time.Second}
}

func TestExchangeCode(t *testing.T) {
	secretCode, secretVerifier, secretKey := "secret-code", "secret-verifier", "secret-key"
	t.Run("request fields and success", func(t *testing.T) {
		cancelled := false
		deps := exchangeDeps(doerFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost || req.URL.String() != "https://keys.invalid/exchange" {
				t.Fatalf("request = %s %s", req.Method, req.URL)
			}
			if req.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("content type = %q", req.Header.Get("Content-Type"))
			}
			var body map[string]string
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["code"] != secretCode || body["code_verifier"] != secretVerifier || body["code_challenge_method"] != "S256" {
				t.Fatalf("body = %#v", body)
			}
			return response(http.StatusOK, `{"key":"`+secretKey+`"}`), nil
		}))
		deps.withTimeout = func(parent context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
			if duration != time.Second {
				t.Fatalf("exchange timeout = %v", duration)
			}
			ctx, cancel := context.WithCancel(parent)
			return ctx, func() { cancelled = true; cancel() }
		}
		got, err := exchangeCode(context.Background(), deps, secretCode, secretVerifier)
		if err != nil || got != secretKey {
			t.Fatalf("exchange = %q, %v", got, err)
		}
		if !cancelled {
			t.Fatal("exchange timeout context was not cancelled")
		}
	})
	validUnicode := []struct {
		name string
		body string
		want string
	}{
		{"valid surrogate pair", "{\"key\":\"\\uD83D\\uDE00\"}", "😀"},
		{"literal replacement character", `{"key":"�"}`, "�"},
		{"escaped replacement character", "{\"key\":\"\\uFFFD\"}", "�"},
		{"ordinary non ascii", `{"key":"日本語-api-key"}`, "日本語-api-key"},
		{"decoded length boundary", `{"key":"` + strings.Repeat("k", maxAPIKeyLength) + `"}`, strings.Repeat("k", maxAPIKeyLength)},
		{"unknown member before key tolerated", `{"user_id":"u-123","key":"ok"}`, "ok"},
		{"unknown member after key tolerated", `{"key":"ok","user_id":"u-123"}`, "ok"},
		{"unknown object member tolerated", `{"limits":{"remaining":5},"key":"ok"}`, "ok"},
		{"unknown array member tolerated", `{"scopes":["a","b"],"key":"ok"}`, "ok"},
	}
	for _, tc := range validUnicode {
		t.Run(tc.name, func(t *testing.T) {
			deps := exchangeDeps(doerFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusOK, tc.body), nil
			}))
			got, err := exchangeCode(context.Background(), deps, secretCode, secretVerifier)
			if err != nil || got != tc.want {
				t.Fatalf("exchange = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	cases := []struct {
		name      string
		status    int
		body      string
		transport error
		want      string
	}{
		{"missing key", http.StatusOK, `{}`, nil, "no key"},
		{"malformed", http.StatusOK, `{`, nil, "decode key response"},
		{"top level array", http.StatusOK, `[{"key":"ok"}]`, nil, "expected object"},
		{"duplicate key", http.StatusOK, `{"key":"first","key":"second"}`, nil, "duplicate key member"},
		{"duplicate key with unknown member between", http.StatusOK, `{"key":"first","user_id":"u","key":"second"}`, nil, "duplicate key member"},
		{"malformed unknown member value", http.StatusOK, `{"user_id":,"key":"ok"}`, nil, "decode key response"},
		{"key number", http.StatusOK, `{"key":123}`, nil, "key must be a string"},
		{"key object", http.StatusOK, `{"key":{}}`, nil, "key must be a string"},
		{"key null", http.StatusOK, `{"key":null}`, nil, "key must be a string"},
		{"raw invalid utf8", http.StatusOK, "{\"key\":\"" + string([]byte{0xff}) + "\"}", nil, "invalid UTF-8"},
		{"lone high surrogate", http.StatusOK, `{"key":"\uD800"}`, nil, "invalid UTF-16 escape"},
		{"lone low surrogate", http.StatusOK, `{"key":"\uDC00"}`, nil, "invalid UTF-16 escape"},
		{"high surrogate without low", http.StatusOK, `{"key":"\uD800A"}`, nil, "invalid UTF-16 escape"},
		{"mismatched surrogate pair", http.StatusOK, "{\"key\":\"\\uD800\\u0041\"}", nil, "invalid UTF-16 escape"},
		{"high high surrogate pair", http.StatusOK, `{"key":"\uD800\uD801"}`, nil, "invalid UTF-16 escape"},
		{"trailing value", http.StatusOK, `{"key":"ok"} {}`, nil, "trailing data"},
		{"oversized key", http.StatusOK, `{"key":"` + strings.Repeat("k", maxAPIKeyLength+1) + `"}`, nil, "oversized key"},
		{"oversized response", http.StatusOK, strings.Repeat(" ", maxExchangeResponse+1), nil, "size limit"},
		{"non 2xx", http.StatusUnauthorized, `upstream-secret-body`, nil, "status 401"},
		{"transport", 0, "", errors.New("transport failed with secret-code secret-verifier secret-key"), "exchange request failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := exchangeDeps(doerFunc(func(*http.Request) (*http.Response, error) {
				if tc.transport != nil {
					return nil, tc.transport
				}
				return response(tc.status, tc.body), nil
			}))
			_, err := exchangeCode(context.Background(), deps, secretCode, secretVerifier)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			for _, secret := range []string{secretCode, secretVerifier, secretKey, "upstream-secret-body"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error disclosed secret %q: %v", secret, err)
				}
			}
		})
	}
}

type errorReadCloser struct{ err error }

func (r errorReadCloser) Read([]byte) (int, error) { return 0, r.err }
func (errorReadCloser) Close() error               { return nil }

func TestExchangeReadErrorDoesNotDiscloseSecrets(t *testing.T) {
	secretCode, secretVerifier, secretKey := "read-secret-code", "read-secret-verifier", "read-secret-key"
	deps := exchangeDeps(doerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       errorReadCloser{err: errors.New("read failed with " + secretCode + " " + secretVerifier + " " + secretKey)},
			Header:     make(http.Header),
		}, nil
	}))
	_, err := exchangeCode(context.Background(), deps, secretCode, secretVerifier)
	if err == nil || err.Error() != "read exchange response failed" {
		t.Fatalf("error = %v", err)
	}
	for _, secret := range []string{secretCode, secretVerifier, secretKey} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error disclosed %q: %v", secret, err)
		}
	}
}

func TestExchangeCodeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	deps := exchangeDeps(doerFunc(func(req *http.Request) (*http.Response, error) { return nil, req.Context().Err() }))
	_, err := exchangeCode(ctx, deps, "code", "verifier")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestExchangeCodeDeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	deps := exchangeDeps(doerFunc(func(req *http.Request) (*http.Response, error) { return nil, req.Context().Err() }))
	_, err := exchangeCode(ctx, deps, "code", "verifier")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

type fakeTimer struct {
	ch      chan time.Time
	stopped bool
}

func (t *fakeTimer) Chan() <-chan time.Time { return t.ch }
func (t *fakeTimer) Stop() bool             { t.stopped = true; return true }

type fakeListener struct{ closed bool }

func (l *fakeListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (l *fakeListener) Close() error              { l.closed = true; return nil }
func (l *fakeListener) Addr() net.Addr            { return fakeAddr("127.0.0.1:43210") }

type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }

type fakeServer struct {
	handler http.Handler
	closed  bool
	once    sync.Once
	served  chan struct{}
}

func (s *fakeServer) Serve(net.Listener) error {
	s.once.Do(func() {
		s.handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, callbackPath+"?code=bad&state=unsupported", nil))
		s.handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, callbackPath+"?code=auth-code", nil))
		close(s.served)
	})
	return nil
}
func (s *fakeServer) Close() error { s.closed = true; return nil }

func TestPKCEListenerCreationFailureHasNoLaterSideEffects(t *testing.T) {
	listenErr := errors.New("listener unavailable")
	called := false
	c, _, _ := captureCLI()
	c.authRuntime = authRuntime{
		random: bytes.NewReader(bytes.Repeat([]byte{1}, 32)),
		listen: func(network, address string) (net.Listener, error) {
			if network != "tcp" || address != "127.0.0.1:0" {
				t.Fatalf("listen = %q, %q", network, address)
			}
			return nil, listenErr
		},
		server:        func(http.Handler) authHTTPServer { called = true; return nil },
		launchBrowser: func(string) error { called = true; return nil },
		newTimer:      func(time.Duration) authTimer { called = true; return nil },
		httpClient:    doerFunc(func(*http.Request) (*http.Response, error) { called = true; return nil, nil }),
	}
	_, err := c.pkceLogin(context.Background())
	if err == nil || !strings.Contains(err.Error(), listenErr.Error()) {
		t.Fatalf("error = %v", err)
	}
	if called {
		t.Fatal("listener failure performed later auth side effects")
	}
}

func TestPKCEOrchestrationBrowserFailureContinuesAndCleansUp(t *testing.T) {
	listener := &fakeListener{}
	server := &fakeServer{served: make(chan struct{})}
	timer := &fakeTimer{ch: make(chan time.Time)}
	browserErr := errors.New("launcher unavailable")
	randomBytes := append(bytes.Repeat([]byte{0x11}, 32), bytes.Repeat([]byte{0x22}, 32)...)
	exchangeCalls := 0
	c, out, errOut := captureCLI()
	c.authRuntime = authRuntime{
		random:        bytes.NewReader(randomBytes),
		listen:        func(string, string) (net.Listener, error) { return listener, nil },
		server:        func(handler http.Handler) authHTTPServer { server.handler = handler; return server },
		launchBrowser: func(string) error { return browserErr },
		newTimer: func(duration time.Duration) authTimer {
			if duration != time.Second {
				t.Fatalf("callback timeout = %v", duration)
			}
			return timer
		},
		httpClient: doerFunc(func(*http.Request) (*http.Response, error) {
			exchangeCalls++
			return response(http.StatusOK, `{"key":"stored-key"}`), nil
		}),
		authorizeURL: "https://authorize.invalid/auth", keysURL: "https://keys.invalid/exchange", callbackWait: time.Second, exchangeWait: time.Second, serverStopWait: time.Second,
	}
	key, err := c.pkceLogin(context.Background())
	if err != nil || key != "stored-key" {
		t.Fatalf("pkceLogin = %q, %v", key, err)
	}
	if !strings.Contains(out.String(), "https://authorize.invalid/auth") || !strings.Contains(out.String(), "code_challenge_method=S256") {
		t.Fatalf("stdout = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "open the URL above manually") || !strings.Contains(errOut.String(), browserErr.Error()) {
		t.Fatalf("stderr = %q", errOut.String())
	}
	if !listener.closed || !server.closed || !timer.stopped {
		t.Fatalf("cleanup listener=%v server=%v timer=%v", listener.closed, server.closed, timer.stopped)
	}
	assertChannelClosed(t, server.served, "success server goroutine")
	lateDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		server.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, callbackPath+"?code=late-after-success", nil))
		lateDone <- recorder
	}()
	select {
	case recorder := <-lateDone:
		if recorder.Code != http.StatusGone {
			t.Fatalf("post-success callback status = %d, want %d", recorder.Code, http.StatusGone)
		}
	case <-time.After(time.Second):
		t.Fatal("post-success callback blocked")
	}
	if exchangeCalls != 1 {
		t.Fatalf("exchange calls = %d, want 1", exchangeCalls)
	}
	for _, secret := range []string{"auth-code", "stored-key"} {
		if strings.Contains(errOut.String(), secret) {
			t.Fatalf("diagnostic disclosed %q", secret)
		}
	}
}

func TestPKCEMalformedCallbackThenTimeoutExpiresFlowAndStopsServer(t *testing.T) {
	listener := &fakeListener{}
	timer := &fakeTimer{ch: make(chan time.Time, 1)}
	server := &malformedBlockingServer{closed: make(chan struct{}), done: make(chan struct{}), timer: timer}
	c, _, _ := captureCLI()
	c.authRuntime = authRuntime{
		random:        bytes.NewReader(bytes.Repeat([]byte{1}, 32)),
		listen:        func(string, string) (net.Listener, error) { return listener, nil },
		server:        func(handler http.Handler) authHTTPServer { server.handler = handler; return server },
		launchBrowser: func(string) error { return nil },
		newTimer:      func(time.Duration) authTimer { return timer },
		httpClient:    doerFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected exchange"); return nil, nil }),
		authorizeURL:  "https://auth.invalid", keysURL: "https://keys.invalid", callbackWait: time.Second, exchangeWait: time.Second, serverStopWait: time.Second,
	}
	_, err := c.pkceLogin(context.Background())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v", err)
	}
	if server.malformedStatus != http.StatusBadRequest {
		t.Fatalf("malformed status = %d", server.malformedStatus)
	}
	assertChannelClosed(t, server.done, "malformed callback server goroutine")

	lateDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		server.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, callbackPath+"?code=late", nil))
		lateDone <- recorder
	}()
	select {
	case recorder := <-lateDone:
		if recorder.Code != http.StatusGone {
			t.Fatalf("late callback status = %d, want %d", recorder.Code, http.StatusGone)
		}
	case <-time.After(time.Second):
		t.Fatal("callback after timeout blocked")
	}
}

func TestPKCEExchangeErrorStopsServerGoroutine(t *testing.T) {
	listener := &fakeListener{}
	server := &fakeServer{served: make(chan struct{})}
	timer := &fakeTimer{ch: make(chan time.Time)}
	c, _, _ := captureCLI()
	c.authRuntime = authRuntime{
		random:        bytes.NewReader(bytes.Repeat([]byte{1}, 32)),
		listen:        func(string, string) (net.Listener, error) { return listener, nil },
		server:        func(handler http.Handler) authHTTPServer { server.handler = handler; return server },
		launchBrowser: func(string) error { return nil },
		newTimer:      func(time.Duration) authTimer { return timer },
		httpClient:    doerFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("transport secret auth-code") }),
		authorizeURL:  "https://auth.invalid", keysURL: "https://keys.invalid", callbackWait: time.Second, exchangeWait: time.Second, serverStopWait: time.Second,
	}
	_, err := c.pkceLogin(context.Background())
	if err == nil || err.Error() != "exchange request failed" {
		t.Fatalf("error = %v", err)
	}
	assertChannelClosed(t, server.served, "exchange error server goroutine")
	if !listener.closed || !server.closed {
		t.Fatal("exchange error did not clean up listener/server")
	}
}

func TestPKCETimeoutAndCancellationCleanup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
	}{{"timeout", false}, {"cancel", true}} {
		t.Run(tc.name, func(t *testing.T) {
			listener := &fakeListener{}
			timer := &fakeTimer{ch: make(chan time.Time, 1)}
			server := &blockingServer{closed: make(chan struct{}), done: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			} else {
				timer.ch <- time.Now()
			}
			c, _, _ := captureCLI()
			c.ctx = ctx
			c.authRuntime = authRuntime{random: bytes.NewReader(bytes.Repeat([]byte{1}, 64)), listen: func(string, string) (net.Listener, error) { return listener, nil }, server: func(http.Handler) authHTTPServer { return server }, launchBrowser: func(string) error { return nil }, newTimer: func(time.Duration) authTimer { return timer }, httpClient: doerFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected exchange"); return nil, nil }), authorizeURL: "https://auth.invalid", keysURL: "https://keys.invalid", callbackWait: time.Second, exchangeWait: time.Second, serverStopWait: time.Second}
			_, err := c.pkceLogin(ctx)
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v", err)
			}
			if !tc.cancel && (err == nil || !strings.Contains(err.Error(), "timed out")) {
				t.Fatalf("error = %v", err)
			}
			if !listener.closed || !server.wasClosed || !timer.stopped {
				t.Fatal("resources not cleaned up")
			}
			assertChannelClosed(t, server.done, tc.name+" server goroutine")
		})
	}
}

func TestPKCEServerStopTimeoutIsBounded(t *testing.T) {
	listener := &fakeListener{}
	callbackTimer := &fakeTimer{ch: make(chan time.Time)}
	stopTimer := &fakeTimer{ch: make(chan time.Time, 1)}
	stopTimer.ch <- time.Now()
	server := &stubbornServer{release: make(chan struct{}), done: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(server.release) }) }
	defer release()
	timerCalls := 0
	c, _, _ := captureCLI()
	c.authRuntime = authRuntime{
		random:        bytes.NewReader(bytes.Repeat([]byte{1}, 32)),
		listen:        func(string, string) (net.Listener, error) { return listener, nil },
		server:        func(handler http.Handler) authHTTPServer { server.handler = handler; return server },
		launchBrowser: func(string) error { return nil },
		newTimer: func(duration time.Duration) authTimer {
			timerCalls++
			if duration != time.Second {
				t.Fatalf("timer duration = %v", duration)
			}
			if timerCalls == 1 {
				return callbackTimer
			}
			return stopTimer
		},
		httpClient:   doerFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected exchange"); return nil, nil }),
		authorizeURL: "https://auth.invalid", keysURL: "https://keys.invalid", callbackWait: time.Second, exchangeWait: time.Second, serverStopWait: time.Second,
	}
	result := make(chan error, 1)
	go func() {
		_, err := c.pkceLogin(context.Background())
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil || err.Error() != "callback server did not stop" {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server stop timeout did not bound orchestration")
	}
	if !server.closeCalled || !listener.closed || timerCalls != 2 {
		t.Fatalf("close=%v listener=%v timer calls=%d", server.closeCalled, listener.closed, timerCalls)
	}
	release()
	assertChannelClosed(t, server.done, "released stubborn server goroutine")
}

func TestPKCEServerErrorSurfacedAndCleansUp(t *testing.T) {
	listener := &fakeListener{}
	timer := &fakeTimer{ch: make(chan time.Time)}
	serveFailure := errors.New("serve failed")
	server := &errorServer{err: serveFailure, done: make(chan struct{})}
	c, _, _ := captureCLI()
	c.authRuntime = authRuntime{
		random:        bytes.NewReader(bytes.Repeat([]byte{1}, 64)),
		listen:        func(string, string) (net.Listener, error) { return listener, nil },
		server:        func(http.Handler) authHTTPServer { return server },
		launchBrowser: func(string) error { return nil },
		newTimer: func(duration time.Duration) authTimer {
			if duration != time.Second {
				t.Fatalf("callback timeout = %v", duration)
			}
			return timer
		},
		httpClient:   doerFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected exchange"); return nil, nil }),
		authorizeURL: "https://auth.invalid", keysURL: "https://keys.invalid", callbackWait: time.Second, exchangeWait: time.Second, serverStopWait: time.Second,
	}
	_, err := c.pkceLogin(context.Background())
	if err == nil || !strings.Contains(err.Error(), serveFailure.Error()) {
		t.Fatalf("error = %v", err)
	}
	if !listener.closed || !server.closed || !timer.stopped {
		t.Fatal("resources not cleaned up")
	}
	assertChannelClosed(t, server.done, "error server goroutine")
}

type stubbornServer struct {
	handler     http.Handler
	release     chan struct{}
	done        chan struct{}
	closeCalled bool
}

func (s *stubbornServer) Serve(net.Listener) error {
	defer close(s.done)
	s.handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, callbackPath+"?code=auth-code", nil))
	<-s.release
	return http.ErrServerClosed
}

func (s *stubbornServer) Close() error {
	s.closeCalled = true
	return nil
}

type malformedBlockingServer struct {
	handler         http.Handler
	closed          chan struct{}
	done            chan struct{}
	timer           *fakeTimer
	once            sync.Once
	malformedStatus int
}

func (s *malformedBlockingServer) Serve(net.Listener) error {
	defer close(s.done)
	recorder := httptest.NewRecorder()
	s.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, callbackPath+"?code=a&code=b", nil))
	s.malformedStatus = recorder.Code
	s.timer.ch <- time.Now()
	<-s.closed
	return http.ErrServerClosed
}

func (s *malformedBlockingServer) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func assertChannelClosed(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("%s did not complete", name)
	}
}

type errorServer struct {
	err    error
	closed bool
	done   chan struct{}
}

func (s *errorServer) Serve(net.Listener) error { defer close(s.done); return s.err }
func (s *errorServer) Close() error             { s.closed = true; return nil }

type blockingServer struct {
	closed    chan struct{}
	done      chan struct{}
	once      sync.Once
	wasClosed bool
}

func (s *blockingServer) Serve(net.Listener) error {
	defer close(s.done)
	<-s.closed
	return http.ErrServerClosed
}
func (s *blockingServer) Close() error {
	s.wasClosed = true
	s.once.Do(func() { close(s.closed) })
	return nil
}

func TestAuthStatusExplicitKeyUsesStdoutWithoutDisclosure(t *testing.T) {
	secret := "status-secret-key"
	c, out, errOut := captureCLI()
	c.authSourceWithError = func(provider, explicit string) (string, error) {
		if provider != "openrouter" || explicit != secret {
			t.Fatalf("source arguments = %q, %q", provider, explicit)
		}
		return "flag", nil
	}
	if got := c.run([]string{"auth", "status", "--api-key", secret}); got != exitOK {
		t.Fatalf("exit = %d", got)
	}
	if !strings.Contains(out.String(), "authenticated (flag)") || errOut.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", out.String(), errOut.String())
	}
	if strings.Contains(out.String(), secret) || strings.Contains(errOut.String(), secret) {
		t.Fatal("status output disclosed API key")
	}
}

func TestAuthLoginOpenAIWarnsProviderIsNotUsable(t *testing.T) {
	const secret = "openai-test-secret"
	c, out, errOut := captureCLI()
	c.authReadLine = func() (string, error) { return secret, nil }
	c.authStore = func(providerName, key string) error {
		if providerName != "openai" || key != secret {
			t.Fatalf("store arguments = %q, %q", providerName, key)
		}
		return nil
	}
	c.authSourceWithError = func(providerName, explicit string) (string, error) {
		if providerName != "openai" || explicit != "" {
			t.Fatalf("source arguments = %q, %q", providerName, explicit)
		}
		return "config file", nil
	}

	if got := c.run([]string{"auth", "login", "--provider", "openai"}); got != exitOK {
		t.Fatalf("exit = %d, want %d; stderr: %s", got, exitOK, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
	for _, want := range []string{
		"OpenAI credentials can be stored, but the provider is not usable yet.",
		"Paste your openai API key:",
		"Stored openai credentials (config file).",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout = %q, want to contain %q", out.String(), want)
		}
	}
	if strings.Contains(out.String(), secret) {
		t.Fatal("login output disclosed API key")
	}
}

func TestAuthLoginPrintsNonFatalStoreWarningAndSucceeds(t *testing.T) {
	c, out, errOut := captureCLI()
	warning := errors.New("stale credential file cleanup needs attention")
	c.authStoreDetailed = func(providerName, key string) (auth.StoreResult, error) {
		if providerName != "anthropic" || key != "secret" {
			t.Fatalf("store arguments = %q, %q", providerName, key)
		}
		return auth.StoreResult{Warning: warning}, nil
	}
	c.authSourceWithError = func(string, string) (string, error) { return "keyring", nil }

	if got := c.storeKey("anthropic", "secret"); got != exitOK {
		t.Fatalf("exit = %d, want %d", got, exitOK)
	}
	if !strings.Contains(errOut.String(), "clai auth login: warning: "+warning.Error()) {
		t.Fatalf("stderr = %q, want non-fatal store warning", errOut.String())
	}
	if !strings.Contains(out.String(), "Stored anthropic credentials (keyring).") {
		t.Fatalf("stdout = %q, want success message", out.String())
	}
}

func TestAuthStatusNotAuthenticatedIncludesLoginHint(t *testing.T) {
	c, out, errOut := captureCLI()
	c.authSourceWithError = func(providerName, explicit string) (string, error) {
		if providerName != "anthropic" || explicit != "" {
			t.Fatalf("source arguments = %q, %q", providerName, explicit)
		}
		return "none", nil
	}
	if got := c.run([]string{"auth", "status", "--provider", "anthropic"}); got != exitError {
		t.Fatalf("exit = %d, want %d", got, exitError)
	}
	want := "anthropic: not authenticated; run `clai auth login --provider anthropic`\n"
	if out.String() != want || errOut.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q, want stdout %q", out.String(), errOut.String(), want)
	}
}

func TestSourceWithErrorCallerBehavior(t *testing.T) {
	inspectionErr := errors.New("unsafe credential permissions")
	t.Run("status", func(t *testing.T) {
		c, out, errOut := captureCLI()
		c.authSourceWithError = func(string, string) (string, error) { return "", inspectionErr }
		if got := c.run([]string{"auth", "status"}); got != exitError {
			t.Fatalf("exit = %d", got)
		}
		if out.Len() != 0 || !strings.Contains(errOut.String(), inspectionErr.Error()) || strings.Contains(errOut.String(), "not authenticated") {
			t.Fatalf("stdout=%q stderr=%q", out.String(), errOut.String())
		}
	})
	t.Run("store reporting", func(t *testing.T) {
		c, out, errOut := captureCLI()
		c.authStore = func(string, string) error { return nil }
		c.authSourceWithError = func(string, string) (string, error) { return "", inspectionErr }
		if got := c.storeKey("openrouter", "secret"); got != exitError {
			t.Fatalf("exit = %d", got)
		}
		if strings.Contains(out.String(), "(none)") || !strings.Contains(errOut.String(), inspectionErr.Error()) {
			t.Fatalf("stdout=%q stderr=%q", out.String(), errOut.String())
		}
	})
}

// TestAuthCommandsRemainNoninteractiveAfterRootMigration proves status and
// logout stay bounded command paths that use only their injected auth
// operations after config/credential state is co-located under the preferred
// root (REQ-CONFIG-006/008, T-01-GC-26).
func TestAuthCommandsRemainNoninteractiveAfterRootMigration(t *testing.T) {
	const credentialSentinel = "auth-command-fixture-secret"
	prepareMigratedAuthCommandRoot(t, credentialSentinel)

	cases := []struct {
		name             string
		args             []string
		wantCode         int
		wantStdout       string
		wantSourceCalls  int
		wantDeleteCalls  int
		configureCommand func(*cli, *int, *int)
	}{
		{
			name:            "status uses only injected source inspection",
			args:            []string{"auth", "status", "--provider", "openrouter"},
			wantCode:        exitOK,
			wantStdout:      "openrouter: authenticated (config file)\n",
			wantSourceCalls: 1,
			configureCommand: func(c *cli, sourceCalls, _ *int) {
				c.authSourceWithError = func(providerName, explicit string) (string, error) {
					*sourceCalls++
					if providerName != "openrouter" || explicit != "" {
						return "", errors.New("unexpected status arguments")
					}
					return "config file", nil
				}
			},
		},
		{
			name:            "logout uses only injected delete",
			args:            []string{"auth", "logout", "--provider", "openrouter"},
			wantCode:        exitOK,
			wantStdout:      "Removed stored openrouter credentials.\n",
			wantDeleteCalls: 1,
			configureCommand: func(c *cli, _, deleteCalls *int) {
				c.authDelete = func(providerName string) error {
					*deleteCalls++
					if providerName != "openrouter" {
						return errors.New("unexpected logout provider")
					}
					return nil
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var unexpected []string
			recordUnexpected := func(name string) { unexpected = append(unexpected, name) }
			sourceCalls, deleteCalls := 0, 0
			c, out, errOut := captureCLI()
			c.authReadLine = func() (string, error) {
				recordUnexpected("stdin")
				return "", errors.New("unexpected input")
			}
			c.authStore = func(string, string) error {
				recordUnexpected("store")
				return errors.New("unexpected store")
			}
			c.authSourceWithError = func(string, string) (string, error) {
				recordUnexpected("real source fallback")
				return "", errors.New("unexpected source fallback")
			}
			c.authDelete = func(string) error {
				recordUnexpected("real delete fallback")
				return errors.New("unexpected delete fallback")
			}
			c.authRuntime.listen = func(string, string) (net.Listener, error) {
				recordUnexpected("listener")
				return nil, errors.New("unexpected listener")
			}
			c.authRuntime.server = func(http.Handler) authHTTPServer {
				recordUnexpected("server")
				return nil
			}
			c.authRuntime.launchBrowser = func(string) error {
				recordUnexpected("browser")
				return errors.New("unexpected browser")
			}
			c.authRuntime.newTimer = func(time.Duration) authTimer {
				recordUnexpected("timer")
				return &fakeTimer{ch: make(chan time.Time)}
			}
			c.authRuntime.withTimeout = func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
				recordUnexpected("timeout")
				return context.WithCancel(parent)
			}
			c.authRuntime.httpClient = doerFunc(func(*http.Request) (*http.Response, error) {
				recordUnexpected("network")
				return nil, errors.New("unexpected network")
			})
			tc.configureCommand(&c, &sourceCalls, &deleteCalls)

			originalTUI := executeTUI
			executeTUI = func(provider.Provider, *capability.Cached, string, string) (app.Outcome, error) {
				recordUnexpected("TUI")
				return app.Outcome{}, errors.New("unexpected TUI")
			}
			t.Cleanup(func() { executeTUI = originalTUI })

			done := make(chan int, 1)
			go func() { done <- c.run(tc.args) }()
			var code int
			select {
			case code = <-done:
			case <-time.After(time.Second):
				t.Fatal("noninteractive auth command blocked past its deadline")
			}
			if code != tc.wantCode {
				t.Fatalf("exit = %d, want %d; stdout=%q stderr=%q", code, tc.wantCode, out.String(), errOut.String())
			}
			if out.String() != tc.wantStdout || errOut.String() != "" {
				t.Fatalf("streams = stdout %q stderr %q, want stdout %q and empty stderr", out.String(), errOut.String(), tc.wantStdout)
			}
			if sourceCalls != tc.wantSourceCalls || deleteCalls != tc.wantDeleteCalls {
				t.Fatalf("injected calls = source:%d delete:%d, want %d/%d", sourceCalls, deleteCalls, tc.wantSourceCalls, tc.wantDeleteCalls)
			}
			if len(unexpected) != 0 {
				t.Fatalf("noninteractive auth command reached forbidden paths: %v", unexpected)
			}
			if strings.Contains(out.String(), credentialSentinel) || strings.Contains(errOut.String(), credentialSentinel) {
				t.Fatal("auth command disclosed the file credential fixture")
			}
		})
	}
}

func prepareMigratedAuthCommandRoot(t *testing.T, credential string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize auth-command root: %v", err)
	}
	home := filepath.Join(root, "home")
	xdg := filepath.Join(root, "xdg")
	for _, path := range []string{home, xdg} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create isolated auth-command root: %v", err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	for _, name := range []string{"OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(name, "")
	}

	legacyConfig := filepath.Join(home, "Library", "Application Support", "clai", "config.json")
	if runtime.GOOS == "darwin" {
		if err := os.MkdirAll(filepath.Dir(legacyConfig), 0o700); err != nil {
			t.Fatalf("create legacy config directory: %v", err)
		}
		if err := os.WriteFile(legacyConfig, []byte(`{"contract":"legacy","provider":"rules","init_completed":true}`), 0o600); err != nil {
			t.Fatalf("write legacy config fixture: %v", err)
		}
		if _, err := config.Load(); err != nil {
			t.Fatalf("migrate config before auth commands: %v", err)
		}
		if _, err := os.Lstat(legacyConfig); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy config remains before auth command proof: %v", err)
		}
	} else if err := config.Save(config.Config{Provider: "rules", InitCompleted: true}); err != nil {
		t.Fatalf("seed preferred config before auth commands: %v", err)
	}

	preferredApp := filepath.Join(xdg, "clai")
	preferredConfig := filepath.Join(preferredApp, "config.json")
	preferredCredentials := filepath.Join(preferredApp, "credentials.json")
	if _, err := os.Stat(preferredConfig); err != nil {
		t.Fatalf("preferred config missing before auth commands: %v", err)
	}
	if err := os.WriteFile(preferredCredentials, []byte(`{"openrouter":"`+credential+`"}`), 0o600); err != nil {
		t.Fatalf("write preferred credential fixture: %v", err)
	}
	for _, path := range []string{preferredConfig, preferredCredentials} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("inspect preferred storage %q: %v", path, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("preferred storage %q mode/type = %v, want regular 0600", path, info.Mode())
		}
	}
}
