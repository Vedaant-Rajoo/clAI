// Package anthropic implements provider.Provider directly against Anthropic's
// Messages API using the official Go SDK, with transport-only streaming.
//
// The SDK consumes the Server-Sent-Events response incrementally, but this
// package produces a candidate only after the stream has completed successfully
// and the final message has passed stop-reason and structured-decode checks.
// Partial model text, partial JSON, thinking blocks, refusals, and truncated
// responses never leave this package as a candidate — clai never auto-executes,
// so anything short of one complete, strictly-decoded candidate fails closed.
//
// Credentials reach only the SDK's x-api-key header. The request receipt and its
// hash are computed over the exact bytes the SDK is about to transmit, captured
// by an injected transport immediately before network activity; the receipt
// carries no headers or credentials by construction.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
	"github.com/Vedaant-Rajoo/clai/internal/provider/candidatejson"
)

// DefaultModel is the bare Anthropic model id used when the caller does not
// specify one. It is the direct-API form, not the OpenRouter catalog slug.
const DefaultModel = "claude-sonnet-5"

const (
	// defaultEndpoint is the pinned production Anthropic base URL. The SDK
	// appends the "/v1/messages" path. Any override is unexported and test-only.
	defaultEndpoint = "https://api.anthropic.com"
	// maxTokens bounds the model's output. A command/explanation JSON object is
	// tiny, so this is deliberately generous rather than tight.
	maxTokens = 4096
	// maxRequestBytes hard-bounds the SDK-produced request body the receipt
	// transport will capture and forward. The real body is a few hundred bytes;
	// this only guards against a pathological request.
	maxRequestBytes = 1 << 20
	// maxCandidateBytes hard-bounds the accumulated candidate text before decode.
	// A command/explanation object is tiny; this only fails closed against a
	// hostile or runaway stream that would otherwise accumulate without limit.
	maxCandidateBytes = 64 << 10
	// defaultTimeout is the provider's inner request bound. It nests inside the
	// app's outer 120s compile timeout.
	defaultTimeout = 90 * time.Second
)

// Sentinel errors classify Anthropic failures so callers can react with
// errors.Is without matching on message text. They carry no upstream-controlled
// data and stay prefixed "anthropic:". Cancellation and deadline errors are
// returned unwrapped (context.Canceled / context.DeadlineExceeded) so the app's
// existing errors.Is checks keep working.
var (
	// ErrAuth reports a rejected credential (HTTP 401/403).
	ErrAuth = errors.New("anthropic: authentication error")
	// ErrInvalidRequest reports a rejected request or model (HTTP 400/404/422).
	ErrInvalidRequest = errors.New("anthropic: invalid request")
	// ErrRateLimited reports throttling (HTTP 429).
	ErrRateLimited = errors.New("anthropic: rate limited")
	// ErrServer reports a transient upstream failure (HTTP 5xx / 529 overload).
	ErrServer = errors.New("anthropic: upstream server error")
	// ErrRefusal reports that the model declined to answer.
	ErrRefusal = errors.New("anthropic: model refused to answer")
	// ErrIncompleteResponse reports a stream that never yielded one complete,
	// well-formed candidate: truncation, missing/ambiguous text, an unexpected
	// stop reason, a mid-stream error, or a malformed structured payload.
	ErrIncompleteResponse = errors.New("anthropic: incomplete or malformed response")

	// defaultReceiptSink is the production consumer for ephemeral receipts. It is
	// intentionally non-persisting; tests may replace it to prove the normal CLI
	// construction path still emits exactly once.
	defaultReceiptSink = func(provider.RequestReceipt) {}
)

