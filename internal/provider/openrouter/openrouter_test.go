package openrouter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
)

func TestCompileRequiresAPIKey(t *testing.T) {
	p := Provider{}
	_, err := p.Compile(context.Background(), provider.Request{Intent: "list files"})
	if err == nil || !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("err = %v, want no-API-key error", err)
	}
}

func TestLocalOnlyRemoteFailsBeforeClient(t *testing.T) {
	var called atomic.Bool
	p := Provider{
		APIKey: "secret", Policy: machinecontext.PolicyLocalOnly,
		client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			called.Store(true)
			return nil, errors.New("client must not run")
		})},
	}
	_, err := p.Compile(context.Background(), provider.Request{Intent: "list files"})
	if err == nil || !strings.Contains(err.Error(), "local-only") {
		t.Fatalf("err = %v, want local-only refusal", err)
	}
	if called.Load() {
		t.Fatal("HTTP client ran before local-only refusal")
	}
}

func TestCompileRejectsEndpointUserinfoBeforeTransportOrReceipt(t *testing.T) {
	for _, endpoint := range []string{
		"https://username@openrouter.ai/v1",
		"https://username:password@openrouter.ai/v1",
	} {
		t.Run(endpoint, func(t *testing.T) {
			var transported, receipted atomic.Bool
			p := Provider{
				APIKey: "secret", endpoint: endpoint,
				client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					transported.Store(true)
					return nil, errors.New("must not transport")
				})},
				receiptSink: func(RequestReceipt) { receipted.Store(true) },
			}
			_, err := p.Compile(context.Background(), provider.Request{Intent: "list files"})
			if err == nil || err.Error() != "openrouter: provider endpoint must not contain userinfo" {
				t.Fatalf("err = %v, want userinfo rejection", err)
			}
			if transported.Load() || receipted.Load() {
				t.Fatal("userinfo endpoint reached transport or receipt")
			}
		})
	}
}

func TestCompileDisablesRedirects(t *testing.T) {
	for _, status := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requestBody []byte
			var authorization, method string
			var receipt RequestReceipt
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method = r.Method
				authorization = r.Header.Get("Authorization")
				requestBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Location", "https://example.invalid/remote-target")
				w.WriteHeader(status)
				io.WriteString(w, "SENTINEL redirect body must not be reflected")
			}))
			defer server.Close()

			var injectedRedirectCalled atomic.Bool
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
				injectedRedirectCalled.Store(true)
				return nil
			}}
			p := Provider{
				APIKey: "secret", endpoint: server.URL, Policy: machinecontext.PolicyRemoteMinimal,
				client: client, receiptSink: func(got RequestReceipt) { receipt = got },
			}
			_, err := p.Compile(context.Background(), provider.Request{
				Intent: "list files", Context: machinecontext.Context{OS: "linux", Shell: "bash"},
			})
			wantErr := fmt.Sprintf("openrouter: HTTP status %d", status)
			if err == nil || err.Error() != wantErr || strings.Contains(err.Error(), "SENTINEL") {
				t.Fatalf("err = %v, want %q", err, wantErr)
			}
			if injectedRedirectCalled.Load() {
				t.Fatal("injected redirect policy ran; provider did not force fail-closed behavior")
			}
			if method != http.MethodPost || authorization != "Bearer secret" {
				t.Fatalf("initial request method/auth = %q/%q", method, authorization)
			}
			if !bytes.Equal(requestBody, receipt.RequestBody) {
				t.Fatalf("initial body differs from receipt")
			}
			if receipt.EffectiveEndpoint != server.URL || receipt.EndpointClassification != machinecontext.EndpointLoopback {
				t.Fatalf("receipt endpoint changed across refused redirect: %+v", receipt)
			}
		})
	}
}

