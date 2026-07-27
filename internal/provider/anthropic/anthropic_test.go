package anthropic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/Vedaant-Rajoo/clai/internal/capability"
	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
)

// -----------------------------------------------------------------------------
// Test transport and fixtures
// -----------------------------------------------------------------------------

// fakeTransport is an injected base RoundTripper. It records the exact bytes and
// headers the SDK (after the receipt transport restores the body) hands to the
// network, and returns a canned response built by handler. No live request is
// ever made.
type fakeTransport struct {
	handler func(req *http.Request, body []byte) (*http.Response, error)

	calls      int
	urls       []string
	lastMethod string
	lastBody   []byte
	lastAPIKey string
	lastAuthz  string
	lastHeader http.Header
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.calls++
	f.urls = append(f.urls, req.URL.String())
	f.lastMethod = req.Method
	f.lastAPIKey = req.Header.Get("x-api-key")
	f.lastAuthz = req.Header.Get("authorization")
	f.lastHeader = req.Header.Clone()
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		f.lastBody = b
	}
	return f.handler(req, f.lastBody)
}

func sseResponse(req *http.Request, body io.ReadCloser) *http.Response {
	h := http.Header{}
	h.Set("Content-Type", "text/event-stream")
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK",
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: h, Body: body, Request: req, ContentLength: -1,
	}
}

func sseString(req *http.Request, body string) *http.Response {
	return sseResponse(req, io.NopCloser(strings.NewReader(body)))
}

func jsonErrorResponse(req *http.Request, status int, body string) *http.Response {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: req,
		ContentLength: int64(len(body)),
	}
}

// sseEvent formats one Server-Sent Event exactly as the Anthropic wire protocol
// does: an "event:" line, a "data:" line, then a blank line that the SDK's
// decoder uses to dispatch. Every event (including the last) is terminated by a
// blank line so the decoder never leaves the final event undispatched.
func sseEvent(typ, data string) string {
	return "event: " + typ + "\ndata: " + data + "\n\n"
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func messageStart() string {
	return sseEvent("message_start", `{"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":5,"output_tokens":1}}}`)
}

func textBlockStart(index int) string {
	return sseEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, index))
}

func thinkingBlockStart(index int) string {
	return sseEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"thinking","thinking":"","signature":""}}`, index))
}

func textDelta(index int, text string) string {
	return sseEvent("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%s}}`, index, jsonStr(text)))
}

func thinkingDelta(index int, text string) string {
	return sseEvent("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":%s}}`, index, jsonStr(text)))
}

func blockStop(index int) string {
	return sseEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, index))
}

func messageDelta(stopReason string) string {
	return sseEvent("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%s,"stop_sequence":null},"usage":{"output_tokens":12}}`, jsonStr(stopReason)))
}

func messageStop() string {
	return sseEvent("message_stop", `{"type":"message_stop"}`)
}

func errorEvent(errType, message string) string {
	return sseEvent("error", fmt.Sprintf(`{"type":"error","error":{"type":%s,"message":%s}}`, jsonStr(errType), jsonStr(message)))
}

// textStream builds a complete single-text-block stream from chunks, ending with
// the given stop reason.
func textStream(stopReason string, chunks ...string) string {
	var b strings.Builder
	b.WriteString(messageStart())
	b.WriteString(textBlockStart(0))
	for _, c := range chunks {
		b.WriteString(textDelta(0, c))
	}
	b.WriteString(blockStop(0))
	b.WriteString(messageDelta(stopReason))
	b.WriteString(messageStop())
	return b.String()
}

// stringTransport serves a fixed SSE body once.
func stringTransport(body string) *fakeTransport {
	return &fakeTransport{handler: func(req *http.Request, _ []byte) (*http.Response, error) {
		return sseString(req, body), nil
	}}
}

// scriptedBody delivers a fixed prefix across one or more reads, then invokes
// onDrain (used to cancel a context mid-stream) and returns drainErr on the next
// read. It lets a cancellation land deterministically after specific events.
type scriptedBody struct {
	prefix   []byte
	onDrain  func()
	drainErr error
	drained  bool
}

func (b *scriptedBody) Read(p []byte) (int, error) {
	if len(b.prefix) > 0 {
		n := copy(p, b.prefix)
		b.prefix = b.prefix[n:]
		return n, nil
	}
	if !b.drained {
		b.drained = true
		if b.onDrain != nil {
			b.onDrain()
		}
	}
	return 0, b.drainErr
}

func (b *scriptedBody) Close() error { return nil }

type cancelingBody struct {
	data   []byte
	cancel context.CancelFunc
	done   bool
}

func (b *cancelingBody) Read(p []byte) (int, error) {
	if b.done {
		return 0, io.EOF
	}
	b.done = true
	n := copy(p, b.data)
	b.cancel()
	return n, nil
}
func (b *cancelingBody) Close() error { return nil }

type failingReadCloser struct{ err error }

func (b failingReadCloser) Read([]byte) (int, error) { return 0, b.err }
func (failingReadCloser) Close() error               { return nil }

type closeErrorBody struct {
	io.Reader
	err error
}

func (b closeErrorBody) Close() error { return b.err }

const validCandidate = `{"command":"ls -la","explanation":"lists files in long format"}`

// remoteProvider returns a provider wired to a fake transport with the given
// receipt sink, defaulting to the pinned remote endpoint and remote-minimal
// policy so context sharing is exercised.
func remoteProvider(ft *fakeTransport, sink func(provider.RequestReceipt)) Provider {
	return Provider{
		APIKey:      "clai-secret-key",
		Policy:      machinecontext.PolicyRemoteMinimal,
		transport:   ft,
		receiptSink: sink,
	}
}

func remoteRequest() provider.Request {
	return provider.Request{
		Intent:  "list files",
		Context: machinecontext.Context{OS: "darwin", Shell: "/bin/fish", WorkingDirectory: "/tmp"},
	}
}

func decodeBody(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("request body is not JSON: %v\nbody: %s", err, b)
	}
	return m
}

// -----------------------------------------------------------------------------
// Authentication and request tests
// -----------------------------------------------------------------------------

func TestCompileRequiresAPIKey(t *testing.T) {
	ft := stringTransport(textStream("end_turn", validCandidate))
	p := Provider{Policy: machinecontext.PolicyRemoteMinimal, transport: ft}
	_, err := p.Compile(context.Background(), remoteRequest())
	if err == nil || !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("err = %v, want no-API-key error", err)
	}
	if ft.calls != 0 {
		t.Fatalf("transport ran %d times before API-key check", ft.calls)
	}
}

func TestExplicitKeyWinsOverAmbientEnvironment(t *testing.T) {
	// Ambient Anthropic environment variables must never override the clai key,
	// because the provider disables SDK environment defaults.
	t.Setenv("ANTHROPIC_API_KEY", "ambient-env-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "ambient-auth-token")
	t.Setenv("ANTHROPIC_BASE_URL", "https://ambient.invalid")
	t.Setenv("ANTHROPIC_PROFILE", "ambient-profile-must-not-load")
	t.Setenv("ANTHROPIC_FEDERATION_RULE_ID", "ambient-rule")
	t.Setenv("ANTHROPIC_ORGANIZATION_ID", "ambient-org")
	t.Setenv("ANTHROPIC_SERVICE_ACCOUNT_ID", "ambient-service-account")
	t.Setenv("ANTHROPIC_IDENTITY_TOKEN", "ambient-identity-token")

	ft := stringTransport(textStream("end_turn", validCandidate))
	var receipt provider.RequestReceipt
	p := remoteProvider(ft, func(r provider.RequestReceipt) { receipt = r })
	candidates, err := p.Compile(context.Background(), remoteRequest())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %+v, want exactly one", candidates)
	}
	if ft.lastAPIKey != "clai-secret-key" {
		t.Fatalf("x-api-key = %q, want the clai key (ambient env must not win)", ft.lastAPIKey)
	}
	for name, values := range ft.lastHeader {
		for _, v := range values {
			for _, forbidden := range []string{"ambient-env-key", "ambient-auth-token", "ambient-profile", "ambient-rule", "ambient-org", "ambient-service-account", "ambient-identity-token"} {
				if strings.Contains(v, forbidden) {
					t.Fatalf("ambient credential leaked into header %s: %q", name, v)
				}
			}
		}
	}
	if ft.lastURL0() != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("request URL = %q, want the pinned endpoint (ambient base URL must not win)", ft.lastURL0())
	}
	if bytes.Contains(receipt.RequestBody, []byte("ambient")) {
		t.Fatalf("ambient value leaked into receipt body: %s", receipt.RequestBody)
	}
}