const systemPrompt = `You convert a natural-language intent into a single shell command candidate.

Rules:
- Return one candidate/v2 object with command, explanation, and optional requirements.
- requirements contains zero to eight actual dependencies using kind tool, shell, or os and lowercase ASCII names.
- min_version is optional and valid only for tool requirements as a numeric dotted version.
- The command must be a single line, safe to paste into the user's shell.
- The command must stay inside the shell syntax subset shared by Fish, Bash, and Zsh. Allowed: plain words, single and double quotes, backslash escapes, leading NAME=value assignments, the env and command wrappers, ; && || and | to combine commands, input/output/append redirects, and the literal brace pair {} as used by xargs -I{} and find -exec {} \;.
- Never use command substitution $(...) or backticks, parameter expansion such as $VAR or ${VAR}, brace expansion such as {a,b} or {1..3}, brace groups { ...; }, subshells ( ... ), comments, here-documents, background &, or passing a command string to a shell with -c. A command using any of these is rejected before the user can accept it, so choose a formulation that avoids them.
- The explanation is one short sentence saying why this command fits the supplied capability facts.
- Use only the normalized capability and environment facts included in the user message; never infer executable paths or raw probe output.
- Declare requirements that the command actually depends on.`

// Provider compiles an intent into one candidate via Anthropic's Messages API.
//
// CLI construction sets the exported fields. DevEndpoint is the loopback-only
// development override (REQ-DEVENDPOINT-001..004): the CLI validates it is
// lexically loopback before constructing the provider, and it replaces the
// pinned production base URL so the full request path stays exercised. The
// remaining seams are unexported and exist for deterministic tests: an alternate
// base transport, base URL, timeout, and a receipt sink.
type Provider struct {
	APIKey       string
	Model        string
	Policy       machinecontext.Policy
	SharedFields []string
	DevEndpoint  string

	baseURL     string
	timeout     time.Duration
	transport   http.RoundTripper
	receiptSink func(provider.RequestReceipt)
}

// streamLifecycle validates the Anthropic SSE message/block lifecycle separately
// from SDK accumulation. The SDK treats a clean transport EOF as non-error and
// can accumulate an end_turn before message_stop, so transport completion alone
// is not sufficient proof that one complete message arrived.
type streamLifecycle struct {
	messageStarted bool
	messageDelta   bool
	messageStopped bool
	openBlocks     map[int64]bool
	blockCount     int64
}

func (s *streamLifecycle) observe(event sdk.MessageStreamEventUnion) error {
	if s.messageStopped {
		return errors.New("event after message_stop")
	}
	switch event.Type {
	case "message_start":
		if s.messageStarted || s.messageDelta || s.blockCount != 0 {
			return errors.New("invalid message_start position")
		}
		s.messageStarted = true
		s.openBlocks = make(map[int64]bool)
	case "content_block_start":
		if !s.messageStarted || s.messageDelta || event.Index != s.blockCount {
			return errors.New("invalid content_block_start position")
		}
		s.openBlocks[event.Index] = true
		s.blockCount++
	case "content_block_delta":
		if !s.messageStarted || s.messageDelta || !s.openBlocks[event.Index] {
			return errors.New("content_block_delta without open block")
		}
	case "content_block_stop":
		if !s.messageStarted || s.messageDelta || !s.openBlocks[event.Index] {
			return errors.New("content_block_stop without open block")
		}
		delete(s.openBlocks, event.Index)
	case "message_delta":
		if !s.messageStarted || s.messageDelta || len(s.openBlocks) != 0 {
			return errors.New("invalid message_delta position")
		}
		s.messageDelta = true
	case "message_stop":
		if !s.messageStarted || !s.messageDelta || len(s.openBlocks) != 0 {
			return errors.New("invalid message_stop position")
		}
		s.messageStopped = true
	default:
		return errors.New("unknown stream event type")
	}
	return nil
}

func (s streamLifecycle) complete() bool {
	return s.messageStarted && s.messageDelta && s.messageStopped && len(s.openBlocks) == 0
}

