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

func TestErrorViewRendersWhenErrSet(t *testing.T) {
	m := New()
	m.err = errors.New("forced failure")
	if !strings.Contains(m.View(), "Error: forced failure") {
		t.Errorf("View() = %q, want error rendered", m.View())
	}
}
