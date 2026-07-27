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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Vedaant-Rajoo/clai/internal/capability"
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
	}, capability.Inventory{}, machinecontext.PolicyRemoteMinimal, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := buildRequestBody("example/model", "list files", selection)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"example/model","messages":[{"role":"system","content":"You convert a natural-language intent into a single shell command candidate.\n\nRules:\n- Reply with ONLY one candidate/v2 JSON object containing command, explanation, and optional requirements.\n- requirements is an array of zero to eight objects with kind tool, shell, or os; name must be lowercase ASCII using only a-z, 0-9, dot, underscore, or hyphen.\n- min_version is optional and may be used only for a tool requirement as a numeric dotted version.\n- The command must be a single line, safe to paste into the user's shell.\n- The explanation is one short sentence saying why this command fits the supplied capability facts.\n- Use only the normalized capability and environment facts included in the user message; never infer executable paths or raw probe output.\n- Declare requirements that the command actually depends on.\n- Never include markdown fences or extra prose."},{"role":"user","content":"{\"intent\":\"list files\",\"context\":{\"os_family\":\"unknown\",\"shell_family\":\"unknown\",\"project_kind\":\"git\",\"platform_arch\":\"unknown\",\"tools\":[{\"name\":\"git\",\"status\":\"absent\"},{\"name\":\"rg\",\"status\":\"absent\"},{\"name\":\"fd\",\"status\":\"absent\"},{\"name\":\"jq\",\"status\":\"absent\"},{\"name\":\"curl\",\"status\":\"absent\"},{\"name\":\"wget\",\"status\":\"absent\"},{\"name\":\"tar\",\"status\":\"absent\"},{\"name\":\"sed\",\"status\":\"absent\"},{\"name\":\"awk\",\"status\":\"absent\"},{\"name\":\"grep\",\"status\":\"absent\"},{\"name\":\"lsof\",\"status\":\"absent\"},{\"name\":\"ifconfig\",\"status\":\"absent\"},{\"name\":\"ip\",\"status\":\"absent\"},{\"name\":\"bash\",\"status\":\"absent\"}]}}"}]}`
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
	}, capability.Inventory{}, machinecontext.PolicyRemoteExplicit, []string{machinecontext.FieldWorkingDirectory, machinecontext.FieldGitBranch})
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
	if len(receipt.OmittedFields) != 2 || receipt.OmittedFields[0].Name != machinecontext.FieldGitRoot || receipt.OmittedFields[1].Name != machinecontext.FieldGitBranch {
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
	if len(receipt.SelectedFields) < 2 || receipt.SelectedFields[1].Name != machinecontext.FieldShellFamily || !reflect.DeepEqual(receipt.SelectedFields[1].Provenance, []string{"SHELL"}) {
		t.Fatalf("provider shell provenance = %+v, want [SHELL]", receipt.SelectedFields)
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
	selection, _ := machinecontext.Select(machinecontext.Context{}, capability.Inventory{}, machinecontext.PolicyRemoteMinimal, nil)
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

func TestOpenRouterCandidateV2SharedStrictDecoder(t *testing.T) {
	valid := `{"command":"rg TODO","explanation":"uses rg","requirements":[{"kind":"tool","name":"rg"}]}`
	candidate, err := parseCandidate(valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.Requirements) != 1 || candidate.Requirements[0].Name != "rg" {
		t.Fatalf("candidate = %+v", candidate)
	}

	tooMany := strings.Repeat(`{"kind":"tool","name":"rg"},`, 8) + `{"kind":"tool","name":"grep"}`
	for _, input := range []string{
		`{"command":"x"}`,
		`{"command":"x","explanation":"x","unknown":true}`,
		`{"command":"x","explanation":"x","requirements":[{"kind":"package","name":"git"}]}`,
		`{"command":"x","explanation":"x","requirements":[{"kind":"tool","name":"Git"}]}`,
		`{"command":"x","explanation":"x","requirements":[` + tooMany + `]}`,
	} {
		if _, err := parseCandidate(input); err == nil {
			t.Fatalf("parseCandidate(%s) accepted", input)
		}
	}
}

func TestOpenRouterFenceNormalizationThenStrictDecode(t *testing.T) {
	for _, input := range []string{
		"```json\n{\"command\":\"pwd\",\"explanation\":\"prints cwd\"}\n```",
		"```\n{\"command\":\"pwd\",\"explanation\":\"prints cwd\"}\n```",
	} {
		if _, err := parseCandidate(input); err != nil {
			t.Fatalf("valid fence rejected: %v", err)
		}
	}
	for _, input := range []string{
		"```json\n{\"command\":\"pwd\",\"explanation\":\"prints cwd\",\"extra\":true}\n```",
		"```json {\"command\":\"pwd\",\"explanation\":\"prints cwd\"}```",
		"```json\n{\"command\":\"pwd\",\"explanation\":\"prints cwd\"}",
	} {
		if _, err := parseCandidate(input); err == nil {
			t.Fatalf("invalid fenced payload accepted: %q", input)
		}
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
	selection, _ := machinecontext.Select(machinecontext.Context{OS: "linux", Shell: "bash", WorkingDirectory: "/x"}, capability.Inventory{}, machinecontext.PolicyRemoteMinimal, nil)
	receipt := makeReceipt("m", defaultEndpoint, machinecontext.EndpointRemote, "direct", selection, []byte("{}"))
	got := []string{}
	for _, field := range receipt.SelectedFields {
		got = append(got, field.Name)
	}
	want := []string{machinecontext.FieldOSFamily, machinecontext.FieldShellFamily, machinecontext.FieldProjectKind, machinecontext.FieldPlatformArch}
	for _, name := range capability.ToolNames() {
		want = append(want, "tool_"+name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected order = %v, want %v", got, want)
	}
}

func TestContextV2ReceiptMembershipAllPolicies(t *testing.T) {
	context := machinecontext.Context{
		WorkingDirectory: "/work",
		GitRepository:    true,
		GitRoot:          "/work",
		GitBranch:        "dev",
	}
	inventory := capability.Inventory{}
	universe := []string{machinecontext.FieldOSFamily, machinecontext.FieldShellFamily, machinecontext.FieldProjectKind, machinecontext.FieldPlatformArch}
	for _, name := range capability.ToolNames() {
		universe = append(universe, "tool_"+name)
	}
	universe = append(universe, machinecontext.FieldWorkingDirectory, machinecontext.FieldGitRoot, machinecontext.FieldGitBranch)

	tests := []struct {
		name         string
		policy       machinecontext.Policy
		shared       []string
		wantSelected []string
		wantOmitted  []string
	}{
		{"local-only", machinecontext.PolicyLocalOnly, nil, []string{}, universe},
		{"remote-minimal", machinecontext.PolicyRemoteMinimal, nil, universe[:len(universe)-3], universe[len(universe)-3:]},
		{"remote-explicit", machinecontext.PolicyRemoteExplicit, []string{machinecontext.FieldWorkingDirectory, machinecontext.FieldGitBranch}, append(append([]string(nil), universe[:len(universe)-3]...), machinecontext.FieldWorkingDirectory, machinecontext.FieldGitBranch), []string{machinecontext.FieldGitRoot}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selection, err := machinecontext.Select(context, inventory, tt.policy, tt.shared)
			if err != nil {
				t.Fatal(err)
			}
			body, err := buildRequestBody("model", "intent", selection)
			if err != nil {
				t.Fatal(err)
			}
			receipt := makeReceipt("model", defaultEndpoint, machinecontext.EndpointRemote, "direct", selection, body)
			wantHash := sha256.Sum256(body)
			if receipt.RequestBodyHash.Algorithm != "sha256" || receipt.RequestBodyHash.Value != hex.EncodeToString(wantHash[:]) || !bytes.Equal(receipt.RequestBody, body) {
				t.Fatalf("receipt body/hash mismatch: %+v", receipt.RequestBodyHash)
			}
			secondBody, err := buildRequestBody("model", "intent", selection)
			if err != nil || !bytes.Equal(secondBody, body) {
				t.Fatalf("request bytes are not deterministic: %v", err)
			}
			if got := receiptNames(receipt.SelectedFields); !reflect.DeepEqual(got, tt.wantSelected) {
				t.Fatalf("selected = %v, want %v", got, tt.wantSelected)
			}
			if len(receipt.RedactedFields) != 0 {
				t.Fatalf("redacted = %+v, want empty", receipt.RedactedFields)
			}
			if got := receiptNames(receipt.OmittedFields); !reflect.DeepEqual(got, tt.wantOmitted) {
				t.Fatalf("omitted = %v, want %v", got, tt.wantOmitted)
			}
		})
	}
}