func (f *fakeTransport) lastURL0() string {
	if len(f.urls) == 0 {
		return ""
	}
	return f.urls[0]
}

func TestOnlyClaiKeyReachesHeaderNotBodyOrReceipt(t *testing.T) {
	ft := stringTransport(textStream("end_turn", validCandidate))
	var receipt provider.RequestReceipt
	p := remoteProvider(ft, func(r provider.RequestReceipt) { receipt = r })
	if _, err := p.Compile(context.Background(), remoteRequest()); err != nil {
		t.Fatalf("compile: %v", err)
	}
	if ft.lastAPIKey != "clai-secret-key" {
		t.Fatalf("x-api-key = %q, want clai key", ft.lastAPIKey)
	}
	if ft.lastAuthz != "" {
		t.Fatalf("Authorization header = %q, want empty (key belongs only in x-api-key)", ft.lastAuthz)
	}
	if bytes.Contains(ft.lastBody, []byte("clai-secret-key")) {
		t.Fatalf("API key leaked into request body: %s", ft.lastBody)
	}
	if bytes.Contains(receipt.RequestBody, []byte("clai-secret-key")) || strings.Contains(receipt.RequestBodyHash.Value, "clai-secret-key") {
		t.Fatal("API key leaked into receipt body or hash")
	}
}

func TestDefaultModelIsBareSonnet(t *testing.T) {
	ft := stringTransport(textStream("end_turn", validCandidate))
	p := remoteProvider(ft, nil)
	if _, err := p.Compile(context.Background(), remoteRequest()); err != nil {
		t.Fatalf("compile: %v", err)
	}
	body := decodeBody(t, ft.lastBody)
	if body["model"] != DefaultModel {
		t.Fatalf("model = %v, want %q", body["model"], DefaultModel)
	}
	if body["model"] == "anthropic/claude-sonnet-5" {
		t.Fatal("model uses the OpenRouter slug form, want the bare direct id")
	}
}

func TestModelOverridePreserved(t *testing.T) {
	ft := stringTransport(textStream("end_turn", validCandidate))
	p := remoteProvider(ft, nil)
	p.Model = "claude-opus-4-8"
	if _, err := p.Compile(context.Background(), remoteRequest()); err != nil {
		t.Fatalf("compile: %v", err)
	}
	body := decodeBody(t, ft.lastBody)
	if body["model"] != "claude-opus-4-8" {
		t.Fatalf("model = %v, want claude-opus-4-8", body["model"])
	}
}

func TestRequestShapeStreamingBoundedStructured(t *testing.T) {
	ft := stringTransport(textStream("end_turn", validCandidate))
	p := remoteProvider(ft, nil)
	if _, err := p.Compile(context.Background(), remoteRequest()); err != nil {
		t.Fatalf("compile: %v", err)
	}
	body := decodeBody(t, ft.lastBody)
	if body["stream"] != true {
		t.Fatalf("stream = %v, want true", body["stream"])
	}
	if body["max_tokens"] != float64(maxTokens) {
		t.Fatalf("max_tokens = %v, want %d", body["max_tokens"], maxTokens)
	}
	oc, ok := body["output_config"].(map[string]any)
	if !ok {
		t.Fatalf("output_config = %T, want object", body["output_config"])
	}
	format, ok := oc["format"].(map[string]any)
	if !ok {
		t.Fatalf("output_config.format = %T, want object", oc["format"])
	}
	if format["type"] != "json_schema" {
		t.Fatalf("output_config.format.type = %v, want json_schema", format["type"])
	}
	schema, ok := format["schema"].(map[string]any)
	if !ok {
		t.Fatalf("output_config.format.schema = %T, want object", format["schema"])
	}
	if schema["type"] != "object" {
		t.Fatalf("schema type = %v, want object", schema["type"])
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("schema additionalProperties = %v, want false", schema["additionalProperties"])
	}
	required, ok := schema["required"].([]any)
	if !ok || len(required) != 2 || required[0] != "command" || required[1] != "explanation" {
		t.Fatalf("schema required = %v, want [command explanation]", schema["required"])
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %T, want object", schema["properties"])
	}
	for _, field := range []string{"command", "explanation"} {
		property, ok := properties[field].(map[string]any)
		if !ok || property["type"] != "string" {
			t.Fatalf("schema property %q = %v, want string property", field, properties[field])
		}
		if _, unsupported := property["minLength"]; unsupported {
			t.Fatalf("schema property %q uses unsupported minLength", field)
		}
	}
	if ft.lastMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", ft.lastMethod)
	}
}

func TestRemoteMinimalBodyContainsOnlyPermittedContext(t *testing.T) {
	ft := stringTransport(textStream("end_turn", validCandidate))
	p := remoteProvider(ft, nil)
	req := provider.Request{
		Intent: "list files",
		Context: machinecontext.Context{
			OS: "darwin", Shell: "/bin/fish",
			WorkingDirectory: "/SENTINEL/private/path",
			GitRepository:    true, GitRoot: "/SENTINEL/git/root", GitBranch: "SENTINEL-branch",
		},
	}
	if _, err := p.Compile(context.Background(), req); err != nil {
		t.Fatalf("compile: %v", err)
	}
	for _, want := range []string{"os_family", "shell_family", "project_kind", "platform_arch", "tools", "unknown"} {
		if !bytes.Contains(ft.lastBody, []byte(want)) {
			t.Errorf("remote-minimal body missing permitted %q: %s", want, ft.lastBody)
		}
	}
	for _, forbidden := range []string{"/SENTINEL", "SENTINEL-branch", "working_directory", "git_root", "git_branch"} {
		if bytes.Contains(ft.lastBody, []byte(forbidden)) {
			t.Errorf("remote-minimal body leaked forbidden %q: %s", forbidden, ft.lastBody)
		}
	}
}

func TestRemoteExplicitIncludesOnlyApprovedFields(t *testing.T) {
	ft := stringTransport(textStream("end_turn", validCandidate))
	var receipt provider.RequestReceipt
	p := Provider{
		APIKey:       "clai-secret-key",
		Policy:       machinecontext.PolicyRemoteExplicit,
		SharedFields: []string{machinecontext.FieldWorkingDirectory, machinecontext.FieldGitBranch},
		transport:    ft,
		receiptSink:  func(r provider.RequestReceipt) { receipt = r },
	}
	req := provider.Request{
		Intent: "status",
		Context: machinecontext.Context{
			OS: "linux", Shell: "zsh", WorkingDirectory: "/work",
			GitRepository: true, GitRoot: "/work",
		},
	}
	if _, err := p.Compile(context.Background(), req); err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !bytes.Contains(ft.lastBody, []byte("/work")) {
		t.Fatalf("explicit body omitted approved working directory: %s", ft.lastBody)
	}
	if bytes.Contains(ft.lastBody, []byte("git_root")) {
		t.Fatalf("explicit body included unapproved git root: %s", ft.lastBody)
	}
	if len(receipt.OmittedFields) != 2 || receipt.OmittedFields[0].Name != machinecontext.FieldGitRoot || receipt.OmittedFields[1].Name != machinecontext.FieldGitBranch {
		t.Fatalf("omitted fields = %+v", receipt.OmittedFields)
	}
}

// -----------------------------------------------------------------------------
// Streaming tests: every failure returns no usable candidate
// -----------------------------------------------------------------------------

