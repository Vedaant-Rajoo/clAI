package validate

import (
	"regexp"
	"strings"
	"unicode"

	"codeberg.org/newedia/clai/internal/shellsyntax"
	"codeberg.org/newedia/clai/internal/textsafe"
)

var placeholderPattern = regexp.MustCompile(`<[^<>[:space:]][^<>]*>`)

const MaxCommandBytes = 8 * 1024

type Result struct {
	Valid   bool
	Reasons []string
}

// Command validates whether command belongs to the bounded common shell subset
// eligible for review and export. It rejects malformed and unsupported syntax;
// it never repairs, strips, expands, or evaluates command text.
func Command(command string) Result {
	var reasons []string
	addReason := func(reason string) {
		for _, existing := range reasons {
			if existing == reason {
				return
			}
		}
		reasons = append(reasons, reason)
	}

	if strings.TrimSpace(command) == "" {
		addReason("Command is empty.")
	}
	if strings.ContainsAny(command, "\r\n") {
		addReason("Command must be a single record without CR or LF.")
	}
	if strings.ContainsRune(command, '\x00') {
		addReason("Command contains a NUL byte.")
	}
	if hasOtherControl(command) {
		addReason("Command contains a terminal control character.")
	}
	if textsafe.ContainsProhibitedCommandFormat(command) {
		addReason("Command contains a prohibited invisible or bidirectional format character.")
	}
	if len(command) > MaxCommandBytes {
		addReason("Command exceeds the maximum size of 8192 bytes.")
	}

	// Parsing is deliberately bounded. Oversize input is already ineligible and
	// is not handed to the structural parser.
	if len(command) <= MaxCommandBytes {
		parsed := shellsyntax.Parse(command)
		if hasUnresolvedPlaceholder(parsed) {
			addReason("Command contains an unresolved placeholder.")
		}
		for _, issue := range parsed.Issues {
			switch issue.Code {
			case "empty-command":
				addReason("Command is empty.")
			case "trailing-operator":
				addReason("Command ends with an incomplete shell operator.")
			case "unclosed-single-quote", "unclosed-double-quote":
				addReason("Command contains an unclosed quote.")
			default:
				addReason("Command has invalid or unsupported shell syntax: " + issue.Message + ".")
			}
		}
	}

	return Result{Valid: len(reasons) == 0, Reasons: reasons}
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
