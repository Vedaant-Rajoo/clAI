package compiler

import (
	"testing"

	machinecontext "codeberg.org/newedia/clai/internal/context"
)

func TestCompile(t *testing.T) {
	tests := []struct {
		name    string
		intent  string
		command string
	}{
		{name: "git status", intent: "show repo status", command: "git status"},
		{name: "git diff", intent: "what changed", command: "git diff"},
		{name: "files", intent: "show files", command: "ls -la"},
		{name: "todos", intent: "find todos", command: "rg TODO"},
		{name: "go tests", intent: "run tests", command: "go test ./..."},
		{name: "fallback", intent: "make me coffee", command: "echo \"No suggestion available yet\""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Compile(Request{Intent: tt.intent, Context: machinecontext.Context{GitRepository: true}})
			if result.Command != tt.command {
				t.Fatalf("Compile(%q).Command = %q, want %q", tt.intent, result.Command, tt.command)
			}

			if result.Explanation == "" {
				t.Fatalf("Compile(%q).Explanation is empty", tt.intent)
			}
		})
	}
}

func TestCompileUsesContext(t *testing.T) {
	tests := []struct {
		name    string
		request Request
		command string
	}{
		{
			name:    "status in git repository",
			request: Request{Intent: "show status", Context: machinecontext.Context{GitRepository: true}},
			command: "git status",
		},
		{
			name:    "status outside git repository",
			request: Request{Intent: "show status", Context: machinecontext.Context{GitRepository: false}},
			command: "ls -la",
		},
		{
			name:    "branch in git repository",
			request: Request{Intent: "current branch", Context: machinecontext.Context{GitRepository: true}},
			command: "git branch --show-current",
		},
		{
			name:    "branch outside git repository",
			request: Request{Intent: "current branch", Context: machinecontext.Context{GitRepository: false}},
			command: "echo \"No Git repository detected\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Compile(tt.request)
			if result.Command != tt.command {
				t.Fatalf("Compile(%+v).Command = %q, want %q", tt.request, result.Command, tt.command)
			}
		})
	}
}