func TestStreamSuccessCandidateSplitAcrossManyDeltas(t *testing.T) {
	// The candidate JSON is fragmented across many text deltas; only the fully
	// accumulated, decoded result becomes a candidate.
	chunks := []string{`{"comm`, `and":"ls`, ` -la","expl`, `anation":"lists `, `files in long format"}`}
	ft := stringTransport(textStream("end_turn", chunks...))
	candidates, err := remoteProvider(ft, nil).Compile(context.Background(), remoteRequest())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Command != "ls -la" || candidates[0].Explanation != "lists files in long format" {
		t.Fatalf("candidates = %+v, want one ls -la candidate", candidates)
	}
}

func TestStreamThinkingInterleavedBeforeText(t *testing.T) {
	var b strings.Builder
	b.WriteString(messageStart())
	b.WriteString(thinkingBlockStart(0))
	b.WriteString(thinkingDelta(0, "the user wants a listing; reason privately"))
	b.WriteString(blockStop(0))
	b.WriteString(textBlockStart(1))
	b.WriteString(textDelta(1, validCandidate))
	b.WriteString(blockStop(1))
	b.WriteString(messageDelta("end_turn"))
	b.WriteString(messageStop())

	ft := stringTransport(b.String())
	candidates, err := remoteProvider(ft, nil).Compile(context.Background(), remoteRequest())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Command != "ls -la" {
		t.Fatalf("candidates = %+v, want one candidate (thinking ignored)", candidates)
	}
	if strings.Contains(candidates[0].Command, "reason privately") || strings.Contains(candidates[0].Explanation, "reason privately") {
		t.Fatal("thinking text leaked into the candidate")
	}
}

func TestStreamErrorBeforeFirstDelta(t *testing.T) {
	body := messageStart() + textBlockStart(0) + errorEvent("overloaded_error", "SENTINEL overloaded")
	ft := stringTransport(body)
	candidates, err := remoteProvider(ft, nil).Compile(context.Background(), remoteRequest())
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none", candidates)
	}
	if !errors.Is(err, ErrIncompleteResponse) {
		t.Fatalf("err = %v, want ErrIncompleteResponse", err)
	}
	if strings.Contains(err.Error(), "SENTINEL") {
		t.Fatalf("upstream error text leaked: %v", err)
	}
}

func TestStreamErrorAfterPartialJSON(t *testing.T) {
	body := messageStart() + textBlockStart(0) + textDelta(0, `{"command":"ls",`) + errorEvent("api_error", "SENTINEL boom")
	ft := stringTransport(body)
	candidates, err := remoteProvider(ft, nil).Compile(context.Background(), remoteRequest())
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none (partial JSON must never surface)", candidates)
	}
	if !errors.Is(err, ErrIncompleteResponse) {
		t.Fatalf("err = %v, want ErrIncompleteResponse", err)
	}
	if strings.Contains(err.Error(), "SENTINEL") || strings.Contains(err.Error(), "ls") {
		t.Fatalf("partial or upstream text leaked: %v", err)
	}
}

func TestStreamCancellationBeforeCompile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ft := stringTransport(textStream("end_turn", validCandidate))
	candidates, err := remoteProvider(ft, nil).Compile(ctx, remoteRequest())
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none", candidates)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if ft.calls != 0 {
		t.Fatalf("transport ran %d times after pre-cancelled context", ft.calls)
	}
}

func TestStreamCancellationBeforeFirstEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ft := &fakeTransport{handler: func(req *http.Request, _ []byte) (*http.Response, error) {
		return sseResponse(req, &scriptedBody{onDrain: cancel, drainErr: context.Canceled}), nil
	}}
	candidates, err := remoteProvider(ft, nil).Compile(ctx, remoteRequest())
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none", candidates)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestStreamCancellationAfterPartialJSON(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	prefix := messageStart() + textBlockStart(0) + textDelta(0, `{"command":"l`)
	ft := &fakeTransport{handler: func(req *http.Request, _ []byte) (*http.Response, error) {
		return sseResponse(req, &scriptedBody{prefix: []byte(prefix), onDrain: cancel, drainErr: context.Canceled}), nil
	}}
	candidates, err := remoteProvider(ft, nil).Compile(ctx, remoteRequest())
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none (partial JSON must never surface)", candidates)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestStreamConnectionCloseBeforeTerminalCompletion(t *testing.T) {
	// The stream ends cleanly (EOF) after a content block stop but before any
	// message_delta carries a stop reason: the empty stop reason fails closed.
	body := messageStart() + textBlockStart(0) + textDelta(0, validCandidate) + blockStop(0)
	ft := stringTransport(body)
	candidates, err := remoteProvider(ft, nil).Compile(context.Background(), remoteRequest())
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none", candidates)
	}
	if !errors.Is(err, ErrIncompleteResponse) {
		t.Fatalf("err = %v, want ErrIncompleteResponse", err)
	}
}

func TestStreamCleanEOFAfterEndTurnBeforeMessageStop(t *testing.T) {
	body := messageStart() + textBlockStart(0) + textDelta(0, validCandidate) + blockStop(0) + messageDelta("end_turn")
	ft := stringTransport(body)
	candidates, err := remoteProvider(ft, nil).Compile(context.Background(), remoteRequest())
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none without message_stop", candidates)
	}
	if !errors.Is(err, ErrIncompleteResponse) {
		t.Fatalf("err = %v, want ErrIncompleteResponse", err)
	}
}

func TestStreamRejectsIncompleteEventLifecycle(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "missing message_start",
			body: textBlockStart(0) + textDelta(0, validCandidate) + blockStop(0) + messageDelta("end_turn") + messageStop(),
		},
		{
			name: "missing content_block_stop",
			body: messageStart() + textBlockStart(0) + textDelta(0, validCandidate) + messageDelta("end_turn") + messageStop(),
		},
		{
			name: "message_stop before message_delta",
			body: messageStart() + messageStop(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidates, err := remoteProvider(stringTransport(tt.body), nil).Compile(context.Background(), remoteRequest())
			if candidates != nil {
				t.Fatalf("candidates = %+v, want none", candidates)
			}
			if !errors.Is(err, ErrIncompleteResponse) {
				t.Fatalf("err = %v, want ErrIncompleteResponse", err)
			}
		})
	}
}

func TestCancellationPrecedesMalformedLifecycleError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	body := textBlockStart(0)
	ft := &fakeTransport{handler: func(req *http.Request, _ []byte) (*http.Response, error) {
		return sseResponse(req, &cancelingBody{data: []byte(body), cancel: cancel}), nil
	}}
	candidates, err := remoteProvider(ft, nil).Compile(ctx, remoteRequest())
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none", candidates)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestStreamMaxTokensAfterPartialContent(t *testing.T) {
	ft := stringTransport(textStream("max_tokens", `{"command":"ls -la",`))
	candidates, err := remoteProvider(ft, nil).Compile(context.Background(), remoteRequest())
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none", candidates)
	}
	if !errors.Is(err, ErrIncompleteResponse) {
		t.Fatalf("err = %v, want ErrIncompleteResponse", err)
	}
}

func TestStreamRefusal(t *testing.T) {
	ft := stringTransport(textStream("refusal", validCandidate))
	candidates, err := remoteProvider(ft, nil).Compile(context.Background(), remoteRequest())
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none", candidates)
	}
	if !errors.Is(err, ErrRefusal) {
		t.Fatalf("err = %v, want ErrRefusal", err)
	}
}

func TestStreamMissingText(t *testing.T) {
	// A thinking-only message with a normal stop reason has no text payload.
	body := messageStart() + thinkingBlockStart(0) + thinkingDelta(0, "hmm") + blockStop(0) + messageDelta("end_turn") + messageStop()
	ft := stringTransport(body)
	candidates, err := remoteProvider(ft, nil).Compile(context.Background(), remoteRequest())
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none", candidates)
	}
	if !errors.Is(err, ErrIncompleteResponse) {
		t.Fatalf("err = %v, want ErrIncompleteResponse", err)
	}
}

func TestStreamMultipleAmbiguousTextPayloads(t *testing.T) {
	var b strings.Builder
	b.WriteString(messageStart())
	b.WriteString(textBlockStart(0))
	b.WriteString(textDelta(0, validCandidate))
	b.WriteString(blockStop(0))
	b.WriteString(textBlockStart(1))
	b.WriteString(textDelta(1, `{"command":"rm -rf /","explanation":"second, ambiguous"}`))
	b.WriteString(blockStop(1))
	b.WriteString(messageDelta("end_turn"))
	b.WriteString(messageStop())

	ft := stringTransport(b.String())
	candidates, err := remoteProvider(ft, nil).Compile(context.Background(), remoteRequest())
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none (ambiguous)", candidates)
	}
	if !errors.Is(err, ErrIncompleteResponse) {
		t.Fatalf("err = %v, want ErrIncompleteResponse", err)
	}
}

func TestStreamMalformedAndTrailingCandidateJSON(t *testing.T) {
	for _, tt := range []struct {
		name string
		text string
	}{
		{"not json", "totally not json"},
		{"trailing json", validCandidate + `{}`},
		{"unknown field", `{"command":"ls","explanation":"x","danger":true}`},
		{"missing explanation", `{"command":"ls"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ft := stringTransport(textStream("end_turn", tt.text))
			candidates, err := remoteProvider(ft, nil).Compile(context.Background(), remoteRequest())
			if candidates != nil {
				t.Fatalf("candidates = %+v, want none", candidates)
			}
			if !errors.Is(err, ErrIncompleteResponse) {
				t.Fatalf("err = %v, want ErrIncompleteResponse", err)
			}
			if strings.Contains(err.Error(), "rm -rf") || strings.Contains(err.Error(), "danger") {
				t.Fatalf("model-controlled payload leaked into error: %v", err)
			}
		})
	}
}

