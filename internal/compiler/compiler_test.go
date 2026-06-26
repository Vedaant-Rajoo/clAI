package compiler

import "testing"

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
			result := Compile(tt.intent)
			if result.Command != tt.command {
				t.Fatalf("Compile(%q).Command = %q, want %q", tt.intent, result.Command, tt.command)
			}

			if result.Explanation == "" {
				t.Fatalf("Compile(%q).Explanation is empty", tt.intent)
			}
		})
	}
}
