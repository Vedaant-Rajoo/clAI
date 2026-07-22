// Package openrouter implements provider.Provider backed by OpenRouter's
// chat completions API via the official Go SDK.
package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	openrouter "github.com/OpenRouterTeam/go-sdk"
	"github.com/OpenRouterTeam/go-sdk/models/components"

	machinecontext "codeberg.org/newedia/clai/internal/context"
	"codeberg.org/newedia/clai/internal/provider"
)

// DefaultModel is used when the caller does not specify one.
const DefaultModel = "anthropic/claude-sonnet-4"

const systemPrompt = `You convert a natural-language intent into a single shell command.

Rules:
- Reply with ONLY a JSON object: {"command": "...", "explanation": "..."}.
- The command must be a single line, safe to paste into the user's shell.
- The explanation is one short sentence saying why this command fits.
- Never include markdown fences or extra prose.
- Use the provided environment context (OS, shell, cwd, git) to pick correct flags.`

type Provider struct {
	APIKey string
	Model  string
}

func (p Provider) Compile(request provider.Request) ([]provider.Candidate, error) {
	if p.APIKey == "" {
		return nil, errors.New("openrouter: no API key (run `clai auth login --provider openrouter` or set OPENROUTER_API_KEY)")
	}

	model := p.Model
	if model == "" {
		model = DefaultModel
	}

	client := openrouter.New(openrouter.WithSecurity(p.APIKey))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := client.Chat.Send(ctx, components.ChatRequest{
		Model: openrouter.Pointer(model),
		Messages: []components.ChatMessages{
			components.CreateChatMessagesSystem(components.ChatSystemMessage{
				Content: components.CreateChatSystemMessageContentStr(systemPrompt),
			}),
			components.CreateChatMessagesUser(components.ChatUserMessage{
				Content: components.CreateChatUserMessageContentStr(userPrompt(request)),
			}),
		},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}
	if res == nil || res.ChatResult == nil || len(res.ChatResult.Choices) == 0 {
		return nil, errors.New("openrouter: empty response")
	}

	content, ok := res.ChatResult.Choices[0].Message.Content.Get()
	if !ok || content.Str == nil || *content.Str == "" {
		return nil, errors.New("openrouter: response has no text content")
	}

	candidate, err := parseCandidate(*content.Str)
	if err != nil {
		return nil, err
	}
	return []provider.Candidate{candidate}, nil
}

func userPrompt(request provider.Request) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Intent: %s\n", request.Intent)
	b.WriteString(contextSummary(request.Context))
	return b.String()
}

func contextSummary(c machinecontext.Context) string {
	lines := []string{
		"OS: " + c.OS,
		"Shell: " + c.Shell,
		"Working directory: " + c.WorkingDirectory,
	}
	if c.GitRepository {
		lines = append(lines, "Git repository: yes (root "+c.GitRoot+", branch "+c.GitBranch+")")
	} else {
		lines = append(lines, "Git repository: no")
	}
	return strings.Join(lines, "\n") + "\n"
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
