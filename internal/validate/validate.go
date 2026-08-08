package validate

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/Vedaant-Rajoo/clai/internal/shellsyntax"
	"github.com/Vedaant-Rajoo/clai/internal/textsafe"
)

var placeholderPattern = regexp.MustCompile(`<[^<>[:space:]][^<>]*>`)

const MaxCommandBytes = 8 * 1024

// Class distinguishes commands clai can fully validate, commands containing
// well-formed syntax outside its non-evaluating parser subset, and commands
// that must not be accepted or exported.
type Class string

const (
	Valid   Class = "valid"
	Warning Class = "warning"
	Invalid Class = "invalid"
)

type Result struct {
	Class Class
	// Valid reports export eligibility. Warning-class results remain eligible;
	// callers that render validation details should also inspect Class.
	Valid   bool
	Reasons []string
}

// Command validates whether command belongs to the bounded common shell subset
// eligible for review and export. Unsupported but well-formed syntax produces a
// visible warning; malformed syntax, prohibited bytes/format characters,
// unresolved placeholders, and the byte limit remain invalid. It never repairs,
// strips, expands, or evaluates command text.
func Command(command string) Result {
	class := Valid
	var reasons []string
	addReason := func(next Class, reason string) {
		for _, existing := range reasons {
			if existing == reason {
				if next == Invalid {
					class = Invalid
				} else if class == Valid {
					class = next
				}
				return
			}
		}
		reasons = append(reasons, reason)
		if next == Invalid {
			class = Invalid
		} else if class == Valid {
			class = next
		}
	}

	if strings.TrimSpace(command) == "" {
		addReason(Invalid, "Command is empty.")
	}
	if strings.ContainsAny(command, "\r\n") {
		addReason(Invalid, "Command must be a single record without CR or LF.")
	}
	if strings.ContainsRune(command, '\x00') {
		addReason(Invalid, "Command contains a NUL byte.")
	}
	if hasOtherControl(command) {
		addReason(Invalid, "Command contains a terminal control character.")
	}
	if textsafe.ContainsProhibitedCommandFormat(command) {
		addReason(Invalid, "Command contains a prohibited invisible or bidirectional format character.")
	}
	if len(command) > MaxCommandBytes {
		addReason(Invalid, "Command exceeds the maximum size of 8192 bytes.")
	}

	// Parsing is deliberately bounded. Oversize input is already ineligible and
	// is not handed to the structural parser.
	if len(command) <= MaxCommandBytes {
		parsed := shellsyntax.Parse(command)
		if hasUnresolvedPlaceholder(parsed) {
			addReason(Invalid, "Command contains an unresolved placeholder.")
		}
		for _, issue := range parsed.Issues {
			if issue.Kind == shellsyntax.Unsupported {
				addReason(Warning, "Command uses shell syntax outside clai's review subset: "+issue.Message+".")
				continue
			}

			switch issue.Code {
			case "empty-command":
				addReason(Invalid, "Command is empty.")
			case "trailing-operator":
				addReason(Invalid, "Command ends with an incomplete shell operator.")
			case "unclosed-single-quote", "unclosed-double-quote":
				addReason(Invalid, "Command contains an unclosed quote.")
			default:
				addReason(Invalid, "Command has invalid shell syntax: "+issue.Message+".")
			}
		}
	}

	return Result{Class: class, Valid: class != Invalid, Reasons: reasons}
}

func hasOtherControl(command string) bool {
	for _, r := range command {
		if unicode.Is(unicode.Cc, r) && r != '\r' && r != '\n' && r != '\x00' {
			return true
		}
	}
	return false
}

// hasUnresolvedPlaceholder builds a mask from parser-derived unquoted spans and
// redirect operators. Quoted, escaped, and dynamic text remains blank, so data
// such as '<tag>' cannot be mistaken for an executable placeholder.
func hasUnresolvedPlaceholder(parsed shellsyntax.Result) bool {
	visible := []byte(strings.Repeat(" ", len(parsed.Source)))
	markWord := func(word shellsyntax.Word) {
		for _, part := range word.Parts {
			if part.Kind == shellsyntax.UnquotedPart {
				copy(visible[part.Span.Start:part.Span.End], parsed.Source[part.Span.Start:part.Span.End])
			}
		}
	}

	for _, pipeline := range parsed.List.Pipelines {
		for _, command := range pipeline.Commands {
			for _, word := range command.Words {
				markWord(word)
			}
			for _, redirect := range command.Redirects {
				operatorEnd := redirect.Span.End
				if redirect.Target != nil {
					operatorEnd = redirect.Target.Span.Start
					markWord(*redirect.Target)
				}
				if redirect.Span.Start >= 0 && operatorEnd >= redirect.Span.Start && operatorEnd <= len(parsed.Source) {
					copy(visible[redirect.Span.Start:operatorEnd], parsed.Source[redirect.Span.Start:operatorEnd])
				}
			}
		}
	}
	return placeholderPattern.Match(visible)
}
