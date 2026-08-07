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
		class   Class
	}{
		{name: "simple command", command: "git status --short", class: Valid},
		{name: "supported list and pipeline", command: "pwd && printf x | rg x; date", class: Valid},
		{name: "supported assignments wrappers and redirects", command: "FOO=bar env -i command -- /usr/bin/printf ok >out", class: Valid},
		{name: "quoted angle brackets", command: "printf '%s\\n' '<tag>'", class: Valid},
		{name: "escaped angle brackets", command: `printf \<tag\>`, class: Valid},
		{name: "ordinary Unicode", command: "printf '%s' 日本語", class: Valid},
		{name: "non prohibited format character", command: "printf soft" + string(rune(0x00AD)) + "hyphen", class: Valid},
		{name: "empty command", command: "   ", class: Invalid},
		{name: "line feed", command: "printf one\nprintf two", class: Invalid},
		{name: "carriage return", command: "printf one\rprintf two", class: Invalid},
		{name: "nul byte", command: "printf one\x00printf two", class: Invalid},
		{name: "escape byte", command: "printf '\x1b]52;c;payload\a'", class: Invalid},
		{name: "readline control byte", command: "printf one\x01printf two", class: Invalid},
		{name: "tab control byte", command: "printf\tone", class: Invalid},
		{name: "unresolved placeholder", command: "rg <pattern>", class: Invalid},
		{name: "unclosed double quote", command: `printf "hello`, class: Invalid},
		{name: "unclosed single quote", command: "printf 'hello", class: Invalid},
		{name: "trailing escape", command: "printf hello\\", class: Invalid},
		{name: "leading pipe", command: "| ls", class: Invalid},
		{name: "trailing pipe", command: "ls |", class: Invalid},
		{name: "trailing conditional", command: "pwd &&", class: Invalid},
		{name: "trailing semicolon", command: "printf '%s\\n' done;", class: Invalid},
		{name: "empty list segment", command: "pwd && || date", class: Invalid},
		{name: "missing redirect target", command: "printf ok >", class: Invalid},
		{name: "unsupported background", command: "printf ok &", class: Warning},
		{name: "unsupported comment", command: "printf ok # note", class: Warning},
		{name: "unsupported grouping", command: "(printf ok)", class: Warning},
		{name: "unsupported command substitution", command: "printf $(date)", class: Warning},
		{name: "unsupported process substitution", command: "cat <(printf x)", class: Warning},
		{name: "unsupported parameter expansion", command: "printf $HOME", class: Warning},
		{name: "unsupported here document", command: "cat <<EOF", class: Warning},
		{name: "unsupported here string", command: "cat <<<value", class: Warning},
		{name: "unsupported shell evaluation", command: `sh -c 'rm file'`, class: Warning},
		{name: "unsupported env wrapper option", command: "env -u FOO rm file", class: Warning},
		{name: "unsupported command wrapper option", command: "command -v rm", class: Warning},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Command(tt.command)
			if result.Class != tt.class {
				t.Fatalf("Command(%q) = %#v, want class %q", tt.command, result, tt.class)
			}
			if got, want := result.Valid, tt.class != Invalid; got != want {
				t.Fatalf("Command(%q).Valid = %v, want %v for class %q", tt.command, got, want, tt.class)
			}
			if tt.class == Valid && len(result.Reasons) != 0 {
				t.Fatalf("Command(%q) is fully valid but carries reasons %v", tt.command, result.Reasons)
			}
			if tt.class != Valid && len(result.Reasons) == 0 {
				t.Fatalf("Command(%q) has class %q without a reason", tt.command, tt.class)
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
