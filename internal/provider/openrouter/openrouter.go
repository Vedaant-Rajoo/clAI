// Package openrouter implements provider.Provider backed by OpenRouter's
// chat completions API.
package openrouter

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
	"strings"
	"time"

	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
	"github.com/Vedaant-Rajoo/clai/internal/textsafe"
)

// DefaultModel is used when the caller does not specify one.
//
// Verified against OpenRouter's live catalog on 2026-07-23: "anthropic/claude-sonnet-5"
// resolves (canonical "anthropic/claude-sonnet-5-20260630", the current
// Sonnet-class model). The prior default "anthropic/claude-sonnet-4" has been
// retired from the catalog, so this migration is required, not cosmetic.
const DefaultModel = "anthropic/claude-sonnet-5"

const (
	defaultEndpoint  = "https://openrouter.ai/api/v1/chat/completions"
	maxResponseBytes = 4 << 20
	// maxRetryAfterRunes hard-bounds how much of an upstream Retry-After header
	// value is ever echoed back in an error message.
	maxRetryAfterRunes = 64
)

// Sentinel errors classify non-2xx OpenRouter responses so callers can react
// with errors.Is without matching on message text. They carry no
// upstream-controlled data and every returned error stays prefixed "openrouter:".
var (
	// ErrAuth reports a rejected credential (HTTP 401/403). The API key is
	// never included in the error.
	ErrAuth = errors.New("openrouter: authentication error")
	// ErrRateLimited reports throttling (HTTP 429).
	ErrRateLimited = errors.New("openrouter: rate limited")
	// ErrServer reports a transient upstream failure (HTTP 5xx).
	ErrServer = errors.New("openrouter: upstream server error")
)

const systemPrompt = `You convert a natural-language intent into a single shell command.

Rules:
- Reply with ONLY a JSON object: {"command": "...", "explanation": "..."}.
- The command must be a single line, safe to paste into the user's shell.
- The explanation is one short sentence saying why this command fits.
- Never include markdown fences or extra prose.
- Use only the environment context included in the user message.`

type Provider struct {
	APIKey       string
	Model        string
	Policy       machinecontext.Policy
	SharedFields []string

	endpoint    string
	timeout     time.Duration
	client      *http.Client
	receiptSink func(RequestReceipt)
}

// RequestReceipt and BodyHash moved to the provider-neutral internal/provider
// package so the direct Anthropic provider emits the same receipt shape. They are
// kept here as type aliases (not new named types) so every existing package-local
// reference — including the golden receipt tests — stays byte-identical.
type RequestReceipt = provider.RequestReceipt

type BodyHash = provider.BodyHash