func TestCompileDoesNotReflectCustomHTTPReasonOrBody(t *testing.T) {
	const sentinel = "SENTINEL-untrusted-reason"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 599,
			Status:     "599 " + sentinel,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(sentinel + "-body")),
			Request:    request,
		}, nil
	})}
	_, err := (Provider{APIKey: "test", endpoint: "http://localhost/v1", client: client}).Compile(
		context.Background(), provider.Request{Intent: "list files"},
	)
	// 599 is a 5xx, so it now classifies as a transient upstream error. The
	// message must remain a stable, deterministic function of the status code
	// only: no upstream reason phrase or body may leak into it.
	const wantErr = "openrouter: upstream server error (HTTP 599): transient upstream failure, retry later"
	if err == nil || err.Error() != wantErr {
		t.Fatalf("err = %v, want %q", err, wantErr)
	}
	if !errors.Is(err, ErrServer) {
		t.Fatalf("err = %v, want errors.Is ErrServer", err)
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("untrusted response text reflected in error: %v", err)
	}
}

func TestCompileHonorsCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	defer server.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := (Provider{APIKey: "test", endpoint: server.URL}).Compile(ctx, provider.Request{Intent: "list files"})
		result <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("OpenRouter request did not start")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("OpenRouter request did not observe caller cancellation")
	}
}

func TestCompileHonorsCancellationWhileReadingResponse(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		<-release
	}))
	defer server.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := (Provider{APIKey: "test", endpoint: server.URL}).Compile(ctx, provider.Request{Intent: "list files"})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("response body read did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("response body read did not observe cancellation")
	}
}

func TestCompileTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	_, err := (Provider{APIKey: "test", endpoint: server.URL, timeout: 20 * time.Millisecond}).Compile(
		context.Background(), provider.Request{Intent: "list files"},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestRemoteMinimalGoldenBodyHashAndForbiddenAbsence(t *testing.T) {
	selection, err := machinecontext.Select(machinecontext.Context{
		OS: "darwin", Shell: "/bin/fish", WorkingDirectory: "/SENTINEL/absolute/path",
		GitRepository: true, GitRoot: "/SENTINEL/git/root", GitBranch: "SENTINEL-private-branch",
	}, machinecontext.PolicyRemoteMinimal, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := buildRequestBody("example/model", "list files", selection)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"example/model","messages":[{"role":"system","content":"You convert a natural-language intent into a single shell command.\n\nRules:\n- Reply with ONLY a JSON object: {\"command\": \"...\", \"explanation\": \"...\"}.\n- The command must be a single line, safe to paste into the user's shell.\n- The explanation is one short sentence saying why this command fits.\n- Never include markdown fences or extra prose.\n- Use only the environment context included in the user message."},{"role":"user","content":"{\"intent\":\"list files\",\"context\":{\"os_family\":\"darwin\",\"shell_family\":\"fish\",\"project_kind\":\"git\"}}"}]}`
	if string(body) != want {
		t.Fatalf("body mismatch\n got: %s\nwant: %s", body, want)
	}
	for _, forbidden := range []string{"/SENTINEL", "SENTINEL-private-branch", "hostname", "username", "credential"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Errorf("body contains forbidden sentinel %q", forbidden)
		}
	}

	receipt := makeReceipt("example/model", defaultEndpoint, machinecontext.EndpointRemote, "direct", selection, body)
	wantHash := sha256.Sum256(body)
	if receipt.RequestBodyHash.Algorithm != "sha256" || receipt.RequestBodyHash.Value != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("hash = %+v", receipt.RequestBodyHash)
	}
	body2, _ := buildRequestBody("example/model", "list files", selection)
	if !bytes.Equal(body, body2) {
		t.Fatal("deterministic request body changed for fixed input")
	}
}

func TestRemoteExplicitIncludesOnlyApprovedAvailableFields(t *testing.T) {
	selection, err := machinecontext.Select(machinecontext.Context{
		OS: "linux", Shell: "zsh", WorkingDirectory: "/work", GitRepository: true, GitRoot: "/work",
	}, machinecontext.PolicyRemoteExplicit, []string{machinecontext.FieldWorkingDirectory, machinecontext.FieldGitBranch})
	if err != nil {
		t.Fatal(err)
	}
	body, err := buildRequestBody("model", "status", selection)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`/work`)) {
		t.Fatalf("body omitted approved working directory: %s", body)
	}
	if bytes.Contains(body, []byte(`git_root`)) {
		t.Fatalf("body included unapproved git root: %s", body)
	}
	receipt := makeReceipt("model", defaultEndpoint, machinecontext.EndpointRemote, "direct", selection, body)
	if len(receipt.OmittedFields) != 1 || receipt.OmittedFields[0].Name != machinecontext.FieldGitBranch {
		t.Fatalf("omitted fields = %+v", receipt.OmittedFields)
	}
}

func TestLoopbackEndpointDefaultsLocalOnly(t *testing.T) {
	var receipt RequestReceipt
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"command\":\"pwd\",\"explanation\":\"prints cwd\"}"}}]}`)
	}))
	defer server.Close()

	p := Provider{APIKey: "test", endpoint: server.URL, receiptSink: func(got RequestReceipt) { receipt = got }}
	_, err := p.Compile(context.Background(), provider.Request{
		Intent: "where am I", Context: machinecontext.Context{OS: "linux", Shell: "bash", WorkingDirectory: "/must-not-leave"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ContextPolicy != machinecontext.PolicyLocalOnly {
		t.Fatalf("policy = %q, want local-only", receipt.ContextPolicy)
	}
	if bytes.Contains(receipt.RequestBody, []byte("/must-not-leave")) || len(receipt.SelectedFields) != 0 {
		t.Fatalf("local-only receipt shared context: %+v body=%s", receipt.SelectedFields, receipt.RequestBody)
	}
}

func TestActualTransportBytesEqualReceipt(t *testing.T) {
	var transported []byte
	var receipt RequestReceipt
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		transported, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"command\":\"pwd\",\"explanation\":\"prints cwd\"}"}}]}`)
	}))
	defer server.Close()

	p := Provider{
		APIKey: "not-in-receipt", endpoint: server.URL, Policy: machinecontext.PolicyRemoteMinimal,
		receiptSink: func(got RequestReceipt) { receipt = got },
	}
	candidates, err := p.Compile(context.Background(), provider.Request{
		Intent: "where am I", Context: machinecontext.Context{OS: "linux", Shell: "bash", WorkingDirectory: "/private"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Command != "pwd" {
		t.Fatalf("candidates = %+v", candidates)
	}
	if !bytes.Equal(transported, receipt.RequestBody) {
		t.Fatalf("transport bytes differ from receipt\ntransport: %s\nreceipt: %s", transported, receipt.RequestBody)
	}
	if bytes.Contains(receipt.RequestBody, []byte("not-in-receipt")) || strings.Contains(receipt.RequestBodyHash.Value, "not-in-receipt") {
		t.Fatal("credential leaked into receipt")
	}
	if receipt.Version != "request-receipt/v1" || receipt.Provider != "openrouter" || receipt.Model != DefaultModel || receipt.EndpointClassification != machinecontext.EndpointLoopback || receipt.SelectorVersion != machinecontext.SelectorVersion {
		t.Fatalf("receipt metadata = %+v", receipt)
	}
	if len(receipt.SelectedFields) < 2 || receipt.SelectedFields[1].Name != machinecontext.FieldShellFamily || receipt.SelectedFields[1].Provenance != "provider-request" {
		t.Fatalf("manual provider request shell provenance = %+v, want provider-request", receipt.SelectedFields)
	}
}

func TestProxyModeExcludesProxySecret(t *testing.T) {
	proxySecret := "proxy-user:proxy-password"
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = func(*http.Request) (*url.URL, error) {
		return url.Parse("http://" + proxySecret + "@proxy.example:8080")
	}
	request, err := http.NewRequest(http.MethodPost, defaultEndpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := classifyProxyMode(&http.Client{Transport: transport}, request); got != "configured-proxy" {
		t.Fatalf("proxy mode = %q", got)
	}
	selection, _ := machinecontext.Select(machinecontext.Context{}, machinecontext.PolicyRemoteMinimal, nil)
	receipt := makeReceipt("model", defaultEndpoint, machinecontext.EndpointRemote, "configured-proxy", selection, []byte(`{"safe":true}`))
	if strings.Contains(string(receipt.RequestBody), proxySecret) || strings.Contains(receipt.EffectiveEndpoint, proxySecret) || strings.Contains(receipt.RequestBodyHash.Value, proxySecret) {
		t.Fatal("proxy secret leaked into receipt")
	}
}

func TestReadResponseBodySizeBoundary(t *testing.T) {
	for _, tt := range []struct {
		name    string
		size    int
		wantErr bool
	}{
		{"limit-minus-one", maxResponseBytes - 1, false},
		{"limit", maxResponseBytes, false},
		{"limit-plus-one", maxResponseBytes + 1, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, err := readResponseBody(bytes.NewReader(bytes.Repeat([]byte{'x'}, tt.size)))
			if tt.wantErr {
				if err == nil || err.Error() != "openrouter: response exceeds size limit" {
					t.Fatalf("err = %v, want size-limit error", err)
				}
				if body != nil {
					t.Fatalf("oversize body returned %d bytes", len(body))
				}
				return
			}
			if err != nil || len(body) != tt.size {
				t.Fatalf("len(body), err = %d, %v; want %d, nil", len(body), err, tt.size)
			}
		})
	}
}

func TestResponseContentWhitespaceAndTrailingContent(t *testing.T) {
	valid := `{"choices":[{"message":{"content":"{\"command\":\"pwd\",\"explanation\":\"prints cwd\"}"}}]}`
	if _, err := responseContent([]byte(valid + " \n\t")); err != nil {
		t.Fatalf("valid response with trailing whitespace: %v", err)
	}
	if _, err := responseContent([]byte(valid + `{}`)); err == nil {
		t.Fatal("valid response prefix with trailing JSON content was accepted")
	}
}

func TestCompileRejectsOversizeValidPrefix(t *testing.T) {
	valid := `{"choices":[{"message":{"content":"{\"command\":\"pwd\",\"explanation\":\"prints cwd\"}"}}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, valid)
		io.WriteString(w, strings.Repeat(" ", maxResponseBytes+1-len(valid)))
	}))
	defer server.Close()

	_, err := (Provider{APIKey: "test", endpoint: server.URL}).Compile(context.Background(), provider.Request{Intent: "pwd"})
	if err == nil || err.Error() != "openrouter: response exceeds size limit" {
		t.Fatalf("err = %v, want size-limit rejection", err)
	}
}

