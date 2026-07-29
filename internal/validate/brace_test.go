package validate

import (
	"strings"
	"testing"
)

// TestCommandBraceDataAndDispatch pins structural validity across the literal
// brace pair. A pair outside command position is word data and therefore
// structurally eligible for review; a pair that could name an executable is not.
func TestCommandBraceDataAndDispatch(t *testing.T) {
	tests := []struct {
		name    string
		command string
		valid   bool
	}{
		{name: "xargs replacement flag", command: `xargs -I{} du -h {}`, valid: true},
		{name: "find exec semicolon form", command: `find . -exec du -h {} \;`, valid: true},
		{name: "find exec plus form", command: `find . -exec du -h {} +`, valid: true},
		{name: "brace inside a flag value", command: `echo -I{}x`, valid: true},
		{name: "brace inside a word", command: `echo a{}b`, valid: true},
		{name: "brace in a leading assignment", command: `FOO={} echo ok`, valid: true},
		{name: "pipeline with replacement flag", command: `fd --type f | xargs -I{} du -h {} | sort -rh`, valid: true},
		{name: "quoted brace pair", command: `echo "{}"`, valid: true},
		{name: "single quoted brace pair", command: `echo '{}'`, valid: true},

		{name: "bare pair in command position", command: `{}`, valid: false},
		{name: "pair as path prefix", command: `{}/echo`, valid: false},
		{name: "pair behind env wrapper", command: `env {}/echo x`, valid: false},
		{name: "pair consumed as a wrapper", command: `{}/env echo x`, valid: false},
		{name: "brace expansion", command: `echo {a,b}`, valid: false},
		{name: "brace range", command: `echo {1..3}`, valid: false},
		{name: "brace group", command: `{ echo hi; }`, valid: false},
		{name: "unbalanced opening brace", command: `{cmd`, valid: false},
		{name: "unbalanced closing brace", command: `echo value}`, valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Command(tt.command)
			if result.Valid != tt.valid {
				t.Fatalf("Command(%q).Valid = %v, want %v (reasons: %v)", tt.command, result.Valid, tt.valid, result.Reasons)
			}
			if tt.valid && len(result.Reasons) != 0 {
				t.Fatalf("Command(%q) is valid but carries reasons %v", tt.command, result.Reasons)
			}
			if !tt.valid && len(result.Reasons) == 0 {
				t.Fatalf("Command(%q) is invalid without a reason", tt.command)
			}
		})
	}
}

// TestCommandBraceDispatchReasonIsReadable proves the dispatch-position issue
// reaches the user as an ordinary structural reason through the existing
// default arm, needing no new consumer branch.
func TestCommandBraceDispatchReasonIsReadable(t *testing.T) {
	result := Command(`{}/echo`)
	if result.Valid {
		t.Fatal("brace dispatch reported valid")
	}
	joined := strings.Join(result.Reasons, " ")
	if !strings.Contains(joined, "brace syntax in a command position") {
		t.Fatalf("reasons = %v, want the brace dispatch message", result.Reasons)
	}
}