// Compile performs one streaming Messages request and returns exactly one
// candidate, or an error. It returns no candidate for any failure; partial
// output is never returned, logged, or placed in a provider.Candidate.
func (p Provider) Compile(ctx context.Context, request provider.Request) ([]provider.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Precedence: the unexported test seam, then the loopback-gated development
	// override, then the pinned production endpoint. Both overrides are still
	// classified lexically below, so neither can reach a remote host under a
	// local-only policy or carry userinfo credentials.
	baseURL := p.baseURL
	if baseURL == "" {
		baseURL = p.DevEndpoint
	}
	if baseURL == "" {
		baseURL = defaultEndpoint
	}
	class, err := machinecontext.ClassifyEndpoint(baseURL)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	policy := p.Policy
	if policy == "" {
		policy = machinecontext.DefaultPolicy(false, class)
	}
	if policy == machinecontext.PolicyLocalOnly && class == machinecontext.EndpointRemote {
		return nil, errors.New("anthropic: local-only context policy prohibits a remote endpoint")
	}
	selection, err := machinecontext.Select(request.Context, request.Capabilities, policy, p.SharedFields)
	if err != nil {
		return nil, fmt.Errorf("anthropic: context policy: %w", err)
	}
	if p.APIKey == "" {
		return nil, errors.New("anthropic: no API key (run `clai auth login --provider anthropic` or set ANTHROPIC_API_KEY)")
	}

	model := p.Model
	if model == "" {
		model = DefaultModel
	}

	userContent, err := buildUserContent(request.Intent, selection)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}

	params := sdk.MessageNewParams{
		Model:     sdk.Model(model),
		MaxTokens: maxTokens,
		System:    []sdk.TextBlockParam{{Text: systemPrompt}},
		Messages: []sdk.MessageParam{
			sdk.NewUserMessage(sdk.NewTextBlock(string(userContent))),
		},
		OutputConfig: sdk.OutputConfigParam{
			Format: sdk.JSONOutputFormatParam{Schema: candidatejson.Schema()},
		},
	}

	base := p.transport
	if base == nil {
		base = http.DefaultTransport
	}
	receiptSink := p.receiptSink
	if receiptSink == nil {
		// Receipts are ephemeral by design. Production still computes and emits one
		// per request; the default consumer deliberately does not persist it.
		receiptSink = defaultReceiptSink
	}
	rt := &receiptTransport{
		base:      base,
		model:     model,
		class:     class,
		proxyMode: classifyProxyMode(base, baseURL),
		selection: selection,
		sink:      receiptSink,
	}
	// The injected client refuses redirects (the target is never contacted) and
	// wraps the receipt transport so the receipt is computed over the exact bytes
	// the SDK serializes.
	httpClient := &http.Client{
		Transport: rt,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	timeout := p.timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	// The context deadline is the single authoritative request bound, so a
	// cancellation or timeout surfaces cleanly through requestCtx.Err() below.
	// The SDK's own WithRequestTimeout is intentionally not set to avoid a second
	// racing deadline that would muddy classification.
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := sdk.NewClient(
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(p.APIKey),
		option.WithBaseURL(baseURL),
		option.WithHTTPClient(httpClient),
		option.WithMaxRetries(0),
	)

	stream := client.Messages.NewStreaming(requestCtx, params)
	defer stream.Close()

	message := sdk.Message{}
	lifecycle := streamLifecycle{}
	for stream.Next() {
		// Cancellation and deadline always win, including when the current event is
		// malformed and would otherwise be classified as a provider failure.
		if ctxErr := requestCtx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		event := stream.Current()
		if err := lifecycle.observe(event); err != nil {
			if ctxErr := requestCtx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, fmt.Errorf("%w: invalid stream lifecycle", ErrIncompleteResponse)
		}
		if err := message.Accumulate(event); err != nil {
			if ctxErr := requestCtx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			// A malformed event invalidates the whole accumulation.
			return nil, fmt.Errorf("%w: accumulate", ErrIncompleteResponse)
		}
	}
	// Cancellation and deadline take precedence over any stream error text and
	// are returned unwrapped so the app suppresses / re-labels them as it does
	// for every provider.
	if ctxErr := requestCtx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err := stream.Err(); err != nil {
		return nil, classifyStreamError(err)
	}
	if !lifecycle.complete() {
		return nil, fmt.Errorf("%w: stream ended before message completion", ErrIncompleteResponse)
	}

	if err := classifyStopReason(message.StopReason); err != nil {
		return nil, err
	}

	text, err := singleTextPayload(message)
	if err != nil {
		return nil, err
	}
	candidate, err := candidatejson.Decode([]byte(text))
	if err != nil {
		// Do not echo the (model-controlled) payload; the sentinel is enough.
		return nil, fmt.Errorf("%w: candidate decode failed", ErrIncompleteResponse)
	}
	return []provider.Candidate{candidate}, nil
}