func TestParseCandidate(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		command string
		wantErr bool
	}{
		{"plain json", `{"command":"git status","explanation":"shows status"}`, "git status", false},
		{"fenced json", "```json\n{\"command\":\"ls -la\",\"explanation\":\"lists files\"}\n```", "ls -la", false},
		{"whitespace", "  \n{\"command\":\"pwd\",\"explanation\":\"prints cwd\"}\n", "pwd", false},
		{"invalid json", "not json at all", "", true},
		{"missing command", `{"explanation":"nothing"}`, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate, err := parseCandidate(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseCandidate(%q) = %+v, want error", tt.raw, candidate)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCandidate(%q): %v", tt.raw, err)
			}
			if candidate.Command != tt.command {
				t.Errorf("Command = %q, want %q", candidate.Command, tt.command)
			}
			if candidate.Explanation == "" {
				t.Error("Explanation is empty")
			}
		})
	}
}

func TestReceiptFieldOrdering(t *testing.T) {
	selection, _ := machinecontext.Select(machinecontext.Context{OS: "linux", Shell: "bash", WorkingDirectory: "/x"}, machinecontext.PolicyRemoteMinimal, nil)
	receipt := makeReceipt("m", defaultEndpoint, machinecontext.EndpointRemote, "direct", selection, []byte("{}"))
	got := []string{}
	for _, field := range receipt.SelectedFields {
		got = append(got, field.Name)
	}
	if want := []string{machinecontext.FieldOSFamily, machinecontext.FieldShellFamily, machinecontext.FieldProjectKind}; !reflect.DeepEqual(got, want) {
		t.Fatalf("selected order = %v, want %v", got, want)
	}
}