func TestStreamEmptyCommand(t *testing.T) {
	ft := stringTransport(textStream("end_turn", `{"command":"","explanation":"nonempty"}`))
	candidates, err := remoteProvider(ft, nil).Compile(context.Background(), remoteRequest())
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none (empty command)", candidates)
	}
	if !errors.Is(err, ErrIncompleteResponse) {
		t.Fatalf("err = %v, want ErrIncompleteResponse", err)
	}
}

func TestStreamOutputExceedsBound(t *testing.T) {
	huge := strings.Repeat("x", maxCandidateBytes+1)
	ft := stringTransport(textStream("end_turn", huge))
	candidates, err := remoteProvider(ft, nil).Compile(context.Background(), remoteRequest())
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none (over output bound)", candidates)
	}
	if !errors.Is(err, ErrIncompleteResponse) {
		t.Fatalf("err = %v, want ErrIncompleteResponse", err)
	}
}

func TestStreamMalformedEventInvalidatesAccumulation(t *testing.T) {
	// A content_block_start whose index skips ahead violates the accumulator's
	// index contract and must invalidate the whole stream.
	body := messageStart() + textBlockStart(5) + messageStop()
	ft := stringTransport(body)
	candidates, err := remoteProvider(ft, nil).Compile(context.Background(), remoteRequest())
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none", candidates)
	}
	if !errors.Is(err, ErrIncompleteResponse) {
		t.Fatalf("err = %v, want ErrIncompleteResponse", err)
	}
}

// -----------------------------------------------------------------------------
// HTTP status classification (non-2xx before the stream begins)
// -----------------------------------------------------------------------------

func TestCompileClassifiesHTTPErrors(t *testing.T) {
	const apiKey = "super-secret-key"
	errorEnvelope := `{"type":"error","error":{"type":"api_error","message":"SENTINEL upstream detail"}}`
	tests := []struct {
		name     string
		status   int
		sentinel error
	}{
		{"unauthorized", http.StatusUnauthorized, ErrAuth},
		{"forbidden", http.StatusForbidden, ErrAuth},
		{"bad-request", http.StatusBadRequest, ErrInvalidRequest},
		{"not-found", http.StatusNotFound, ErrInvalidRequest},
		{"unprocessable", http.StatusUnprocessableEntity, ErrInvalidRequest},
		{"rate-limited", http.StatusTooManyRequests, ErrRateLimited},
		{"internal", http.StatusInternalServerError, ErrServer},
		{"unavailable", http.StatusServiceUnavailable, ErrServer},
		{"overloaded", 529, ErrServer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := &fakeTransport{handler: func(req *http.Request, _ []byte) (*http.Response, error) {
				return jsonErrorResponse(req, tt.status, errorEnvelope), nil
			}}
			p := Provider{APIKey: apiKey, Policy: machinecontext.PolicyRemoteMinimal, transport: ft}
			candidates, err := p.Compile(context.Background(), remoteRequest())
			if candidates != nil {
				t.Fatalf("candidates = %+v, want none", candidates)
			}
			if !errors.Is(err, tt.sentinel) {
				t.Fatalf("status %d: err = %v, want errors.Is %v", tt.status, err, tt.sentinel)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", tt.status)) {
				t.Errorf("status %d: err = %q, want it to name the status", tt.status, err)
			}
			if strings.Contains(err.Error(), apiKey) {
				t.Fatalf("status %d: API key leaked into error: %v", tt.status, err)
			}
			if strings.Contains(err.Error(), "SENTINEL") {
				t.Fatalf("status %d: upstream body leaked into error: %v", tt.status, err)
			}
			if ft.calls != 1 {
				t.Fatalf("status %d: transport ran %d times, want exactly one (no duplicate retries)", tt.status, ft.calls)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Receipt and transport tests
// -----------------------------------------------------------------------------

func TestReceiptBytesEqualServerObservedBytesAndHash(t *testing.T) {
	ft := stringTransport(textStream("end_turn", validCandidate))
	var receipt provider.RequestReceipt
	p := remoteProvider(ft, func(r provider.RequestReceipt) { receipt = r })
	if _, err := p.Compile(context.Background(), remoteRequest()); err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !bytes.Equal(ft.lastBody, receipt.RequestBody) {
		t.Fatalf("server-observed bytes differ from receipt\nserver: %s\nreceipt: %s", ft.lastBody, receipt.RequestBody)
	}
	sum := sha256.Sum256(ft.lastBody)
	if receipt.RequestBodyHash.Algorithm != "sha256" || receipt.RequestBodyHash.Value != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash = %+v, want sha256 of observed bytes", receipt.RequestBodyHash)
	}
	if receipt.Version != "request-receipt/v1" || receipt.Provider != "anthropic" || receipt.Model != DefaultModel {
		t.Fatalf("receipt metadata = %+v", receipt)
	}
	if receipt.EndpointClassification != machinecontext.EndpointRemote || receipt.EffectiveEndpoint != defaultEndpoint+"/v1/messages" {
		t.Fatalf("receipt endpoint = %q/%q", receipt.EffectiveEndpoint, receipt.EndpointClassification)
	}
}

func TestReceiptDeterministicForFixedInput(t *testing.T) {
	var bodies [][]byte
	for i := 0; i < 2; i++ {
		ft := stringTransport(textStream("end_turn", validCandidate))
		var receipt provider.RequestReceipt
		p := remoteProvider(ft, func(r provider.RequestReceipt) { receipt = r })
		if _, err := p.Compile(context.Background(), remoteRequest()); err != nil {
			t.Fatalf("compile %d: %v", i, err)
		}
		if !bytes.Equal(ft.lastBody, receipt.RequestBody) {
			t.Fatalf("run %d: server bytes != receipt bytes", i)
		}
		bodies = append(bodies, receipt.RequestBody)
	}
	if !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("request body changed across identical inputs\nfirst:  %s\nsecond: %s", bodies[0], bodies[1])
	}
}

func TestReceiptBytesEqualHTTPServerObservedBytes(t *testing.T) {
	var observed []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		observed, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read server body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, textStream("end_turn", validCandidate))
	}))
	defer server.Close()

	var receipt provider.RequestReceipt
	p := Provider{
		APIKey: "test", baseURL: server.URL,
		Policy:      machinecontext.PolicyRemoteMinimal,
		receiptSink: func(r provider.RequestReceipt) { receipt = r },
	}
	if _, err := p.Compile(context.Background(), remoteRequest()); err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !bytes.Equal(observed, receipt.RequestBody) {
		t.Fatalf("HTTP server bytes differ from receipt\nserver: %s\nreceipt: %s", observed, receipt.RequestBody)
	}
	if receipt.EffectiveEndpoint != server.URL+"/v1/messages" {
		t.Fatalf("effective endpoint = %q, want final request URL", receipt.EffectiveEndpoint)
	}
}