func TestContextV2ProhibitedDataExcluded(t *testing.T) {
	inventory, executablePath := providerPrivacyInventory(t)
	selection, err := machinecontext.Select(machinecontext.Context{
		WorkingDirectory: "/SENTINEL/private",
		GitRepository:    true,
		GitRoot:          "/SENTINEL/private",
		GitBranch:        "SENTINEL-branch",
	}, inventory, machinecontext.PolicyRemoteMinimal, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := buildRequestBody("model", "intent", selection)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/SENTINEL", "SENTINEL-branch", "SENTINEL-RAW-PROBE-OUTPUT", executablePath, "hostname", "username", "credential", "git_remote"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("minimal body contains prohibited %q: %s", forbidden, body)
		}
	}
}

func TestRequestReceiptV1OuterShapeUnchanged(t *testing.T) {
	typeOf := reflect.TypeOf(provider.RequestReceipt{})
	want := []string{"Version", "Provider", "Model", "EffectiveEndpoint", "EndpointClassification", "ProxyMode", "ContextPolicy", "SelectorVersion", "SelectedFields", "RedactedFields", "OmittedFields", "RequestBody", "RequestBodyHash"}
	got := make([]string, typeOf.NumField())
	for index := range typeOf.NumField() {
		got[index] = typeOf.Field(index).Name
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request-receipt/v1 fields = %v, want %v", got, want)
	}
}

func providerPrivacyInventory(t *testing.T) (capability.Inventory, string) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "rg")
	body := "#!/bin/sh\nprintf 'ripgrep 14.1.0 SENTINEL-RAW-PROBE-OUTPUT\\n'\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("SHELL", "/bin/bash")
	return capability.Collect(t.Context(), ""), path
}

func receiptNames(fields []machinecontext.Field) []string {
	result := make([]string, len(fields))
	for index, field := range fields {
		result[index] = field.Name
	}
	return result
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
