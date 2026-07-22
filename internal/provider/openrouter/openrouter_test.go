package openrouter

import (
	"strings"
	"testing"

	machinecontext "codeberg.org/newedia/clai/internal/context"
	"codeberg.org/newedia/clai/internal/provider"
)

func TestCompileRequiresAPIKey(t *testing.T) {
	p := Provider{}
	_, err := p.Compile(provider.Request{Intent: "list files"})
	if err == nil || !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("err = %v, want no-API-key error", err)
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

func TestUserPromptIncludesContext(t *testing.T) {
	prompt := userPrompt(provider.Request{
		Intent: "show recent commits",
		Context: machinecontext.Context{
			OS:               "darwin",
			Shell:            "fish",
			WorkingDirectory: "/tmp/repo",
			GitRepository:    true,
			GitRoot:          "/tmp/repo",
			GitBranch:        "main",
		},
	})

	for _, want := range []string{"show recent commits", "darwin", "fish", "/tmp/repo", "main"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestContextSummaryOutsideGit(t *testing.T) {
	summary := contextSummary(machinecontext.Context{GitRepository: false})
	if !strings.Contains(summary, "Git repository: no") {
		t.Errorf("summary = %q, want non-git marker", summary)
	}
}
