package validate

import (
	"regexp"
	"strings"
	"unicode"
)

var placeholderPattern = regexp.MustCompile(`<[^<>[:space:]][^<>]*>`)

const MaxCommandBytes = 8 * 1024

type Result struct {
	Valid   bool
	Reasons []string
}

func Command(command string) Result {
	trimmed := strings.TrimSpace(command)
	var reasons []string

	if trimmed == "" {
		reasons = append(reasons, "Command is empty.")
	}

	if strings.ContainsAny(command, "\r\n") {
		reasons = append(reasons, "Command must be a single record without CR or LF.")
	}

	if strings.ContainsRune(command, '\x00') {
		reasons = append(reasons, "Command contains a NUL byte.")
	}

	if hasOtherControl(command) {
		reasons = append(reasons, "Command contains a terminal control character.")
	}

	if len(command) > MaxCommandBytes {
		reasons = append(reasons, "Command exceeds the maximum size of 8192 bytes.")
	}

	if hasUnresolvedPlaceholder(trimmed) {
		reasons = append(reasons, "Command contains an unresolved placeholder.")
	}

	if hasUnclosedQuote(trimmed) {
		reasons = append(reasons, "Command contains an unclosed quote.")
	}

	if endsWithOperator(trimmed) {
		reasons = append(reasons, "Command ends with an incomplete shell operator.")
	}

	return Result{Valid: len(reasons) == 0, Reasons: reasons}
}

func hasOtherControl(command string) bool {
	for _, r := range command {
		if unicode.IsControl(r) && r != '\r' && r != '\n' && r != '\x00' {
			return true
		}
	}
	return false
}

func hasUnresolvedPlaceholder(command string) bool {
	var unquoted strings.Builder
	var quote rune
	escaped := false

	for _, r := range command {
		if escaped {
			escaped = false
			unquoted.WriteRune(' ')
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			unquoted.WriteRune(' ')
			continue
		}
		if r == '\'' || r == '"' {
			if quote == 0 {
				quote = r
			} else if quote == r {
				quote = 0
			}
			unquoted.WriteRune(' ')
			continue
		}
		if quote == 0 {
			unquoted.WriteRune(r)
		} else {
			unquoted.WriteRune(' ')
		}
	}

	return placeholderPattern.MatchString(unquoted.String())
}

func hasUnclosedQuote(command string) bool {
	var quote rune
	escaped := false

	for _, r := range command {
		if escaped {
			escaped = false
			continue
		}

		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}

		if r != '\'' && r != '"' {
			continue
		}

		if quote == 0 {
			quote = r
		} else if quote == r {
			quote = 0
		}
	}

	return quote != 0
}

func endsWithOperator(command string) bool {
	return strings.HasSuffix(command, "|") ||
		strings.HasSuffix(command, "&&") ||
		strings.HasSuffix(command, "||") ||
		strings.HasSuffix(command, ";")
}
