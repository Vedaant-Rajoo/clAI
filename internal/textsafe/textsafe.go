// Package textsafe provides one-way transformations for displaying and editing
// untrusted text without preserving prohibited command-format characters.
package textsafe

import (
	"fmt"
	"strings"
	"unicode"
)

// IsProhibitedCommandFormat reports whether r belongs to the exhaustive
// prohibited-command-format/v1 set.
func IsProhibitedCommandFormat(r rune) bool {
	switch {
	case r == 0x061C,
		r >= 0x200B && r <= 0x200F,
		r >= 0x202A && r <= 0x202E,
		r == 0x2060,
		r >= 0x2066 && r <= 0x2069,
		r == 0xFEFF:
		return true
	default:
		return false
	}
}

// ContainsProhibitedCommandFormat reports whether value contains any member of
// the prohibited-command-format/v1 set. It does not modify value.
func ContainsProhibitedCommandFormat(value string) bool {
	for _, r := range value {
		if IsProhibitedCommandFormat(r) {
			return true
		}
	}
	return false
}

// Visible returns a terminal-safe, one-way display representation of untrusted
// text. Prohibited command-format characters use literal U+XXXX notation.
// Terminal control characters retain the application's existing visible escape
// forms. Other Unicode format characters are preserved.
func Visible(value string) string {
	var result strings.Builder
	for _, r := range value {
		if IsProhibitedCommandFormat(r) {
			writeCodePoint(&result, r)
			continue
		}
		if !unicode.IsControl(r) {
			result.WriteRune(r)
			continue
		}
		switch r {
		case '\n':
			result.WriteString(`\n`)
		case '\r':
			result.WriteString(`\r`)
		case '\t':
			result.WriteString(`\t`)
		default:
			fmt.Fprintf(&result, `\u{%04X}`, r)
		}
	}
	return result.String()
}

// EditableCommand returns a one-way representation suitable for a command
// editor. Prohibited command-format characters become literal U+XXXX text and
// terminal control characters are removed. No reverse transformation is
// provided; the returned text cannot recreate the original prohibited rune.
func EditableCommand(value string) string {
	var result strings.Builder
	for _, r := range value {
		switch {
		case IsProhibitedCommandFormat(r):
			writeCodePoint(&result, r)
		case unicode.IsControl(r):
			// Command editors cannot safely retain terminal control characters.
		default:
			result.WriteRune(r)
		}
	}
	return result.String()
}

func writeCodePoint(result *strings.Builder, r rune) {
	fmt.Fprintf(result, "U+%04X", r)
}
