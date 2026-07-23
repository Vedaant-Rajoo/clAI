package textsafe

import (
	"fmt"
	"strings"
	"testing"
	"unicode"
)

var prohibitedCommandFormatV1 = []rune{
	0x061C,
	0x200E, 0x200F,
	0x202A, 0x202B, 0x202C, 0x202D, 0x202E,
	0x2066, 0x2067, 0x2068, 0x2069,
	0x200B, 0x200C, 0x200D,
	0x2060,
	0xFEFF,
}

func TestProhibitedCommandFormatV1CanonicalTable(t *testing.T) {
	if got, want := len(prohibitedCommandFormatV1), 17; got != want {
		t.Fatalf("canonical table contains %d code points, want %d", got, want)
	}

	seen := make(map[rune]bool, len(prohibitedCommandFormatV1))
	for _, r := range prohibitedCommandFormatV1 {
		if seen[r] {
			t.Fatalf("canonical table repeats U+%04X", r)
		}
		seen[r] = true
		if !IsProhibitedCommandFormat(r) {
			t.Errorf("IsProhibitedCommandFormat(U+%04X) = false, want true", r)
		}
	}
}

func TestIsProhibitedCommandFormatDoesNotWidenSet(t *testing.T) {
	tests := []struct {
		name string
		r    rune
	}{
		{name: "ordinary ASCII", r: 'A'},
		{name: "ordinary Unicode", r: '界'},
		{name: "soft hyphen Cf", r: 0x00AD},
		{name: "Arabic number sign Cf", r: 0x0600},
		{name: "before zero width range", r: 0x200A},
		{name: "after directional marks", r: 0x2010},
		{name: "before bidi embedding range", r: 0x2029},
		{name: "after bidi embedding range", r: 0x202F},
		{name: "after word joiner", r: 0x2061},
		{name: "before isolate range", r: 0x2065},
		{name: "after isolate range", r: 0x206A},
		{name: "before byte order mark", r: 0xFEFE},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if IsProhibitedCommandFormat(tt.r) {
				t.Fatalf("IsProhibitedCommandFormat(U+%04X) = true, want false", tt.r)
			}
		})
	}

	for _, r := range []rune{0x00AD, 0x0600} {
		if !unicode.In(r, unicode.Cf) {
			t.Fatalf("test fixture U+%04X is not Unicode Cf", r)
		}
	}
}

func TestContainsProhibitedCommandFormatAtEveryPosition(t *testing.T) {
	positions := []struct {
		name   string
		prefix string
		suffix string
	}{
		{name: "first", suffix: "safe"},
		{name: "middle", prefix: "sa", suffix: "fe"},
		{name: "final", prefix: "safe"},
	}

	for _, r := range prohibitedCommandFormatV1 {
		for _, position := range positions {
			t.Run(fmt.Sprintf("U+%04X/%s", r, position.name), func(t *testing.T) {
				command := position.prefix + string(r) + position.suffix
				if !ContainsProhibitedCommandFormat(command) {
					t.Fatalf("ContainsProhibitedCommandFormat(%q) = false, want true", command)
				}
			})
		}
	}

	for _, value := range []string{"", "git status", "printf '%s' 日本語", "soft" + string(rune(0x00AD)) + "hyphen"} {
		if ContainsProhibitedCommandFormat(value) {
			t.Errorf("ContainsProhibitedCommandFormat(%q) = true, want false", value)
		}
	}
}

func TestVisibleExposesEveryProhibitedCommandFormat(t *testing.T) {
	positions := []struct {
		name   string
		prefix string
		suffix string
	}{
		{name: "first", suffix: "safe"},
		{name: "middle", prefix: "sa", suffix: "fe"},
		{name: "final", prefix: "safe"},
	}

	for _, r := range prohibitedCommandFormatV1 {
		for _, position := range positions {
			t.Run(fmt.Sprintf("U+%04X/%s", r, position.name), func(t *testing.T) {
				notation := fmt.Sprintf("U+%04X", r)
				got := Visible(position.prefix + string(r) + position.suffix)
				want := position.prefix + notation + position.suffix
				if got != want {
					t.Fatalf("Visible() = %q, want %q", got, want)
				}
				if strings.ContainsRune(got, r) {
					t.Fatalf("Visible() retained prohibited U+%04X: %q", r, got)
				}
			})
		}
	}
}

func TestVisibleReplacesEveryOccurrence(t *testing.T) {
	for _, r := range prohibitedCommandFormatV1 {
		notation := fmt.Sprintf("U+%04X", r)
		got := Visible("a" + string(r) + "b" + string(r) + "c")
		want := "a" + notation + "b" + notation + "c"
		if got != want {
			t.Errorf("Visible() for U+%04X = %q, want %q", r, got, want)
		}
	}
}

func TestEditableCommandIsOneWay(t *testing.T) {
	positions := []struct {
		name   string
		prefix string
		suffix string
	}{
		{name: "first", suffix: "safe"},
		{name: "middle", prefix: "sa", suffix: "fe"},
		{name: "final", prefix: "safe"},
	}

	for _, r := range prohibitedCommandFormatV1 {
		for _, position := range positions {
			t.Run(fmt.Sprintf("U+%04X/%s", r, position.name), func(t *testing.T) {
				notation := fmt.Sprintf("U+%04X", r)
				got := EditableCommand(position.prefix + string(r) + position.suffix)
				want := position.prefix + notation + position.suffix
				if got != want {
					t.Fatalf("EditableCommand() = %q, want %q", got, want)
				}
				if strings.ContainsRune(got, r) {
					t.Fatalf("EditableCommand() retained prohibited U+%04X: %q", r, got)
				}
			})
		}
	}
}

func TestRepeatedProcessingNeverRecreatesProhibitedRune(t *testing.T) {
	for _, r := range prohibitedCommandFormatV1 {
		input := "before" + string(r) + "after"
		for name, transform := range map[string]func(string) string{
			"Visible":         Visible,
			"EditableCommand": EditableCommand,
		} {
			t.Run(fmt.Sprintf("%s/U+%04X", name, r), func(t *testing.T) {
				once := transform(input)
				twice := transform(once)
				if twice != once {
					t.Fatalf("repeat processing changed output: once %q, twice %q", once, twice)
				}
				if strings.ContainsRune(twice, r) {
					t.Fatalf("repeat processing recreated U+%04X: %q", r, twice)
				}
			})
		}
	}
}

func TestTerminalControlsAndOtherUnicodeRemainScoped(t *testing.T) {
	otherCf := string(rune(0x00AD))
	input := "safe 日本語" + otherCf + "\x1b\x01\t\n\rend"

	visibleWant := "safe 日本語" + otherCf + `\u{001B}\u{0001}\t\n\rend`
	if got := Visible(input); got != visibleWant {
		t.Fatalf("Visible() = %q, want %q", got, visibleWant)
	}

	editableWant := "safe 日本語" + otherCf + "end"
	if got := EditableCommand(input); got != editableWant {
		t.Fatalf("EditableCommand() = %q, want %q", got, editableWant)
	}
}
