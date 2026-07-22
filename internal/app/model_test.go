package app

import (
	"errors"
	"strings"
	"testing"

	"codeberg.org/newedia/clai/internal/provider"
	tea "github.com/charmbracelet/bubbletea"
)

type stubProvider struct {
	candidates []provider.Candidate
	err        error
}

func (s stubProvider) Compile(provider.Request) ([]provider.Candidate, error) {
	return s.candidates, s.err
}

type recordingProvider struct {
	request provider.Request
}

func (p *recordingProvider) Compile(request provider.Request) ([]provider.Candidate, error) {
	p.request = request
	return []provider.Candidate{{Command: "git status", Explanation: "shows status"}}, nil
}

func submitIntent(t *testing.T, m Model, intent string) Model {
	t.Helper()
	m.input.SetValue(intent)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	return model
}

func TestSubmitUsesActiveShellOverrideInProviderContext(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	p := &recordingProvider{}

	m := submitIntent(t, NewWithProviderAndShell(p, "fish"), "show repo status")

	if p.request.Context.Shell != "fish" {
		t.Fatalf("provider context shell = %q, want fish", p.request.Context.Shell)
	}
	if m.screen != screenReview {
		t.Fatalf("screen = %v, want review", m.screen)
	}
	if m.command != "git status" {
		t.Fatalf("command = %q, want git status", m.command)
	}
}

func TestSubmitRejectsUnsupportedActiveShellOverride(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	p := &recordingProvider{}

	submitIntent(t, NewWithProviderAndShell(p, "sh"), "show repo status")

	if p.request.Context.Shell != "/bin/zsh" {
		t.Fatalf("provider context shell = %q, want /bin/zsh fallback", p.request.Context.Shell)
	}
}

func TestSubmitUsesFirstCandidate(t *testing.T) {
	p := stubProvider{candidates: []provider.Candidate{
		{Command: "git status", Explanation: "shows status"},
		{Command: "git diff", Explanation: "alternate"},
	}}
	m := submitIntent(t, NewWithProvider(p), "show repo status")

	if m.screen != screenReview {
		t.Fatalf("screen = %v, want review", m.screen)
	}
	if m.command != "git status" {
		t.Errorf("command = %q, want %q", m.command, "git status")
	}
	if m.explanation != "shows status" {
		t.Errorf("explanation = %q, want %q", m.explanation, "shows status")
	}
	if m.err != nil {
		t.Errorf("err = %v, want nil", m.err)
	}
}

func TestSubmitProviderErrorSetsErr(t *testing.T) {
	p := stubProvider{err: errors.New("boom")}
	m := submitIntent(t, NewWithProvider(p), "anything")

	if m.err == nil {
		t.Fatal("err = nil, want error")
	}
	if !strings.Contains(m.View(), "Error:") {
		t.Errorf("View() = %q, want it to render the error", m.View())
	}
	if m.accepted {
		t.Error("accepted = true after provider error")
	}
	if m.screen == screenReview {
		t.Error("screen advanced to review after provider error")
	}
}

func TestSubmitEmptyCandidatesFallsBack(t *testing.T) {
	p := stubProvider{candidates: nil}
	m := submitIntent(t, NewWithProvider(p), "obscure thing")

	if m.screen != screenReview {
		t.Fatalf("screen = %v, want review", m.screen)
	}
	if m.command != `echo "No suggestion available yet"` {
		t.Errorf("command = %q, want no-suggestion fallback", m.command)
	}
	if m.err != nil {
		t.Errorf("err = %v, want nil", m.err)
	}
}

func TestAcceptGatedOnSafetyAndValidation(t *testing.T) {
	p := stubProvider{candidates: []provider.Candidate{
		{Command: "rm -rf /", Explanation: "dangerous"},
	}}
	m := submitIntent(t, NewWithProvider(p), "wipe everything")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model := updated.(Model)
	if model.accepted {
		t.Error("accepted a blocked/invalid command")
	}
}

func TestTerminalSafeEscapesControlCharacters(t *testing.T) {
	got := terminalSafe("safe 日本語\x1b\x01\tend")
	want := `safe 日本語\u{001B}\u{0001}\tend`
	if got != want {
		t.Fatalf("terminalSafe() = %q, want %q", got, want)
	}
}

func TestControlCharacterCandidateIsSafeToReviewAndEdit(t *testing.T) {
	p := stubProvider{candidates: []provider.Candidate{{
		Command:     "printf '\x1b]52;c;payload\a'",
		Explanation: "unsafe\x1b explanation",
	}}}
	m := submitIntent(t, NewWithProvider(p), "review")
	if m.validation.Valid {
		t.Fatal("control-character command was valid")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	model := updated.(Model)
	if strings.ContainsRune(model.commandInput.Value(), '\x1b') || strings.ContainsRune(model.commandInput.Value(), '\a') {
		t.Fatalf("edit value contains terminal controls: %q", model.commandInput.Value())
	}
}

func TestErrorViewRendersWhenErrSet(t *testing.T) {
	m := New()
	m.err = errors.New("forced failure")
	if !strings.Contains(m.View(), "Error: forced failure") {
		t.Errorf("View() = %q, want error rendered", m.View())
	}
}
