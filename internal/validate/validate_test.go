package validate

import (
	"strings"
	"testing"
)

func TestCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		valid   bool
	}{
		{name: "valid command", command: "git status --short", valid: true},
		{name: "valid redirect", command: "go test ./... > test.log", valid: true},
		{name: "valid trailing semicolon", command: "printf '%s\\n' done;", valid: true},
		{name: "quoted angle brackets", command: "printf '%s\\n' '<tag>'", valid: true},
		{name: "empty command", command: "   ", valid: false},
		{name: "line feed", command: "printf one\nprintf two", valid: false},
		{name: "carriage return", command: "printf one\rprintf two", valid: false},
		{name: "nul byte", command: "printf one\x00printf two", valid: false},
		{name: "escape byte", command: "printf '\x1b]52;c;payload\a'", valid: false},
		{name: "readline control byte", command: "printf one\x01printf two", valid: false},
		{name: "tab control byte", command: "printf\tone", valid: false},
		{name: "maximum size", command: strings.Repeat("x", MaxCommandBytes), valid: true},
		{name: "over maximum size", command: strings.Repeat("x", MaxCommandBytes+1), valid: false},
		{name: "unresolved placeholder", command: "rg <pattern>", valid: false},
		{name: "unclosed double quote", command: "printf \"hello", valid: false},
		{name: "trailing pipe", command: "ls |", valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Command(tt.command)
			if result.Valid != tt.valid {
				t.Fatalf("Command(%q).Valid = %v, want %v", tt.command, result.Valid, tt.valid)
			}

			if !tt.valid && len(result.Reasons) == 0 {
				t.Fatalf("Command(%q).Reasons is empty", tt.command)
			}
		})
	}
}