func TestCompileClassifiesHTTPErrors(t *testing.T) {
	const apiKey = "super-secret-key"
	tests := []struct {
		name        string
		status      int
		retryAfter  string
		sentinel    error
		wantSubstrs []string
		notSubstrs  []string
	}{
		{"unauthorized", http.StatusUnauthorized, "", ErrAuth,
			[]string{"openrouter:", "authentication error", "HTTP 401", "re-authenticate"}, nil},
		{"forbidden", http.StatusForbidden, "", ErrAuth,
			[]string{"authentication error", "HTTP 403"}, nil},
		{"rate-limited-with-retry-after", http.StatusTooManyRequests, "42", ErrRateLimited,
			[]string{"rate limited", "HTTP 429", "retry after 42", "back off"}, nil},
		{"rate-limited-no-retry-after", http.StatusTooManyRequests, "", ErrRateLimited,
			[]string{"rate limited", "HTTP 429", "back off"}, []string{"retry after"}},
		{"internal-server-error", http.StatusInternalServerError, "", ErrServer,
			[]string{"upstream server error", "HTTP 500", "retry later"}, nil},
		{"service-unavailable", http.StatusServiceUnavailable, "", ErrServer,
			[]string{"upstream server error", "HTTP 503", "retry later"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.retryAfter != "" {
					w.Header().Set("Retry-After", tt.retryAfter)
				}
				w.WriteHeader(tt.status)
				io.WriteString(w, "SENTINEL upstream body must not leak")
			}))
			defer server.Close()

			_, err := (Provider{APIKey: apiKey, endpoint: server.URL}).Compile(
				context.Background(), provider.Request{Intent: "list files"},
			)
			if err == nil {
				t.Fatalf("status %d: err = nil, want classified error", tt.status)
			}
			if !errors.Is(err, tt.sentinel) {
				t.Fatalf("status %d: err = %v, want errors.Is %v", tt.status, err, tt.sentinel)
			}
			for _, sub := range tt.wantSubstrs {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("status %d: err = %q, want substring %q", tt.status, err.Error(), sub)
				}
			}
			for _, sub := range tt.notSubstrs {
				if strings.Contains(err.Error(), sub) {
					t.Errorf("status %d: err = %q, must not contain %q", tt.status, err.Error(), sub)
				}
			}
			if strings.Contains(err.Error(), apiKey) {
				t.Fatalf("status %d: API key leaked into error: %v", tt.status, err)
			}
			if strings.Contains(err.Error(), "SENTINEL") {
				t.Fatalf("status %d: upstream body leaked into error: %v", tt.status, err)
			}
		})
	}
}

func TestCompileGenericStatusFallback(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusTeapot} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()

			_, err := (Provider{APIKey: "test", endpoint: server.URL}).Compile(
				context.Background(), provider.Request{Intent: "list files"},
			)
			want := fmt.Sprintf("openrouter: HTTP status %d", status)
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
			if errors.Is(err, ErrAuth) || errors.Is(err, ErrRateLimited) || errors.Is(err, ErrServer) {
				t.Fatalf("generic status %d matched a specific sentinel: %v", status, err)
			}
		})
	}
}

func TestCompileSucceedsOn200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"command\":\"ls -la\",\"explanation\":\"lists files\"}"}}]}`)
	}))
	defer server.Close()

	candidates, err := (Provider{APIKey: "test", endpoint: server.URL}).Compile(
		context.Background(), provider.Request{Intent: "list files"},
	)
	if err != nil {
		t.Fatalf("200 response failed: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Command != "ls -la" {
		t.Fatalf("candidates = %+v, want single ls -la candidate", candidates)
	}
}

func TestSanitizeRetryAfter(t *testing.T) {
	if got := sanitizeRetryAfter("120"); got != "120" {
		t.Errorf("seconds: got %q, want %q", got, "120")
	}
	if got := sanitizeRetryAfter("  30  "); got != "30" {
		t.Errorf("trimmed: got %q, want %q", got, "30")
	}
	if got := sanitizeRetryAfter("Wed, 21 Oct 2015 07:28:00 GMT"); got != "Wed, 21 Oct 2015 07:28:00 GMT" {
		t.Errorf("http-date: got %q", got)
	}
	if got := sanitizeRetryAfter(""); got != "" {
		t.Errorf("empty: got %q, want empty", got)
	}
	// Control/escape sequences from a hostile upstream must not survive verbatim
	// into a terminal-rendered error.
	for _, raw := range []string{"30\x1b[31mred", "30\ninjected", "30\r\n"} {
		if got := sanitizeRetryAfter(raw); strings.ContainsAny(got, "\x1b\n\r") {
			t.Errorf("sanitizeRetryAfter(%q) = %q still contains a raw control character", raw, got)
		}
	}
	// The rendered value is hard-bounded regardless of input length.
	if got := sanitizeRetryAfter(strings.Repeat("9", 500)); len([]rune(got)) > maxRetryAfterRunes {
		t.Errorf("length not bounded: %d runes", len([]rune(got)))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
