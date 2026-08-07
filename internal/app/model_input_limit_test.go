package app

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/Vedaant-Rajoo/clai/internal/capability"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
	"github.com/Vedaant-Rajoo/clai/internal/validate"
	tea "github.com/charmbracelet/bubbletea"
)

type boundedField struct {
	name  string
	limit int
}

var boundedFields = []boundedField{
	{name: "intent", limit: MaxIntentBytes},
	{name: "command", limit: validate.MaxCommandBytes},
}

func newBoundedFieldModel(field boundedField) Model {
	m := New()
	if field.name == "command" {
		m.input.Blur()
		m.commandInput.Focus()
		m.screen = screenEditCommand
	}
	return m
}

func boundedFieldValue(m Model, field boundedField) string {
	if field.name == "command" {
		return m.commandInput.Value()
	}
	return m.input.Value()
}

func setBoundedFieldValue(m *Model, field boundedField, value string) {
	if field.name == "command" {
		m.commandInput.SetValue(value)
		m.commandInput.CursorEnd()
		return
	}
	m.input.SetValue(value)
	m.input.CursorEnd()
}

func updateBoundedField(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(msg)
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	return next, cmd
}

func assertLimitNotice(t *testing.T, m Model, want string) {
	t.Helper()
	if !strings.Contains(m.notice, want) {
		t.Fatalf("notice = %q, want substring %q", m.notice, want)
	}
	if strings.IndexFunc(m.notice, unicode.IsControl) >= 0 {
		t.Fatalf("notice contains a terminal control character: %q", m.notice)
	}
	if !strings.Contains(stripANSI(m.View()), want) {
		t.Fatalf("view does not display limit notice %q", want)
	}
}

func TestEditableFieldsUseByteLimitBackstops(t *testing.T) {
	m := New()
	if m.input.CharLimit != MaxIntentBytes {
		t.Fatalf("intent CharLimit = %d, want %d", m.input.CharLimit, MaxIntentBytes)
	}
	if m.commandInput.CharLimit != validate.MaxCommandBytes {
		t.Fatalf("command CharLimit = %d, want %d", m.commandInput.CharLimit, validate.MaxCommandBytes)
	}
}

func TestEditableFieldNormalTypingByteLimits(t *testing.T) {
	for _, field := range boundedFields {
		field := field
		t.Run(field.name+"/ascii", func(t *testing.T) {
			m := newBoundedFieldModel(field)
			setBoundedFieldValue(&m, field, strings.Repeat("a", field.limit-1))

			m, _ = updateBoundedField(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
			if got := len(boundedFieldValue(m, field)); got != field.limit {
				t.Fatalf("exact-limit bytes = %d, want %d", got, field.limit)
			}
			m, _ = updateBoundedField(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
			if got := boundedFieldValue(m, field); len(got) != field.limit || got[len(got)-1] != 'b' {
				t.Fatalf("value changed after limit refusal: len=%d suffix=%q", len(got), got[len(got)-1:])
			}
			assertLimitNotice(t, m, "8192/8192 bytes retained")
		})

		t.Run(field.name+"/multibyte", func(t *testing.T) {
			m := newBoundedFieldModel(field)
			setBoundedFieldValue(&m, field, strings.Repeat("a", field.limit-3))

			m, _ = updateBoundedField(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'界'}})
			if got := boundedFieldValue(m, field); len(got) != field.limit || !utf8.ValidString(got) {
				t.Fatalf("exact multibyte value: len=%d valid=%v, want len=%d valid", len(got), utf8.ValidString(got), field.limit)
			}
			m, _ = updateBoundedField(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'界'}})
			if got := boundedFieldValue(m, field); len(got) != field.limit || !utf8.ValidString(got) {
				t.Fatalf("refused multibyte value: len=%d valid=%v, want len=%d valid", len(got), utf8.ValidString(got), field.limit)
			}
			assertLimitNotice(t, m, "8192/8192 bytes retained")
		})
	}
}

func TestEditableFieldBracketedPasteByteLimits(t *testing.T) {
	for _, field := range boundedFields {
		field := field
		for _, fixture := range []struct {
			name    string
			payload string
			wantLen int
		}{
			{name: "one-megabyte-ascii", payload: strings.Repeat("x", 1<<20), wantLen: field.limit},
			{name: "multibyte", payload: strings.Repeat("界", field.limit/3+100), wantLen: field.limit - field.limit%3},
		} {
			fixture := fixture
			t.Run(field.name+"/"+fixture.name, func(t *testing.T) {
				m := newBoundedFieldModel(field)
				m, _ = updateBoundedField(t, m, tea.KeyMsg{
					Type:  tea.KeyRunes,
					Runes: []rune(fixture.payload),
					Paste: true,
				})

				got := boundedFieldValue(m, field)
				if len(got) != fixture.wantLen || len(got) > field.limit || !utf8.ValidString(got) {
					t.Fatalf("bracketed paste: len=%d valid=%v, want len=%d and <=%d", len(got), utf8.ValidString(got), fixture.wantLen, field.limit)
				}
				assertLimitNotice(t, m, "Paste truncated")
			})
		}
	}
}

