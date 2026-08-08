package app

import (
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/Vedaant-Rajoo/clai/internal/textsafe"
	"github.com/Vedaant-Rajoo/clai/internal/validate"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// MaxIntentBytes is the byte ceiling for the user-authored provider request.
// It matches the command editor's 8 KiB ceiling but remains independently
// named so the product can lower the natural-language limit later.
const MaxIntentBytes = 8 * 1024

// clipboardPasteMsg is owned by app rather than Bubbles so clipboard data can
// be byte-bounded before it is ever handed to textinput.Model.
type clipboardPasteMsg struct {
	value string
	err   error
}

var readClipboard = clipboard.ReadAll

// boundActiveInput repairs a programmatically seeded oversize value before a
// key action can submit or further edit it. Production user input is bounded
// earlier by updateBoundedInput.
func (m *Model) boundActiveInput(input *textinput.Model, limit int) bool {
	value := input.Value()
	prefix, truncated := completeRunePrefix(value, limit)
	if !truncated {
		return false
	}
	input.SetValue(prefix)
	input.CursorEnd()
	m.notice = limitNotice(false, len(prefix), limit)
	return true
}

// updateBoundedInput applies the byte ceiling before textinput.Model sees user
// runes. This avoids a transient oversize widget value for normal typing,
// bracketed paste, and clipboard paste alike.
func updateBoundedInput(input textinput.Model, msg tea.Msg, limit int, notice string) (textinput.Model, tea.Cmd, string) {
	before := input.Value()

	switch msg := msg.(type) {
	case clipboardPasteMsg:
		if msg.err != nil {
			return input, nil, "Paste failed: " + textsafe.Visible(msg.err.Error())
		}
		runes, truncated := boundedSanitizedString(msg.value, limit-len(before))
		var cmd tea.Cmd
		if len(runes) > 0 {
			input, cmd = input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: runes, Paste: true})
		}
		if truncated {
			return input, cmd, limitNotice(true, len(input.Value()), limit)
		}
		if input.Value() != before {
			notice = ""
		}
		return input, cmd, notice

	case tea.KeyMsg:
		if msg.String() == "ctrl+v" {
			return input, func() tea.Msg {
				value, err := readClipboard()
				return clipboardPasteMsg{value: value, err: err}
			}, notice
		}
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
			runes, truncated := boundedSanitizedRunes(msg.Runes, limit-len(before))
			var cmd tea.Cmd
			if len(runes) > 0 {
				msg.Runes = runes
				input, cmd = input.Update(msg)
			}
			if truncated {
				return input, cmd, limitNotice(msg.Paste, len(input.Value()), limit)
			}
			if input.Value() != before {
				notice = ""
			}
			return input, cmd, notice
		}
	}

	updated, cmd := input.Update(msg)
	if updated.Value() != before {
		notice = ""
	}
	return updated, cmd, notice
}

func boundedSanitizedRunes(input []rune, available int) ([]rune, bool) {
	if available < 0 {
		available = 0
	}
	bounded := make([]rune, 0, min(len(input), available))
	used := 0
	for _, raw := range input {
		r, keep := sanitizedInputRune(raw)
		if !keep {
			continue
		}
		size := utf8.RuneLen(r)
		if used+size > available {
			return bounded, true
		}
		bounded = append(bounded, r)
		used += size
	}
	return bounded, false
}

func boundedSanitizedString(input string, available int) ([]rune, bool) {
	if available < 0 {
		available = 0
	}
	bounded := make([]rune, 0, min(len(input), available))
	used := 0
	for len(input) > 0 {
		raw, size := utf8.DecodeRuneInString(input)
		input = input[size:]
		r, keep := sanitizedInputRune(raw)
		if !keep {
			continue
		}
		runeBytes := utf8.RuneLen(r)
		if used+runeBytes > available {
			return bounded, true
		}
		bounded = append(bounded, r)
		used += runeBytes
	}
	return bounded, false
}

// sanitizedInputRune mirrors Bubbles textinput's single-line sanitizer: tabs
// and line endings become spaces, other controls and RuneError are discarded.
func sanitizedInputRune(r rune) (rune, bool) {
	switch {
	case r == utf8.RuneError:
		return 0, false
	case r == '\r' || r == '\n' || r == '\t':
		return ' ', true
	case unicode.IsControl(r):
		return 0, false
	default:
		return r, true
	}
}

func completeRunePrefix(value string, limit int) (string, bool) {
	if len(value) <= limit && utf8.ValidString(value) {
		return value, false
	}
	end := 0
	for end < len(value) {
		_, size := utf8.DecodeRuneInString(value[end:])
		if size == 1 && value[end] >= utf8.RuneSelf {
			break
		}
		if end+size > limit {
			break
		}
		end += size
	}
	return value[:end], true
}

func limitNotice(paste bool, used, limit int) string {
	if paste {
		return fmt.Sprintf("Paste truncated at the %d-byte input limit: %d/%d bytes retained.", limit, used, limit)
	}
	return fmt.Sprintf("Input limit reached: %d/%d bytes retained; additional text was not added.", used, limit)
}

func editableCommand(command string) (string, string) {
	if len(command) > validate.MaxCommandBytes {
		return "", fmt.Sprintf("Edit unavailable: Command exceeds the maximum size of %d bytes.", validate.MaxCommandBytes)
	}
	editable := textsafe.EditableCommand(command)
	if len(editable) > validate.MaxCommandBytes {
		return "", fmt.Sprintf("Edit unavailable: the command's safe editable representation exceeds the %d-byte limit.", validate.MaxCommandBytes)
	}
	return editable, ""
}