// buildUserContent marshals the intent plus only the policy-selected context
// fields into the user message text.
func buildUserContent(intent string, selection machinecontext.Selection) ([]byte, error) {
	return json.Marshal(machinecontext.ProviderPayloadFor(intent, selection))
}

// classifyStopReason maps the final stop reason to success or a fail-closed
// error. Only a natural end_turn yields a candidate.
func classifyStopReason(reason sdk.StopReason) error {
	switch reason {
	case sdk.StopReasonEndTurn:
		return nil
	case sdk.StopReasonMaxTokens:
		return fmt.Errorf("%w: output truncated at the token limit", ErrIncompleteResponse)
	case sdk.StopReasonRefusal:
		return ErrRefusal
	default:
		// tool_use, pause_turn, stop_sequence, model_context_window_exceeded, or
		// any unknown value: the turn did not complete as a plain answer.
		return fmt.Errorf("%w: unexpected stop reason", ErrIncompleteResponse)
	}
}

// singleTextPayload requires exactly one text content block and returns its
// text. Thinking blocks are ignored; any other block type fails closed.
func singleTextPayload(message sdk.Message) (string, error) {
	var text string
	count := 0
	for i := range message.Content {
		block := message.Content[i]
		switch block.Type {
		case "text":
			count++
			text = block.Text
		case "thinking", "redacted_thinking":
			// Never parsed or rendered.
			continue
		default:
			return "", fmt.Errorf("%w: unexpected content block", ErrIncompleteResponse)
		}
	}
	if count == 0 {
		return "", fmt.Errorf("%w: response contained no text", ErrIncompleteResponse)
	}
	if count > 1 {
		return "", fmt.Errorf("%w: response contained multiple text blocks", ErrIncompleteResponse)
	}
	if len(text) > maxCandidateBytes {
		return "", fmt.Errorf("%w: response text exceeds size limit", ErrIncompleteResponse)
	}
	return text, nil
}

// classifyStreamError maps a non-context stream error to a differentiated,
// wrappable sentinel. For an *sdk.Error only the numeric status is reflected;
// the upstream body, reason phrase, and any mid-stream error text are never
// echoed, so a hostile upstream cannot inject text into the error.
func classifyStreamError(err error) error {
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) {
		switch status := apiErr.StatusCode; {
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return fmt.Errorf("%w (HTTP %d): re-authenticate or check the API key (run `clai auth login --provider anthropic` or set ANTHROPIC_API_KEY)", ErrAuth, status)
		case status == http.StatusBadRequest || status == http.StatusNotFound || status == http.StatusUnprocessableEntity:
			return fmt.Errorf("%w (HTTP %d): the request or model was rejected", ErrInvalidRequest, status)
		case status == http.StatusTooManyRequests:
			return fmt.Errorf("%w (HTTP %d): back off before retrying", ErrRateLimited, status)
		case status == 529 || (status >= 500 && status <= 599):
			return fmt.Errorf("%w (HTTP %d): transient upstream failure, retry later", ErrServer, status)
		}
		// A mid-stream error event carries the 200 response status; treat any
		// other status as an incomplete response without echoing upstream text.
		return fmt.Errorf("%w: stream reported an error", ErrIncompleteResponse)
	}
	// Transport / decode error: never echo raw text (the response is
	// model-controlled and may be hostile).
	return fmt.Errorf("%w: stream failed", ErrIncompleteResponse)
}

// classifyProxyMode reports "direct", "configured-proxy", or "unknown" for the
// base transport. It never reveals a proxy URL or credentials.
func classifyProxyMode(base http.RoundTripper, endpoint string) string {
	httpTransport, ok := base.(*http.Transport)
	if !ok {
		return "unknown"
	}
	if httpTransport.Proxy == nil {
		return "direct"
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, nil)
	if err != nil {
		return "unknown"
	}
	proxyURL, err := httpTransport.Proxy(req)
	if err != nil {
		return "unknown"
	}
	if proxyURL == nil {
		return "direct"
	}
	return "configured-proxy"
}
