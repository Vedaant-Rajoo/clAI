package validate

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCommandBoundaryAndStructuralValidation(t *testing.T) {
	tests := []struct {
		name    string
		command string
		valid   bool
	}{
		{name: "simple command", command: "git status --short", valid: true},
		{name: "supported list and pipeline", command: "pwd && printf x | rg x; date", valid: true},
		{name: "supported assignments wrappers and redirects", command: "FOO=bar env -i command -- /usr/bin/printf ok >out", valid: true},
		{name: "quoted angle brackets", command: "printf '%s\\n' '<tag>'", valid: true},
		{name: "escaped angle brackets", command: `printf \<tag\>`, valid: true},
		{name: "ordinary Unicode", command: "printf '%s' 日本語", valid: true},
		{name: "non prohibited format character", command: "printf soft" + string(rune(0x00AD)) + "hyphen", valid: true},
		{name: "empty command", command: "   ", valid: false},
		{name: "line feed", command: "printf one\nprintf two", valid: false},
		{name: "carriage return", command: "printf one\rprintf two", valid: false},
		{name: "nul byte", command: "printf one\x00printf two", valid: false},
		{name: "escape byte", command: "printf '\x1b]52;c;payload\a'", valid: false},
		{name: "readline control byte", command: "printf one\x01printf two", valid: false},
		{name: "tab control byte", command: "printf\tone", valid: false},
		{name: "unresolved placeholder", command: "rg <pattern>", valid: false},
		{name: "unclosed double quote", command: `printf "hello`, valid: false},
		{name: "unclosed single quote", command: "printf 'hello", valid: false},
		{name: "trailing escape", command: "printf hello\\", valid: false},
		{name: "leading pipe", command: "| ls", valid: false},
		{name: "trailing pipe", command: "ls |", valid: false},
		{name: "trailing conditional", command: "pwd &&", valid: false},
		{name: "trailing semicolon", command: "printf '%s\\n' done;", valid: false},
		{name: "empty list segment", command: "pwd && || date", valid: false},
		{name: "missing redirect target", command: "printf ok >", valid: false},
		{name: "unsupported background", command: "printf ok &", valid: false},
		{name: "unsupported comment", command: "printf ok # note", valid: false},
		{name: "unsupported grouping", command: "(printf ok)", valid: false},
		{name: "unsupported command substitution", command: "printf $(date)", valid: false},
		{name: "unsupported process substitution", command: "cat <(printf x)", valid: false},
		{name: "unsupported parameter expansion", command: "printf $HOME", valid: false},
		{name: "unsupported here document", command: "cat <<EOF", valid: false},
		{name: "unsupported here string", command: "cat <<<value", valid: false},
		{name: "unsupported shell evaluation", command: `sh -c 'rm file'`, valid: false},
		{name: "unsupported env wrapper option", command: "env -u FOO rm file", valid: false},
		{name: "unsupported command wrapper option", command: "command -v rm", valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Command(tt.command)
			if result.Valid != tt.valid {
				t.Fatalf("Command(%q) = %#v, want valid %v", tt.command, result, tt.valid)
			}
			if !tt.valid && len(result.Reasons) == 0 {
				t.Fatalf("Command(%q).Reasons is empty", tt.command)
			}
		})
	}
}

func TestCommandSizeBoundary(t *testing.T) {
	atLimit := strings.Repeat("x", MaxCommandBytes)
	if result := Command(atLimit); !result.Valid {
		t.Fatalf("8192-byte command = %#v, want valid", result)
	}
	overLimit := strings.Repeat("x", MaxCommandBytes+1)
	result := Command(overLimit)
	if result.Valid || !containsReason(result.Reasons, "maximum size") {
		t.Fatalf("8193-byte command = %#v, want size rejection", result)
	}
}

func TestCommandRejectsEveryProhibitedFormatAtEveryPosition(t *testing.T) {
	prohibited := []rune{
		0x061C,
		0x200E, 0x200F,
		0x202A, 0x202B, 0x202C, 0x202D, 0x202E,
		0x2066, 0x2067, 0x2068, 0x2069,
		0x200B, 0x200C, 0x200D,
		0x2060,
		0xFEFF,
	}
	positions := []struct {
		name   string
		prefix string
		suffix string
	}{
		{name: "first", suffix: "printf safe"},
		{name: "middle", prefix: "printf sa", suffix: "fe"},
		{name: "final", prefix: "printf safe"},
	}

	if len(prohibited) != 17 {
		t.Fatalf("prohibited fixture has %d code points, want 17", len(prohibited))
	}
	for _, r := range prohibited {
		for _, position := range positions {
			t.Run(fmt.Sprintf("U+%04X/%s", r, position.name), func(t *testing.T) {
				result := Command(position.prefix + string(r) + position.suffix)
				if result.Valid || !containsReason(result.Reasons, "prohibited invisible") {
					t.Fatalf("Command with U+%04X = %#v, want prohibited-format rejection", r, result)
				}
			})
		}
	}
}

func TestCommandReasonsAreDeterministicAndDeduplicated(t *testing.T) {
	command := "printf\t'broken\n"
	first := Command(command)
	second := Command(command)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("results differ:\nfirst %#v\nsecond %#v", first, second)
	}
	seen := map[string]bool{}
	for _, reason := range first.Reasons {
		if seen[reason] {
			t.Fatalf("duplicate reason %q in %#v", reason, first.Reasons)
		}
		seen[reason] = true
	}
}

func containsReason(reasons []string, fragment string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, fragment) {
			return true
		}
	}
	return false
}