// goldenInventory is a deterministic capability fixture so context bytes and
// receipt membership are exact regardless of the host machine.
func goldenInventory() capability.Inventory {
	tools := make([]capability.ToolFact, 0, len(capability.ToolNames()))
	for _, name := range capability.ToolNames() {
		fact := capability.ToolFact{Name: name}
		if name == "rg" {
			fact = capability.ToolFact{Name: "rg", Present: true, Path: "/fixture/bin/rg", Version: "14.1.0"}
		}
		tools = append(tools, fact)
	}
	shell := capability.ShellIdentity{Family: capability.ShellBash, Path: "/bin/bash", Provenance: capability.ShellFromEnv}
	return capability.NewFixtureInventory("linux", "arm64", shell, tools)
}

func goldenToolsJSON() string {
	var b strings.Builder
	b.WriteByte('[')
	for index, name := range capability.ToolNames() {
		if index > 0 {
			b.WriteByte(',')
		}
		status := "absent"
		if name == "rg" {
			status = "present:14.1.0"
		}
		b.WriteString(`{"name":"` + name + `","status":"` + status + `"}`)
	}
	b.WriteByte(']')
	return b.String()
}

func receiptFieldNames(fields []machinecontext.Field) []string {
	names := make([]string, 0, len(fields))
	for _, field := range fields {
		names = append(names, field.Name)
	}
	return names
}

func TestAnthropicContextV2ReceiptMembershipAllPolicies(t *testing.T) {
	requestContext := machinecontext.Context{
		WorkingDirectory: "/work",
		GitRepository:    true,
		GitRoot:          "/work",
		GitBranch:        "dev",
	}
	inventory := goldenInventory()
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
			selection, err := machinecontext.Select(requestContext, inventory, tt.policy, tt.shared)
			if err != nil {
				t.Fatal(err)
			}
			body, err := buildUserContent("intent", selection)
			if err != nil {
				t.Fatal(err)
			}
			receipt := makeReceipt(DefaultModel, defaultEndpoint, machinecontext.EndpointRemote, "direct", selection, body)
			wantHash := sha256.Sum256(body)
			if receipt.RequestBodyHash.Algorithm != "sha256" || receipt.RequestBodyHash.Value != hex.EncodeToString(wantHash[:]) || !bytes.Equal(receipt.RequestBody, body) {
				t.Fatalf("receipt body/hash mismatch: %+v", receipt.RequestBodyHash)
			}
			secondBody, err := buildUserContent("intent", selection)
			if err != nil || !bytes.Equal(secondBody, body) {
				t.Fatalf("request bytes are not deterministic: %v", err)
			}
			if got := receiptFieldNames(receipt.SelectedFields); !reflect.DeepEqual(got, tt.wantSelected) {
				t.Fatalf("selected = %v, want %v", got, tt.wantSelected)
			}
			if len(receipt.RedactedFields) != 0 {
				t.Fatalf("redacted = %+v, want empty", receipt.RedactedFields)
			}
			if got := receiptFieldNames(receipt.OmittedFields); !reflect.DeepEqual(got, tt.wantOmitted) {
				t.Fatalf("omitted = %v, want %v", got, tt.wantOmitted)
			}
		})
	}
}

