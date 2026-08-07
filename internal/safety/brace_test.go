package safety

import "testing"

// TestEvaluateBraceDataAndDispatch pins the safety decisions on both sides of
// the literal brace pair: inert as an argument, still conservative wherever it
// could name an executable.
func TestEvaluateBraceDataAndDispatch(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		decision Decision
	}{
		// A brace pair in argument position is data, so the decision comes from
		// the real executable and nothing else.
		{name: "read-only dispatched executable with brace arguments allows", command: `xargs -I{} du -h {}`, decision: Allow},
		{name: "read-only executable with brace argument allows", command: `echo -I{}x`, decision: Allow},
		{name: "brace inside a word allows", command: `echo a{}b`, decision: Allow},
		{name: "brace in a leading assignment allows", command: `FOO={} echo ok`, decision: Allow},

		// executableBase reduces {}/echo to echo. Without the dispatch guard
		// these would reach the read-only allow list.
		{name: "brace path prefix never allows", command: `{}/echo`, decision: Warn},
		{name: "bare brace pair never allows", command: `{}`, decision: Warn},
		{name: "brace path prefix before destructive base blocks", command: `{}/rm`, decision: Block},
		{name: "brace behind env wrapper never allows", command: `env {}/echo x`, decision: Warn},
		{name: "brace behind command wrapper never allows", command: `command {}/echo`, decision: Warn},
		{name: "brace consumed as a wrapper never allows", command: `{}/env echo x`, decision: Warn},

		// Expansion and grouping stay outside the subset.
		{name: "brace expansion warns", command: `echo {a,b}`, decision: Warn},
		{name: "brace range warns", command: `echo {1..3}`, decision: Warn},
		{name: "brace group warns", command: `{ echo hi; }`, decision: Warn},
		{name: "brace group with destructive body warns", command: `{ rm -rf /; }`, decision: Warn},

		// Quoted braces were already data and are unchanged.
		{name: "double quoted brace allows", command: `echo "{}"`, decision: Allow},
		{name: "single quoted brace allows", command: `echo '{}'`, decision: Allow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Evaluate(tt.command)
			if result.Decision != tt.decision {
				t.Fatalf("Evaluate(%q) = %#v, want %q", tt.command, result, tt.decision)
			}
		})
	}
}

// TestEvaluateFindExternalDispatch proves find's own -exec family warns. The
// dispatched executable is find argument syntax, so it never reaches
// classifyExecutable: `find . -exec rm -rf . \;` resolves to base find and read
// as read-only before this rule existed.
func TestEvaluateFindExternalDispatch(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		decision Decision
	}{
		{name: "plain find allows", command: `find . -type f`, decision: Allow},
		{name: "find with name filter allows", command: `find . -name '*.log' -size +50M`, decision: Allow},
		{name: "find exec warns", command: `find . -exec rm -rf . \;`, decision: Warn},
		{name: "find exec with brace warns", command: `find . -exec du -h {} \;`, decision: Warn},
		{name: "find exec plus form warns", command: `find . -exec du -h {} +`, decision: Warn},
		{name: "find execdir warns", command: `find . -execdir rm {} \;`, decision: Warn},
		{name: "find ok warns", command: `find . -ok rm {} \;`, decision: Warn},
		{name: "find okdir warns", command: `find . -okdir rm {} \;`, decision: Warn},
		{name: "find delete warns", command: `find . -delete`, decision: Warn},
		{name: "find fprint warns", command: `find . -fprint results.txt`, decision: Warn},
		// The flag is matched as a whole argument, so lookalike values do not
		// trigger it.
		{name: "find with exec-like filename allows", command: `find . -name -exec-notes`, decision: Allow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Evaluate(tt.command)
			if result.Decision != tt.decision {
				t.Fatalf("Evaluate(%q) = %#v, want %q", tt.command, result, tt.decision)
			}
			if tt.decision == Warn && len(result.Reasons) == 0 {
				t.Fatalf("Evaluate(%q) warned without a reason", tt.command)
			}
		})
	}
}