func TestEditableFieldClipboardPasteByteLimits(t *testing.T) {
	originalReadClipboard := readClipboard
	defer func() { readClipboard = originalReadClipboard }()

	for _, field := range boundedFields {
		field := field
		for _, fixture := range []struct {
			name    string
			payload string
			wantLen int
		}{
			{name: "one-megabyte-ascii", payload: strings.Repeat("x", 1<<20), wantLen: field.limit},
			{name: "multibyte", payload: strings.Repeat("界", field.limit/3+100), wantLen: field.limit - field.limit%3},
		} {
			fixture := fixture
			t.Run(field.name+"/"+fixture.name, func(t *testing.T) {
				readClipboard = func() (string, error) { return fixture.payload, nil }
				m := newBoundedFieldModel(field)
				m, cmd := updateBoundedField(t, m, tea.KeyMsg{Type: tea.KeyCtrlV})
				if cmd == nil {
					t.Fatal("ctrl+v returned no clipboard command")
				}
				m, _ = updateBoundedField(t, m, cmd())

				got := boundedFieldValue(m, field)
				if len(got) != fixture.wantLen || len(got) > field.limit || !utf8.ValidString(got) {
					t.Fatalf("clipboard paste: len=%d valid=%v, want len=%d and <=%d", len(got), utf8.ValidString(got), fixture.wantLen, field.limit)
				}
				assertLimitNotice(t, m, "Paste truncated")
			})
		}
	}
}

func TestProgrammaticOversizeIntentIsBoundedBeforeProvider(t *testing.T) {
	p := &recordingProvider{}
	m := NewFromDeps(Deps{
		Provider:        p,
		InventorySource: staticInventorySource{},
	})
	// CharLimit counts runes, so this white-box seed is still oversize in bytes.
	m.input.SetValue(strings.Repeat("界", MaxIntentBytes))

	m, cmd := updateBoundedField(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || m.screen != screenInput {
		t.Fatalf("first enter after oversize seed: cmd nil=%v screen=%v, want visible repair on input", cmd == nil, m.screen)
	}
	if got := m.input.Value(); len(got) > MaxIntentBytes || !utf8.ValidString(got) {
		t.Fatalf("repaired intent: len=%d valid=%v", len(got), utf8.ValidString(got))
	}
	assertLimitNotice(t, m, "Input limit reached")

	m, cmd = updateBoundedField(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || m.screen != screenLoading {
		t.Fatalf("second enter: cmd nil=%v screen=%v, want compile", cmd == nil, m.screen)
	}
	cmd()
	if len(p.request.Intent) > MaxIntentBytes || !utf8.ValidString(p.request.Intent) {
		t.Fatalf("provider intent: len=%d valid=%v, want <=%d valid", len(p.request.Intent), utf8.ValidString(p.request.Intent), MaxIntentBytes)
	}
}

func TestOversizeProviderCommandEditIsExplicitlyRefused(t *testing.T) {
	command := strings.Repeat("x", validate.MaxCommandBytes+1)
	m := submitIntent(t, NewWithProvider(stubProvider{candidates: []provider.Candidate{{
		Command:     command,
		Explanation: "oversize candidate",
	}}}), "intent")

	m, cmd := updateBoundedField(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if cmd != nil || m.screen != screenReview {
		t.Fatalf("oversize edit: cmd nil=%v screen=%v, want refused review", cmd == nil, m.screen)
	}
	if m.commandInput.Value() != "" {
		t.Fatalf("oversize provider command seeded edit buffer: len=%d", len(m.commandInput.Value()))
	}
	view := stripANSI(m.View())
	for _, want := range []string{
		"Edit unavailable: Command exceeds the maximum size of 8192 bytes.",
		"e edit unavailable",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("oversize edit view missing %q: %q", want, view)
		}
	}
}

func TestEditedCommandStaysBoundedAndValidatorRejectsExternalOversize(t *testing.T) {
	m := newBoundedFieldModel(boundedField{name: "command", limit: validate.MaxCommandBytes})
	m.inventory = reviewInventory{tools: map[string]capability.ToolFact{}}
	m, _ = updateBoundedField(t, m, tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune(strings.Repeat("x", 1<<20)),
		Paste: true,
	})
	m, _ = updateBoundedField(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenReview || len(m.command) != validate.MaxCommandBytes {
		t.Fatalf("saved edit: screen=%v bytes=%d, want review/%d", m.screen, len(m.command), validate.MaxCommandBytes)
	}

	external := validate.Command(strings.Repeat("x", validate.MaxCommandBytes+1))
	if external.Class != validate.Invalid || external.Valid {
		t.Fatalf("external oversize validation = %+v, want invalid", external)
	}
}