func TestAnthropicContextWireGoldenAllPolicies(t *testing.T) {
	requestContext := machinecontext.Context{
		WorkingDirectory: "/work",
		GitRepository:    true,
		GitRoot:          "/work",
		GitBranch:        "dev",
	}
	contextJSON := `{"os_family":"linux","shell_family":"bash","project_kind":"git","platform_arch":"arm64","tools":` + goldenToolsJSON()
	tests := []struct {
		name    string
		baseURL string
		policy  machinecontext.Policy
		shared  []string
		want    string
	}{
		{"local-only", "http://localhost:9999", machinecontext.PolicyLocalOnly, nil, `{"intent":"golden intent"}`},
		{"remote-minimal", "", machinecontext.PolicyRemoteMinimal, nil, `{"intent":"golden intent","context":` + contextJSON + `}}`},
		{"remote-explicit", "", machinecontext.PolicyRemoteExplicit, []string{machinecontext.FieldWorkingDirectory, machinecontext.FieldGitBranch}, `{"intent":"golden intent","context":` + contextJSON + `,"working_directory":"/work","git_branch":"dev"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := stringTransport(textStream("end_turn", validCandidate))
			p := Provider{
				APIKey:       "test",
				Policy:       tt.policy,
				SharedFields: tt.shared,
				baseURL:      tt.baseURL,
				transport:    ft,
			}
			req := provider.Request{Intent: "golden intent", Context: requestContext, Capabilities: goldenInventory()}
			if _, err := p.Compile(context.Background(), req); err != nil {
				t.Fatalf("compile: %v", err)
			}
			var envelope struct {
				Messages []struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(ft.lastBody, &envelope); err != nil {
				t.Fatalf("decode request envelope: %v", err)
			}
			if len(envelope.Messages) != 1 || len(envelope.Messages[0].Content) != 1 {
				t.Fatalf("envelope shape = %s", ft.lastBody)
			}
			if got := envelope.Messages[0].Content[0].Text; got != tt.want {
				t.Fatalf("context payload = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestReceiptCaptureFailuresAreBoundedAndFailClosed(t *testing.T) {
	readErr := errors.New("read failed")
	closeErr := errors.New("close failed")
	tests := []struct {
		name string
		body io.ReadCloser
	}{
		{"over request bound", io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), maxRequestBytes+1)))},
		{"body read failure", failingReadCloser{err: readErr}},
		{"body close failure", closeErrorBody{Reader: strings.NewReader("body"), err: closeErr}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			receipts := 0
			base := &fakeTransport{handler: func(req *http.Request, body []byte) (*http.Response, error) {
				return nil, errors.New("base transport must not run")
			}}
			rt := &receiptTransport{
				base: base, model: DefaultModel, class: machinecontext.EndpointRemote,
				selection: machinecontext.Selection{Policy: machinecontext.PolicyRemoteMinimal},
				sink:      func(provider.RequestReceipt) { receipts++ },
			}
			req, err := http.NewRequest(http.MethodPost, defaultEndpoint+"/v1/messages", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Body = tt.body
			if _, err := rt.RoundTrip(req); err == nil {
				t.Fatal("RoundTrip unexpectedly succeeded")
			}
			if base.calls != 0 || receipts != 0 {
				t.Fatalf("base calls=%d receipts=%d, want both zero", base.calls, receipts)
			}
		})
	}
}

func TestProductionDefaultReceiptSinkEmitsOnce(t *testing.T) {
	original := defaultReceiptSink
	receipts := 0
	defaultReceiptSink = func(provider.RequestReceipt) { receipts++ }
	t.Cleanup(func() { defaultReceiptSink = original })

	ft := stringTransport(textStream("end_turn", validCandidate))
	p := Provider{APIKey: "test", Policy: machinecontext.PolicyRemoteMinimal, transport: ft}
	if _, err := p.Compile(context.Background(), remoteRequest()); err != nil {
		t.Fatalf("compile: %v", err)
	}
	if receipts != 1 {
		t.Fatalf("default receipt emissions = %d, want exactly one", receipts)
	}
}

func TestRedirectTargetNeverContacted(t *testing.T) {
	ft := &fakeTransport{handler: func(req *http.Request, _ []byte) (*http.Response, error) {
		h := http.Header{}
		h.Set("Location", "https://example.invalid/remote-target")
		return &http.Response{
			StatusCode: http.StatusFound, Status: "302 Found",
			Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: h, Body: io.NopCloser(strings.NewReader("SENTINEL redirect body")),
			Request: req, ContentLength: -1,
		}, nil
	}}
	var receipt provider.RequestReceipt
	p := remoteProvider(ft, func(r provider.RequestReceipt) { receipt = r })
	candidates, err := p.Compile(context.Background(), remoteRequest())
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none", candidates)
	}
	if err == nil {
		t.Fatal("err = nil, want a fail-closed error for a refused redirect")
	}
	if strings.Contains(err.Error(), "SENTINEL") {
		t.Fatalf("redirect body leaked into error: %v", err)
	}
	if ft.calls != 1 {
		t.Fatalf("transport ran %d times, want exactly one (redirect not followed)", ft.calls)
	}
	for _, u := range ft.urls {
		if strings.Contains(u, "example.invalid") {
			t.Fatalf("redirect target was contacted: %q", u)
		}
	}
	if receipt.EffectiveEndpoint != defaultEndpoint+"/v1/messages" {
		t.Fatalf("receipt endpoint changed across refused redirect: %q", receipt.EffectiveEndpoint)
	}
}

func TestUserinfoEndpointRejectedBeforeNetworkOrReceipt(t *testing.T) {
	for _, endpoint := range []string{
		"https://user@api.anthropic.com",
		"https://user:pass@api.anthropic.com",
	} {
		t.Run(endpoint, func(t *testing.T) {
			var receipted bool
			ft := &fakeTransport{handler: func(req *http.Request, _ []byte) (*http.Response, error) {
				return nil, errors.New("must not transport")
			}}
			p := Provider{
				APIKey: "clai-secret-key", baseURL: endpoint,
				Policy: machinecontext.PolicyRemoteMinimal, transport: ft,
				receiptSink: func(provider.RequestReceipt) { receipted = true },
			}
			_, err := p.Compile(context.Background(), remoteRequest())
			if err == nil || !strings.Contains(err.Error(), "must not contain userinfo") {
				t.Fatalf("err = %v, want userinfo rejection", err)
			}
			if ft.calls != 0 || receipted {
				t.Fatalf("userinfo endpoint reached transport (%d) or receipt (%v)", ft.calls, receipted)
			}
		})
	}
}

func TestLocalOnlyRemoteFailsBeforeNetwork(t *testing.T) {
	ft := &fakeTransport{handler: func(req *http.Request, _ []byte) (*http.Response, error) {
		return nil, errors.New("must not transport")
	}}
	p := Provider{APIKey: "clai-secret-key", Policy: machinecontext.PolicyLocalOnly, transport: ft}
	_, err := p.Compile(context.Background(), remoteRequest())
	if err == nil || !strings.Contains(err.Error(), "local-only") {
		t.Fatalf("err = %v, want local-only refusal", err)
	}
	if ft.calls != 0 {
		t.Fatalf("transport ran %d times before local-only refusal", ft.calls)
	}
}

func TestProxyModeClassificationExcludesSecret(t *testing.T) {
	// Non-*http.Transport base: unknown.
	if got := classifyProxyMode(&fakeTransport{}, defaultEndpoint); got != "unknown" {
		t.Fatalf("fake transport proxy mode = %q, want unknown", got)
	}
	// Direct *http.Transport with no proxy.
	if got := classifyProxyMode(&http.Transport{}, defaultEndpoint); got != "direct" {
		t.Fatalf("no-proxy transport proxy mode = %q, want direct", got)
	}
	// Configured proxy carrying a secret: classification is configured-proxy and
	// the secret never appears in the receipt.
	const proxySecret = "proxy-user:proxy-password"
	transport := &http.Transport{Proxy: func(*http.Request) (*url.URL, error) {
		return url.Parse("http://" + proxySecret + "@proxy.example:8080")
	}}
	if got := classifyProxyMode(transport, defaultEndpoint); got != "configured-proxy" {
		t.Fatalf("proxy mode = %q, want configured-proxy", got)
	}
	selection, _ := machinecontext.Select(machinecontext.Context{}, capability.Inventory{}, machinecontext.PolicyRemoteMinimal, nil)
	receipt := makeReceipt("m", defaultEndpoint, machinecontext.EndpointRemote, "configured-proxy", selection, []byte(`{"safe":true}`))
	if strings.Contains(string(receipt.RequestBody), proxySecret) || strings.Contains(receipt.EffectiveEndpoint, proxySecret) {
		t.Fatal("proxy secret leaked into receipt")
	}
}

// -----------------------------------------------------------------------------
// Cancellation and timeout over a real loopback transport
// -----------------------------------------------------------------------------

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
		p := Provider{APIKey: "test", baseURL: server.URL, Policy: machinecontext.PolicyRemoteMinimal}
		_, err := p.Compile(ctx, remoteRequest())
		result <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation not observed")
	}
}

func TestCompileHonorsCancellationWhileReading(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		<-release
	}))
	defer server.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		p := Provider{APIKey: "test", baseURL: server.URL, Policy: machinecontext.PolicyRemoteMinimal}
		_, err := p.Compile(ctx, remoteRequest())
		result <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("response read did not start")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation not observed while reading")
	}
}

func TestDefaultTimeoutIsNinetySeconds(t *testing.T) {
	if defaultTimeout != 90*time.Second {
		t.Fatalf("defaultTimeout = %s, want 90s", defaultTimeout)
	}
}

func TestCompileTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	p := Provider{APIKey: "test", baseURL: server.URL, Policy: machinecontext.PolicyRemoteMinimal, timeout: 30 * time.Millisecond}
	_, err := p.Compile(context.Background(), remoteRequest())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

// -----------------------------------------------------------------------------
// Pure classifier unit tests
// -----------------------------------------------------------------------------

func TestClassifyStreamErrorStatuses(t *testing.T) {
	tests := []struct {
		status   int
		sentinel error
	}{
		{http.StatusUnauthorized, ErrAuth},
		{http.StatusForbidden, ErrAuth},
		{http.StatusBadRequest, ErrInvalidRequest},
		{http.StatusNotFound, ErrInvalidRequest},
		{http.StatusUnprocessableEntity, ErrInvalidRequest},
		{http.StatusTooManyRequests, ErrRateLimited},
		{http.StatusInternalServerError, ErrServer},
		{http.StatusBadGateway, ErrServer},
		{529, ErrServer},
		{http.StatusOK, ErrIncompleteResponse}, // mid-stream error event
	}
	for _, tt := range tests {
		err := classifyStreamError(&sdk.Error{StatusCode: tt.status})
		if !errors.Is(err, tt.sentinel) {
			t.Errorf("status %d: err = %v, want errors.Is %v", tt.status, err, tt.sentinel)
		}
	}
	// A non-API transport error never echoes its text and maps to incomplete.
	err := classifyStreamError(errors.New("SENTINEL raw transport text"))
	if !errors.Is(err, ErrIncompleteResponse) {
		t.Errorf("non-API err = %v, want ErrIncompleteResponse", err)
	}
	if strings.Contains(err.Error(), "SENTINEL") {
		t.Errorf("raw transport text leaked: %v", err)
	}
}

func TestClassifyStopReason(t *testing.T) {
	tests := []struct {
		reason  sdk.StopReason
		wantErr error
	}{
		{sdk.StopReasonEndTurn, nil},
		{sdk.StopReasonMaxTokens, ErrIncompleteResponse},
		{sdk.StopReasonRefusal, ErrRefusal},
		{sdk.StopReasonToolUse, ErrIncompleteResponse},
		{sdk.StopReasonPauseTurn, ErrIncompleteResponse},
		{sdk.StopReasonStopSequence, ErrIncompleteResponse},
		{sdk.StopReasonModelContextWindowExceeded, ErrIncompleteResponse},
		{sdk.StopReason("something_new"), ErrIncompleteResponse},
	}
	for _, tt := range tests {
		err := classifyStopReason(tt.reason)
		if tt.wantErr == nil {
			if err != nil {
				t.Errorf("stop %q: err = %v, want nil", tt.reason, err)
			}
			continue
		}
		if !errors.Is(err, tt.wantErr) {
			t.Errorf("stop %q: err = %v, want errors.Is %v", tt.reason, err, tt.wantErr)
		}
	}
}

func TestSingleTextPayload(t *testing.T) {
	textBlock := func(s string) sdk.ContentBlockUnion { return sdk.ContentBlockUnion{Type: "text", Text: s} }
	thinkingBlock := sdk.ContentBlockUnion{Type: "thinking", Thinking: "private"}

	got, err := singleTextPayload(sdk.Message{Content: []sdk.ContentBlockUnion{thinkingBlock, textBlock("payload")}})
	if err != nil || got != "payload" {
		t.Fatalf("thinking+text: got %q, err %v; want payload", got, err)
	}
	if _, err := singleTextPayload(sdk.Message{Content: []sdk.ContentBlockUnion{thinkingBlock}}); !errors.Is(err, ErrIncompleteResponse) {
		t.Errorf("thinking-only: err = %v, want ErrIncompleteResponse", err)
	}
	if _, err := singleTextPayload(sdk.Message{Content: []sdk.ContentBlockUnion{textBlock("a"), textBlock("b")}}); !errors.Is(err, ErrIncompleteResponse) {
		t.Errorf("two-text: err = %v, want ErrIncompleteResponse", err)
	}
	if _, err := singleTextPayload(sdk.Message{Content: []sdk.ContentBlockUnion{{Type: "tool_use"}}}); !errors.Is(err, ErrIncompleteResponse) {
		t.Errorf("tool_use block: err = %v, want ErrIncompleteResponse", err)
	}
	if _, err := singleTextPayload(sdk.Message{Content: []sdk.ContentBlockUnion{textBlock(strings.Repeat("x", maxCandidateBytes+1))}}); !errors.Is(err, ErrIncompleteResponse) {
		t.Errorf("oversize text: err = %v, want ErrIncompleteResponse", err)
	}
}

// -----------------------------------------------------------------------------
// Acceptance-manifest anchors (acceptance-manifest/v1)
//
// Each test below carries the exact name referenced by an automated acceptance
// case in .local/acceptance-manifest.yaml. They compose the focused tests above
// (so there is one source of truth per assertion) and add the few requirement
// proofs unique to the acceptance layer. Renaming any of these breaks the
// manifest's `go test -run '^Name$'` binding.
// -----------------------------------------------------------------------------

// TestAnthropicUsesOfficialSDK anchors AC-ANTHROPIC-SDK (REQ-ANTHROPIC-001): the
// request is produced by the official Stainless-generated anthropic-sdk-go at the
// pinned version — not a raw transport, OpenAI shim, or hand-rolled envelope.
func TestAnthropicUsesOfficialSDK(t *testing.T) {
	ft := stringTransport(textStream("end_turn", validCandidate))
	if _, err := remoteProvider(ft, nil).Compile(context.Background(), remoteRequest()); err != nil {
		t.Fatalf("compile: %v", err)
	}
	if ft.lastURL0() != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("URL = %q, want the SDK Messages path", ft.lastURL0())
	}
	if ft.lastMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", ft.lastMethod)
	}
	if got := ft.lastHeader.Get("Anthropic-Version"); got != "2023-06-01" {
		t.Errorf("Anthropic-Version = %q, want the SDK-pinned API version", got)
	}
	if got := ft.lastHeader.Get("User-Agent"); !strings.Contains(got, "Anthropic/Go") {
		t.Errorf("User-Agent = %q, want the official Go SDK agent", got)
	}
	if got := ft.lastHeader.Get("X-Stainless-Lang"); got != "go" {
		t.Errorf("X-Stainless-Lang = %q, want go (official generated SDK marker)", got)
	}
	if got := ft.lastHeader.Get("X-Stainless-Package-Version"); got != "1.60.0" {
		t.Errorf("X-Stainless-Package-Version = %q, want the pinned SDK version", got)
	}
	if ft.lastHeader.Get("Authorization") != "" {
		t.Error("Authorization header present; the official SDK authenticates via x-api-key")
	}
}

// TestAnthropicAuthResolutionAndAmbientDisabled anchors AC-ANTHROPIC-AUTH
// (REQ-ANTHROPIC-002): the clai-resolved key is the only credential, ambient SDK
// resolution cannot override it, and the key never leaves the header.
func TestAnthropicAuthResolutionAndAmbientDisabled(t *testing.T) {
	t.Run("ExplicitKeyWinsOverAmbientEnvironment", TestExplicitKeyWinsOverAmbientEnvironment)
	t.Run("KeyOnlyInHeaderNotBodyOrReceipt", TestOnlyClaiKeyReachesHeaderNotBodyOrReceipt)
	t.Run("MissingKeyFailsClosed", TestCompileRequiresAPIKey)
}

// TestAnthropicModelDefaultAndOverride anchors AC-ANTHROPIC-MODEL
// (REQ-ANTHROPIC-003).
func TestAnthropicModelDefaultAndOverride(t *testing.T) {
	t.Run("DefaultBareSonnet", TestDefaultModelIsBareSonnet)
	t.Run("OverridePreservedVerbatim", TestModelOverridePreserved)
}

// TestAnthropicContextDefaultAndEndpointClassification anchors
// AC-ANTHROPIC-CONTEXT-ENDPOINT (REQ-ANTHROPIC-004).
func TestAnthropicContextDefaultAndEndpointClassification(t *testing.T) {
	t.Run("DefaultsToRemoteMinimalAgainstPinnedRemoteEndpoint", func(t *testing.T) {
		ft := stringTransport(textStream("end_turn", validCandidate))
		var receipt provider.RequestReceipt
		// Empty Policy: the provider must derive remote-minimal from the pinned
		// remote endpoint's classification.
		p := Provider{APIKey: "clai-secret-key", transport: ft, receiptSink: func(r provider.RequestReceipt) { receipt = r }}
		if _, err := p.Compile(context.Background(), remoteRequest()); err != nil {
			t.Fatalf("compile: %v", err)
		}
		if receipt.ContextPolicy != machinecontext.PolicyRemoteMinimal {
			t.Errorf("default policy = %q, want remote-minimal", receipt.ContextPolicy)
		}
		if receipt.EndpointClassification != machinecontext.EndpointRemote {
			t.Errorf("endpoint class = %q, want remote", receipt.EndpointClassification)
		}
	})
	t.Run("RemoteMinimalBodyOnlyPermittedContext", TestRemoteMinimalBodyContainsOnlyPermittedContext)
	t.Run("RemoteExplicitOnlyApprovedFields", TestRemoteExplicitIncludesOnlyApprovedFields)
	t.Run("LocalOnlyAgainstRemoteFailsBeforeNetwork", TestLocalOnlyRemoteFailsBeforeNetwork)
	t.Run("UserinfoEndpointRejectedBeforeNetworkOrReceipt", TestUserinfoEndpointRejectedBeforeNetworkOrReceipt)
}

// TestAnthropicStructuredOutputStrictDecode anchors AC-ANTHROPIC-STRUCTURED
// (REQ-ANTHROPIC-005).
func TestAnthropicStructuredOutputStrictDecode(t *testing.T) {
	t.Run("StrictSchemaOnRequest", TestRequestShapeStreamingBoundedStructured)
	t.Run("MalformedOrTrailingRejected", TestStreamMalformedAndTrailingCandidateJSON)
	t.Run("EmptyCommandRejected", TestStreamEmptyCommand)
	t.Run("ThinkingNeverEntersCandidate", TestStreamThinkingInterleavedBeforeText)
}

func TestAnthropicCandidateV2Schema(t *testing.T) {
	ft := stringTransport(textStream("end_turn", `{"command":"rg TODO","explanation":"uses rg","requirements":[{"kind":"tool","name":"rg"}]}`))
	candidates, err := remoteProvider(ft, nil).Compile(context.Background(), remoteRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || len(candidates[0].Requirements) != 1 || candidates[0].Requirements[0].Name != "rg" {
		t.Fatalf("candidates = %+v", candidates)
	}
	body := decodeBody(t, ft.lastBody)
	encoded, err := json.Marshal(body["output_config"])
	if err != nil {
		t.Fatal(err)
	}
	for _, unsupported := range []string{"minLength", "maxLength", "pattern", "maxItems"} {
		if bytes.Contains(encoded, []byte(unsupported)) {
			t.Fatalf("Anthropic schema contains unsupported %q: %s", unsupported, encoded)
		}
	}
	for _, required := range []string{"requirements", "min_version", "additionalProperties"} {
		if !bytes.Contains(encoded, []byte(required)) {
			t.Fatalf("candidate/v2 schema missing %q: %s", required, encoded)
		}
	}
}

// TestAnthropicStreamAccumulationAndStopReasons anchors AC-ANTHROPIC-STREAM
// (REQ-ANTHROPIC-006).
func TestAnthropicStreamAccumulationAndStopReasons(t *testing.T) {
	t.Run("AccumulatesSplitDeltas", TestStreamSuccessCandidateSplitAcrossManyDeltas)
	t.Run("MaxTokensDiscarded", TestStreamMaxTokensAfterPartialContent)
	t.Run("RefusalDiscarded", TestStreamRefusal)
	t.Run("MissingTextDiscarded", TestStreamMissingText)
	t.Run("AmbiguousMultiTextDiscarded", TestStreamMultipleAmbiguousTextPayloads)
	t.Run("ConnectionCloseBeforeTerminalDiscarded", TestStreamConnectionCloseBeforeTerminalCompletion)
	t.Run("CleanEOFAfterEndTurnBeforeMessageStopDiscarded", TestStreamCleanEOFAfterEndTurnBeforeMessageStop)
	t.Run("IncompleteEventLifecycleDiscarded", TestStreamRejectsIncompleteEventLifecycle)
	t.Run("MalformedEventInvalidatesAccumulation", TestStreamMalformedEventInvalidatesAccumulation)
	t.Run("CancellationBeforeCompile", TestStreamCancellationBeforeCompile)
	t.Run("CancellationBeforeFirstEvent", TestStreamCancellationBeforeFirstEvent)
	t.Run("CancellationAfterPartialJSON", TestStreamCancellationAfterPartialJSON)
	t.Run("CancellationPrecedesMalformedLifecycle", TestCancellationPrecedesMalformedLifecycleError)
	t.Run("CallerCancellation", TestCompileHonorsCallerCancellation)
	t.Run("CancellationWhileReading", TestCompileHonorsCancellationWhileReading)
	t.Run("ProviderTimeout", TestCompileTimeout)
	t.Run("StopReasonClassification", TestClassifyStopReason)
}

// TestAnthropicErrorClassificationSecretFree anchors AC-ANTHROPIC-ERRORS
// (REQ-ANTHROPIC-007).
func TestAnthropicErrorClassificationSecretFree(t *testing.T) {
	t.Run("HTTPStatusClassification", TestCompileClassifiesHTTPErrors)
	t.Run("StreamErrorStatusClassification", TestClassifyStreamErrorStatuses)
	t.Run("ErrorBeforeFirstDeltaSecretFree", TestStreamErrorBeforeFirstDelta)
	t.Run("ErrorAfterPartialSecretFree", TestStreamErrorAfterPartialJSON)
}

// TestAnthropicTimeoutAndZeroRetries anchors AC-ANTHROPIC-TIMEOUT-RETRIES
// (REQ-ANTHROPIC-008): a bounded timeout, exactly one network attempt, and
// exactly one receipt per compile.
func TestAnthropicTimeoutAndZeroRetries(t *testing.T) {
	t.Run("DefaultTimeoutIsNinetySeconds", TestDefaultTimeoutIsNinetySeconds)
	t.Run("RequestTimeout", TestCompileTimeout)
	t.Run("RetryCountHeaderIsZero", func(t *testing.T) {
		ft := stringTransport(textStream("end_turn", validCandidate))
		if _, err := remoteProvider(ft, nil).Compile(context.Background(), remoteRequest()); err != nil {
			t.Fatalf("compile: %v", err)
		}
		if got := ft.lastHeader.Get("X-Stainless-Retry-Count"); got != "0" {
			t.Errorf("X-Stainless-Retry-Count = %q, want 0 (SDK retries disabled)", got)
		}
	})
	t.Run("OneAttemptOneReceiptOnServerError", func(t *testing.T) {
		receipts := 0
		ft := &fakeTransport{handler: func(req *http.Request, _ []byte) (*http.Response, error) {
			return jsonErrorResponse(req, http.StatusInternalServerError, `{"type":"error","error":{"type":"api_error","message":"x"}}`), nil
		}}
		p := Provider{APIKey: "k", Policy: machinecontext.PolicyRemoteMinimal, transport: ft, receiptSink: func(provider.RequestReceipt) { receipts++ }}
		_, err := p.Compile(context.Background(), remoteRequest())
		if !errors.Is(err, ErrServer) {
			t.Fatalf("err = %v, want ErrServer", err)
		}
		if ft.calls != 1 {
			t.Errorf("network attempts = %d, want exactly 1 (no retries)", ft.calls)
		}
		if receipts != 1 {
			t.Errorf("receipts emitted = %d, want exactly 1", receipts)
		}
	})
	t.Run("OneReceiptOnSuccess", func(t *testing.T) {
		receipts := 0
		ft := stringTransport(textStream("end_turn", validCandidate))
		p := remoteProvider(ft, func(provider.RequestReceipt) { receipts++ })
		if _, err := p.Compile(context.Background(), remoteRequest()); err != nil {
			t.Fatalf("compile: %v", err)
		}
		if receipts != 1 {
			t.Errorf("receipts emitted = %d, want exactly 1", receipts)
		}
	})
}

// TestAnthropicRefusesRedirects anchors AC-ANTHROPIC-NO-REDIRECT
// (REQ-ANTHROPIC-009).
func TestAnthropicRefusesRedirects(t *testing.T) {
	t.Run("RedirectTargetNeverContacted", TestRedirectTargetNeverContacted)
}

// TestAnthropicReceiptExactBytesAndHash anchors AC-ANTHROPIC-RECEIPT
// (REQ-ANTHROPIC-010, REQ-CONTEXT-018).
func TestAnthropicReceiptExactBytesAndHash(t *testing.T) {
	t.Run("BytesEqualInjectedTransportObservedAndHash", TestReceiptBytesEqualServerObservedBytesAndHash)
	t.Run("BytesEqualHTTPServerObserved", TestReceiptBytesEqualHTTPServerObservedBytes)
	t.Run("BoundedCaptureFailuresFailClosed", TestReceiptCaptureFailuresAreBoundedAndFailClosed)
	t.Run("ProductionDefaultSinkEmitsOnce", TestProductionDefaultReceiptSinkEmitsOnce)
	t.Run("DeterministicForFixedInput", TestReceiptDeterministicForFixedInput)
	t.Run("EmittedExactlyOnce", func(t *testing.T) {
		receipts := 0
		ft := stringTransport(textStream("end_turn", validCandidate))
		p := remoteProvider(ft, func(provider.RequestReceipt) { receipts++ })
		if _, err := p.Compile(context.Background(), remoteRequest()); err != nil {
			t.Fatalf("compile: %v", err)
		}
		if receipts != 1 {
			t.Errorf("receipts = %d, want exactly 1", receipts)
		}
	})
}

// TestAnthropicTransportOnlyNoPartialCandidate anchors AC-ANTHROPIC-TRANSPORT-ONLY
// (REQ-PERFORMANCE-012, REQ-NON-GOAL-009): streaming is a transport detail; only
// a complete candidate ever surfaces, and no partial fragment does.
func TestAnthropicTransportOnlyNoPartialCandidate(t *testing.T) {
	t.Run("CompleteCandidateOnlyAfterFullAccumulation", TestStreamSuccessCandidateSplitAcrossManyDeltas)
	t.Run("ErrorAfterPartialYieldsNoCandidate", TestStreamErrorAfterPartialJSON)
	t.Run("CancellationAfterPartialYieldsNoCandidate", TestStreamCancellationAfterPartialJSON)
	t.Run("CancellationBeforeFirstEventYieldsNoCandidate", TestStreamCancellationBeforeFirstEvent)
	t.Run("ConnectionCloseBeforeTerminalYieldsNoCandidate", TestStreamConnectionCloseBeforeTerminalCompletion)
	t.Run("CleanEOFWithoutMessageStopYieldsNoCandidate", TestStreamCleanEOFAfterEndTurnBeforeMessageStop)
	t.Run("IncompleteLifecycleYieldsNoCandidate", TestStreamRejectsIncompleteEventLifecycle)
}