type requestBody struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type promptContext struct {
	OSFamily         string `json:"os_family,omitempty"`
	ShellFamily      string `json:"shell_family,omitempty"`
	ProjectKind      string `json:"project_kind,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	GitRoot          string `json:"git_root,omitempty"`
	GitBranch        string `json:"git_branch,omitempty"`
}

type promptPayload struct {
	Intent  string        `json:"intent"`
	Context promptContext `json:"context"`
}

func (p Provider) Compile(ctx context.Context, request provider.Request) ([]provider.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	endpoint := p.endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	class, err := machinecontext.ClassifyEndpoint(endpoint)
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}
	policy := p.Policy
	if policy == "" {
		policy = machinecontext.DefaultPolicy(false, class)
	}
	if policy == machinecontext.PolicyLocalOnly && class == machinecontext.EndpointRemote {
		return nil, errors.New("openrouter: local-only context policy prohibits a remote endpoint")
	}
	selection, err := machinecontext.Select(request.Context, policy, p.SharedFields)
	if err != nil {
		return nil, fmt.Errorf("openrouter: context policy: %w", err)
	}
	if p.APIKey == "" {
		return nil, errors.New("openrouter: no API key (run `clai auth login --provider openrouter` or set OPENROUTER_API_KEY)")
	}

	model := p.Model
	if model == "" {
		model = DefaultModel
	}
	bodyBytes, err := buildRequestBody(model, request.Intent, selection)
	if err != nil {
		return nil, fmt.Errorf("openrouter: encode request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("openrouter: create request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+p.APIKey)

	client := noRedirectClient(p.client)
	proxyMode := classifyProxyMode(client, httpRequest)
	receipt := makeReceipt(model, endpoint, class, proxyMode, selection, bodyBytes)
	if p.receiptSink != nil {
		p.receiptSink(receipt)
	}

	timeout := p.timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	requestContext, cancel := context.WithTimeout(httpRequest.Context(), timeout)
	defer cancel()
	httpRequest = httpRequest.WithContext(requestContext)

	response, err := client.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, classifyHTTPError(response)
	}
	responseBytes, err := readResponseBody(response.Body)
	if err != nil {
		if contextErr := requestContext.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, err
	}

	content, err := responseContent(responseBytes)
	if err != nil {
		return nil, err
	}
	candidate, err := parseCandidate(content)
	if err != nil {
		return nil, err
	}
	return []provider.Candidate{candidate}, nil
}

func buildRequestBody(model, intent string, selection machinecontext.Selection) ([]byte, error) {
	contextValues := promptContext{}
	for _, field := range selection.Capsule.Fields {
		if field.Sharing != machinecontext.SharingSelected {
			continue
		}
		switch field.Name {
		case machinecontext.FieldOSFamily:
			contextValues.OSFamily = field.Value
		case machinecontext.FieldShellFamily:
			contextValues.ShellFamily = field.Value
		case machinecontext.FieldProjectKind:
			contextValues.ProjectKind = field.Value
		case machinecontext.FieldWorkingDirectory:
			contextValues.WorkingDirectory = field.Value
		case machinecontext.FieldGitRoot:
			contextValues.GitRoot = field.Value
		case machinecontext.FieldGitBranch:
			contextValues.GitBranch = field.Value
		}
	}
	payloadBytes, err := json.Marshal(promptPayload{Intent: intent, Context: contextValues})
	if err != nil {
		return nil, err
	}
	return json.Marshal(requestBody{
		Model: model,
		Messages: []message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: string(payloadBytes)},
		},
	})
}

func makeReceipt(model, endpoint string, class machinecontext.EndpointClass, proxyMode string, selection machinecontext.Selection, body []byte) RequestReceipt {
	sum := sha256.Sum256(body)
	receipt := RequestReceipt{
		Version: "request-receipt/v1", Provider: "openrouter", Model: model,
		EffectiveEndpoint: endpoint, EndpointClassification: class, ProxyMode: proxyMode,
		ContextPolicy: selection.Policy, SelectorVersion: machinecontext.SelectorVersion,
		RequestBody:     append([]byte(nil), body...),
		RequestBodyHash: BodyHash{Algorithm: "sha256", Value: hex.EncodeToString(sum[:])},
	}
	for _, field := range selection.Capsule.Fields {
		switch field.Sharing {
		case machinecontext.SharingSelected:
			receipt.SelectedFields = append(receipt.SelectedFields, field)
		case machinecontext.SharingRedacted:
			receipt.RedactedFields = append(receipt.RedactedFields, field)
		case machinecontext.SharingOmitted:
			receipt.OmittedFields = append(receipt.OmittedFields, field)
		}
	}
	return receipt
}

func noRedirectClient(base *http.Client) *http.Client {
	if base == nil {
		base = &http.Client{}
	}
	client := *base
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &client
}

func readResponseBody(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil {
		return nil, errors.New("openrouter: read response failed")
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("openrouter: response exceeds size limit")
	}
	return body, nil
}

// classifyHTTPError maps a non-2xx response to a differentiated, wrappable
// error. Only the numeric status and (for 429) a sanitized Retry-After value
// are ever reflected: the response body and upstream reason phrase are never
// read or echoed, so a hostile upstream cannot inject text into the error.
func classifyHTTPError(response *http.Response) error {
	status := response.StatusCode
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d): re-authenticate or check the API key (run `clai auth login --provider openrouter` or set OPENROUTER_API_KEY)", ErrAuth, status)
	case status == http.StatusTooManyRequests:
		if retry := sanitizeRetryAfter(response.Header.Get("Retry-After")); retry != "" {
			return fmt.Errorf("%w (HTTP %d): retry after %s, then back off before retrying", ErrRateLimited, status, retry)
		}
		return fmt.Errorf("%w (HTTP %d): back off before retrying", ErrRateLimited, status)
	case status >= 500 && status <= 599:
		return fmt.Errorf("%w (HTTP %d): transient upstream failure, retry later", ErrServer, status)
	default:
		return fmt.Errorf("openrouter: HTTP status %d", status)
	}
}

// sanitizeRetryAfter returns a terminal-safe, length-bounded rendering of an
// upstream Retry-After header value, or "" when it is absent/blank. The value
// is upstream-controlled, so it is hard-capped and passed through
// textsafe.Visible to neutralize control and format characters before it can
// reach a terminal-rendered error.
func sanitizeRetryAfter(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) > maxRetryAfterRunes {
		runes = runes[:maxRetryAfterRunes]
	}
	return textsafe.Visible(string(runes))
}

func classifyProxyMode(client *http.Client, request *http.Request) string {
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		return "unknown"
	}
	if httpTransport.Proxy == nil {
		return "direct"
	}
	proxyURL, err := httpTransport.Proxy(request)
	if err != nil {
		return "unknown"
	}
	if proxyURL == nil {
		return "direct"
	}
	return "configured-proxy"
}

func responseContent(body []byte) (string, error) {
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("openrouter: parse response envelope: %w", err)
	}
	if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content == "" {
		return "", errors.New("openrouter: response has no text content")
	}
	return parsed.Choices[0].Message.Content, nil
}

func parseCandidate(raw string) (provider.Candidate, error) {
	trimmed := strings.TrimSpace(raw)
	// Tolerate markdown fences despite the prompt forbidding them.
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)

	var parsed struct {
		Command     string `json:"command"`
		Explanation string `json:"explanation"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return provider.Candidate{}, fmt.Errorf("openrouter: parse response: %w", err)
	}
	if parsed.Command == "" {
		return provider.Candidate{}, errors.New("openrouter: response contained no command")
	}
	return provider.Candidate{Command: parsed.Command, Explanation: parsed.Explanation}, nil
}
