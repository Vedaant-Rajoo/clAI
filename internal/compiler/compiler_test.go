package compiler

import (
	"testing"

	machinecontext "codeberg.org/newedia/clai/internal/context"
)

func TestCompile(t *testing.T) {
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
			name:    "git command outside repository",
			request: Request{Intent: "current branch", Context: machinecontext.Context{GitRepository: false}},
			command: "echo \"No Git repository detected\"",
		},
		{
			name:    "non-git command",
			request: Request{Intent: "run tests"},
			command: "go test ./...",
		},
		{
			name:    "fallback",
			request: Request{Intent: "make me coffee"},
			command: "echo \"No suggestion available yet\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Compile(tt.request)
			if result.Command != tt.command {
				t.Fatalf("Compile(%+v).Command = %q, want %q", tt.request, result.Command, tt.command)
			}

			if result.Explanation == "" {
				t.Fatalf("Compile(%+v).Explanation is empty", tt.request)
			}
		})
	}
}
