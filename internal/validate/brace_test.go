package validate

import (
	"strings"
	"testing"
)

// TestCommandBraceDataAndDispatch pins validation class across brace syntax. A
// literal pair outside command position is fully understood; brace constructs
// outside the parser subset remain eligible with a visible warning.
func TestCommandBraceDataAndDispatch(t *testing.T) {
	tests := []struct {
		name    string
		command string
		class   Class
	}{
		{name: "xargs replacement flag", command: `xargs -I{} du -h {}`, class: Valid},
		{name: "find exec semicolon form", command: `find . -exec du -h {} \;`, class: Valid},
		{name: "find exec plus form", command: `find . -exec du -h {} +`, class: Valid},
		{name: "brace inside a flag value", command: `echo -I{}x`, class: Valid},
		{name: "brace inside a word", command: `echo a{}b`, class: Valid},
		{name: "brace in a leading assignment", command: `FOO={} echo ok`, class: Valid},
		{name: "pipeline with replacement flag", command: `fd --type f | xargs -I{} du -h {} | sort -rh`, class: Valid},
		{name: "quoted brace pair", command: `echo "{}"`, class: Valid},
		{name: "single quoted brace pair", command: `echo '{}'`, class: Valid},

		{name: "bare pair in command position", command: `{}`, class: Warning},
		{name: "pair as path prefix", command: `{}/echo`, class: Warning},
		{name: "pair behind env wrapper", command: `env {}/echo x`, class: Warning},
		{name: "pair consumed as a wrapper", command: `{}/env echo x`, class: Warning},
		{name: "brace expansion", command: `echo {a,b}`, class: Warning},
		{name: "brace range", command: `echo {1..3}`, class: Warning},
		{name: "brace group", command: `{ echo hi; }`, class: Warning},
		{name: "unbalanced opening brace", command: `{cmd`, class: Warning},
		{name: "unbalanced closing brace", command: `echo value}`, class: Warning},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Command(tt.command)
			if result.Class != tt.class {
				t.Fatalf("Command(%q).Class = %q, want %q (reasons: %v)", tt.command, result.Class, tt.class, result.Reasons)
			}
			if !result.Valid {
				t.Fatalf("Command(%q) class %q is not export-eligible", tt.command, result.Class)
			}
			if tt.class == Valid && len(result.Reasons) != 0 {
				t.Fatalf("Command(%q) is valid but carries reasons %v", tt.command, result.Reasons)
			}
			if tt.class == Warning && len(result.Reasons) == 0 {
				t.Fatalf("Command(%q) warns without a reason", tt.command)
			}
		})
	}
}

// TestCommandBraceDispatchWarningIsReadable proves the dispatch-position issue
// remains visible even though warning-class syntax is export-eligible.
func TestCommandBraceDispatchWarningIsReadable(t *testing.T) {
	result := Command(`{}/echo`)
	if !result.Valid || result.Class != Warning {
		t.Fatalf("brace dispatch = %#v, want warning-class eligibility", result)
	}
	joined := strings.Join(result.Reasons, " ")
	if !strings.Contains(joined, "brace syntax in a command position") {
		t.Fatalf("reasons = %v, want the brace dispatch message", result.Reasons)
	}
}
