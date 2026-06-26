package validate

import "testing"

func TestCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		valid   bool
	}{
		{name: "valid command", command: "git status --short", valid: true},
		{name: "valid redirect", command: "go test ./... > test.log", valid: true},
		{name: "empty command", command: "   ", valid: false},
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
