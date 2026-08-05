package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Vedaant-Rajoo/clai/internal/applicability"
	"github.com/Vedaant-Rajoo/clai/internal/capability"
	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
	tea "github.com/charmbracelet/bubbletea"
)

type stubProvider struct {
	candidates []provider.Candidate
	err        error
}

func (s stubProvider) Compile(context.Context, provider.Request) ([]provider.Candidate, error) {
	return s.candidates, s.err
}

type staticInventorySource struct {
	inventory capability.Inventory
}

func (source staticInventorySource) Inventory(context.Context) capability.Inventory {
	return source.inventory
}

type recordingProvider struct {
	request provider.Request
}

func (p *recordingProvider) Compile(_ context.Context, request provider.Request) ([]provider.Candidate, error) {
	p.request = request
	return []provider.Candidate{{Command: "git status", Explanation: "shows status"}}, nil
}

func submitIntent(t *testing.T, m Model, intent string) Model {
	t.Helper()
	m.input.SetValue(intent)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	if model.screen != screenLoading {
		t.Fatalf("screen after enter = %v, want loading", model.screen)
	}
	if cmd == nil {
		t.Fatal("enter returned nil compile command")
	}
	updated, _ = model.Update(cmd())
	model, ok = updated.(Model)
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

func TestOnlyCandidateZeroIsReviewedAndEvaluated(t *testing.T) {
	m := submitIntent(t, NewWithProvider(stubProvider{candidates: []provider.Candidate{
		{Command: "", Explanation: "invalid primary"},
		{Command: "pwd", Explanation: "valid alternate"},
	}}), "show directory")

	if m.command != "" || m.explanation != "invalid primary" {
		t.Fatalf("reviewed candidate = command %q explanation %q, want candidate zero", m.command, m.explanation)
	}
	if m.validation.Valid {
		t.Fatal("empty candidate zero was considered valid")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.(Model).Accepted() {
		t.Fatal("accepted alternate candidate without selecting or evaluating it")
	}
}

func TestProviderErrorDiscardsReturnedCandidates(t *testing.T) {
	m := submitIntent(t, NewWithProvider(stubProvider{
		candidates: []provider.Candidate{{Command: "pwd", Explanation: "must be ignored"}},
		err:        errors.New("boom"),
	}), "anything")
	if m.screen != screenInput || m.Command() != "" || m.Accepted() {
		t.Fatalf("error result exposed candidate: screen=%v command=%q accepted=%v", m.screen, m.Command(), m.Accepted())
	}
	if m.err == nil {
		t.Fatal("provider error was not retained")
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

func TestDeadlineExceededIsVisibleFailureWithNoExport(t *testing.T) {
	m := submitIntent(t, NewWithProvider(stubProvider{
		candidates: []provider.Candidate{{Command: "pwd", Explanation: "must be ignored"}},
		err:        context.DeadlineExceeded,
	}), "anything")
	if m.screen != screenInput || !errors.Is(m.err, context.DeadlineExceeded) {
		t.Fatalf("deadline result: screen=%v err=%v", m.screen, m.err)
	}
	if !strings.Contains(m.View(), "Error:") {
		t.Fatalf("deadline failure is not visible: %q", m.View())
	}
	if m.Accepted() || m.Command() != "" {
		t.Fatalf("deadline exposed command: accepted=%v command=%q", m.Accepted(), m.Command())
	}
}

func TestCancelledResultIsNotProviderFailure(t *testing.T) {
	m := submitIntent(t, NewWithProvider(stubProvider{err: context.Canceled}), "anything")
	if m.screen != screenInput {
		t.Fatalf("screen = %v, want input", m.screen)
	}
	if m.err != nil || strings.Contains(m.View(), "Error:") {
		t.Fatalf("cancellation rendered as provider failure: err=%v view=%q", m.err, m.View())
	}
	if m.Accepted() || m.Command() != "" {
		t.Fatalf("cancellation exposed command: accepted=%v command=%q", m.Accepted(), m.Command())
	}
}

func TestSubmitEmptyCandidatesShowsNonAcceptingNoSuggestion(t *testing.T) {
	m := submitIntent(t, NewWithProvider(stubProvider{}), "obscure thing")

	if m.screen != screenNoSuggestion {
		t.Fatalf("screen = %v, want no suggestion", m.screen)
	}
	if m.command != "" {
		t.Errorf("command = %q, want empty", m.command)
	}
	if m.Accepted() {
		t.Fatal("empty candidates set Accepted true")
	}
	if !strings.Contains(m.View(), "No suggestion") {
		t.Errorf("View() = %q, want no-suggestion state", m.View())
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if updated.(Model).screen != screenNoSuggestion {
		t.Fatal("no-suggestion state exposed command editing")
	}
	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.(Model).Accepted() {
		t.Fatal("retry action accepted an empty command")
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

func TestReviewViewEscapesControlAndProhibitedCharacters(t *testing.T) {
	m := New()
	m.screen = screenReview
	m.command = "safe 日本語\x1b\x01\t‮en"
	view := m.View()

	for _, want := range []string{`\u{001B}`, `\u{0001}`, `\t`, "U+202E"} {
		if !strings.Contains(view, want) {
			t.Errorf("review view missing visible escape %q in %q", want, view)
		}
	}
	for _, bad := range []rune{'\x1b', '\x01', 0x202E} {
		if strings.ContainsRune(view, bad) {
			t.Errorf("review view leaks raw rune U+%04X", bad)
		}
	}
	if !strings.Contains(view, "日本語") {
		t.Errorf("review view dropped benign multibyte text: %q", view)
	}
}

// prohibitedCommandFormat enumerates the exhaustive prohibited-command-format/v1
// set (REQ-SECURITY-006): U+061C, U+200B–U+200F, U+202A–U+202E, U+2060,
// U+2066–U+2069, and U+FEFF — seventeen code points in total.
func prohibitedCommandFormat() []rune {
	pts := []rune{0x061C}
	for r := rune(0x200B); r <= 0x200F; r++ {
		pts = append(pts, r)
	}
	for r := rune(0x202A); r <= 0x202E; r++ {
		pts = append(pts, r)
	}
	pts = append(pts, 0x2060)
	for r := rune(0x2066); r <= 0x2069; r++ {
		pts = append(pts, r)
	}
	pts = append(pts, 0xFEFF)
	return pts
}

// renderUntrustedField places payload into a single untrusted display field and
// returns the rendered view for that field's screen.
func renderUntrustedField(t *testing.T, field, payload string) string {
	t.Helper()
	m := New()
	switch field {
	case "command":
		m.screen = screenReview
		m.command = payload
	case "explanation":
		m.screen = screenReview
		m.explanation = payload
		// The Why section is collapsed by default; expand it so the untrusted
		// explanation is actually rendered for the sanitization assertion.
		m.whyExpanded = true
	case "intent":
		m.screen = screenReview
		m.intent = payload
	case "context":
		m.screen = screenReview
		m.context = machinecontext.Context{WorkingDirectory: payload}
		// The Context used section is collapsed by default; expand it so the
		// untrusted working directory is actually rendered.
		m.contextExpanded = true
	case "error":
		m.screen = screenInput
		m.err = errors.New(payload)
	default:
		t.Fatalf("unknown field %q", field)
	}
	return m.View()
}

// TestProhibitedCommandFormatRenderedVisiblyInAllUntrustedFields asserts every
// prohibited code point at the first, middle, and final positions of every
// untrusted display field renders as visible U+XXXX and never leaks the raw
// rune that could reorder or conceal adjacent text (REQ-SECURITY-007,
// REQ-ACCEPT-SAFETY-004, REQ-ACCEPT-SAFETY-006).
func TestProhibitedCommandFormatRenderedVisiblyInAllUntrustedFields(t *testing.T) {
	fields := []string{"command", "explanation", "intent", "context", "error"}
	positions := []struct {
		name  string
		build func(rune) string
	}{
		{"first", func(r rune) string { return string(r) + "ab" }},
		{"middle", func(r rune) string { return "a" + string(r) + "b" }},
		{"final", func(r rune) string { return "ab" + string(r) }},
	}
	for _, field := range fields {
		for _, r := range prohibitedCommandFormat() {
			for _, pos := range positions {
				view := renderUntrustedField(t, field, pos.build(r))
				visible := fmt.Sprintf("U+%04X", r)
				if !strings.Contains(view, visible) {
					t.Errorf("field=%s pos=%s U+%04X: view missing visible token %q", field, pos.name, r, visible)
				}
				if strings.ContainsRune(view, r) {
					t.Errorf("field=%s pos=%s U+%04X: view leaks raw prohibited rune", field, pos.name, r)
				}
			}
		}
	}
}

// TestProhibitedCommandFormatCommandCannotBeAcceptedOrEdited asserts a command
// carrying any prohibited code point is invalid, cannot be accepted, and when
// opened for editing exposes visible U+XXXX text rather than the raw rune
// (REQ-ACCEPT-SAFETY-005, REQ-SECURITY-007).
func TestProhibitedCommandFormatCommandCannotBeAcceptedOrEdited(t *testing.T) {
	for _, r := range prohibitedCommandFormat() {
		command := "echo " + string(r) + "value"
		m := submitIntent(t, NewWithProvider(stubProvider{candidates: []provider.Candidate{
			{Command: command, Explanation: "candidate"},
		}}), "intent")
		if m.validation.Valid {
			t.Fatalf("U+%04X: command with prohibited format reported valid", r)
		}

		accepted, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if accepted.(Model).Accepted() {
			t.Fatalf("U+%04X: accepted a prohibited-format command", r)
		}

		edited, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
		buffer := edited.(Model).commandInput.Value()
		if strings.ContainsRune(buffer, r) {
			t.Fatalf("U+%04X: edit buffer leaked raw prohibited rune: %q", r, buffer)
		}
		if !strings.Contains(buffer, fmt.Sprintf("U+%04X", r)) {
			t.Fatalf("U+%04X: edit buffer missing visible token: %q", r, buffer)
		}
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

func TestEditRoundTripsPlainCommandVerbatim(t *testing.T) {
	p := stubProvider{candidates: []provider.Candidate{{
		Command:     "ls -la",
		Explanation: "list files",
	}}}
	m := submitIntent(t, NewWithProvider(p), "list files")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	model := updated.(Model)
	if model.commandInput.Value() != "ls -la" {
		t.Fatalf("edit value = %q, want the raw command", model.commandInput.Value())
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.command != "ls -la" {
		t.Fatalf("command after unchanged edit = %q, want %q", model.command, "ls -la")
	}
}

type contextLifecycleProvider struct {
	watching chan struct{}
	done     chan struct{}
}

func (p contextLifecycleProvider) Compile(ctx context.Context, _ provider.Request) ([]provider.Candidate, error) {
	go func() {
		close(p.watching)
		<-ctx.Done()
		close(p.done)
	}()
	<-p.watching
	return []provider.Candidate{{Command: "pwd", Explanation: "current directory"}}, nil
}

func TestConsumingCompileResultClosesRequestContext(t *testing.T) {
	watching := make(chan struct{})
	done := make(chan struct{})
	m := NewWithProvider(contextLifecycleProvider{watching: watching, done: done})
	m.input.SetValue("where am i")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	result := cmd().(compileResult)

	select {
	case <-done:
		t.Fatal("request context closed before result was consumed")
	default:
	}

	updated, _ = m.Update(result)
	m = updated.(Model)
	if m.screen != screenReview {
		t.Fatalf("screen = %v, want review", m.screen)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request context did not close after result consumption")
	}
}

type cancellingProvider struct {
	started chan struct{}
}

func (p cancellingProvider) Compile(ctx context.Context, _ provider.Request) ([]provider.Candidate, error) {
	close(p.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestEscapeCancelsLoadingAndRestoresIntent(t *testing.T) {
	started := make(chan struct{})
	m := NewWithProvider(cancellingProvider{started: started})
	m.input.SetValue("show status")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.screen != screenInput {
		t.Fatalf("screen = %v, want input", m.screen)
	}
	if m.input.Value() != "show status" {
		t.Fatalf("input = %q, want restored intent", m.input.Value())
	}
	if m.err != nil {
		t.Fatalf("err = %v, cancellation is not provider failure", m.err)
	}

	var cancelled compileResult
	select {
	case msg := <-result:
		cancelled = msg.(compileResult)
	case <-time.After(time.Second):
		t.Fatal("provider did not observe cancellation")
	}
	updated, _ = m.Update(cancelled)
	m = updated.(Model)
	if m.screen != screenInput || m.err != nil {
		t.Fatalf("cancelled result mutated state: screen=%v err=%v", m.screen, m.err)
	}
}

func TestCtrlCCancelsLoadingAndQuitsWhileEscapeReturnsToInput(t *testing.T) {
	started := make(chan struct{})
	m := NewWithProvider(cancellingProvider{started: started})
	m.input.SetValue("show status")
	updated, compileCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	result := make(chan tea.Msg, 1)
	go func() { result <- compileCmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}

	updated, quitCmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(Model)
	if quitCmd == nil {
		t.Fatal("Ctrl-C did not return a quit command")
	}
	if _, ok := quitCmd().(tea.QuitMsg); !ok {
		t.Fatalf("Ctrl-C command returned %T, want tea.QuitMsg", quitCmd())
	}
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("Ctrl-C did not cancel provider context")
	}

	escapeModel := NewWithProvider(stubProvider{})
	escapeModel.input.SetValue("show status")
	updated, _ = escapeModel.Update(tea.KeyMsg{Type: tea.KeyEnter})
	escapeModel = updated.(Model)
	updated, escapeCmd := escapeModel.Update(tea.KeyMsg{Type: tea.KeyEsc})
	escapeModel = updated.(Model)
	if escapeCmd != nil || escapeModel.screen != screenInput {
		t.Fatalf("Escape: cmd nil=%v screen=%v, want nil/input", escapeCmd == nil, escapeModel.screen)
	}
}

type ignoresCancellationProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p ignoresCancellationProvider) Compile(context.Context, provider.Request) ([]provider.Candidate, error) {
	close(p.started)
	<-p.release
	return []provider.Candidate{{Command: "pwd", Explanation: "late result"}}, nil
}

func TestEscapeBeforeResultPreventsLateCandidateReviewOrExport(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	m := NewWithProvider(ignoresCancellationProvider{started: started, release: release})
	m.input.SetValue("where am i")
	updated, compileCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	result := make(chan tea.Msg, 1)
	go func() { result <- compileCmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	close(release)

	var late tea.Msg
	select {
	case late = <-result:
	case <-time.After(time.Second):
		t.Fatal("provider did not return late candidate")
	}
	updated, _ = m.Update(late)
	m = updated.(Model)
	if m.screen != screenInput || m.Command() != "" || m.Accepted() {
		t.Fatalf("late result entered review/export: screen=%v command=%q accepted=%v", m.screen, m.Command(), m.Accepted())
	}
}

func TestStaleResultFromSupersededRequestIsIgnored(t *testing.T) {
	m := NewWithProvider(stubProvider{})
	m.input.SetValue("request A")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	requestA := m.activeRequest

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	m.input.SetValue("request B")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	requestB := m.activeRequest

	updated, _ = m.Update(compileResult{
		requestID:  requestA,
		candidates: []provider.Candidate{{Command: "echo stale", Explanation: "stale"}},
	})
	m = updated.(Model)
	if m.screen != screenLoading || m.command != "" || m.activeRequest != requestB {
		t.Fatalf("stale result mutated current request: screen=%v command=%q active=%d", m.screen, m.command, m.activeRequest)
	}

	updated, _ = m.Update(compileResult{
		requestID:  requestB,
		candidates: []provider.Candidate{{Command: "pwd", Explanation: "current"}},
	})
	m = updated.(Model)
	if m.screen != screenReview || m.command != "pwd" {
		t.Fatalf("current result not applied: screen=%v command=%q", m.screen, m.command)
	}
}

func TestNoSuggestionBackAndRetry(t *testing.T) {
	m := submitIntent(t, NewWithProvider(stubProvider{}), "obscure thing")
	updated, retry := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = updated.(Model)
	if m.screen != screenLoading || retry == nil {
		t.Fatalf("retry: screen=%v cmd nil=%v", m.screen, retry == nil)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	m.screen = screenNoSuggestion
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	m = updated.(Model)
	if m.screen != screenInput || m.input.Value() != "obscure thing" {
		t.Fatalf("back: screen=%v input=%q", m.screen, m.input.Value())
	}
}

func TestProviderErrorRestoresIntentForEditing(t *testing.T) {
	p := stubProvider{err: errors.New("boom")}
	m := submitIntent(t, NewWithProvider(p), "anything")

	if m.screen != screenInput {
		t.Fatalf("screen = %v, want input", m.screen)
	}
	if m.input.Value() != "anything" {
		t.Fatalf("input = %q, want intent restored", m.input.Value())
	}
	if !strings.Contains(m.View(), "Error:") {
		t.Errorf("View() = %q, want the error visible alongside the input", m.View())
	}
}

func TestWindowResizePreservesReviewStateAndInformation(t *testing.T) {
	p := stubProvider{candidates: []provider.Candidate{
		{Command: "rm -rf /tmp/thing", Explanation: "removes the directory"},
	}}
	m := submitIntent(t, NewWithProvider(p), "delete the temp dir")
	if m.screen != screenReview {
		t.Fatalf("screen = %v, want review", m.screen)
	}

	for _, width := range []int{0, 20, 40, 4096} {
		updated, cmd := m.Update(tea.WindowSizeMsg{Width: width, Height: 10})
		resized, ok := updated.(Model)
		if !ok {
			t.Fatalf("Update returned %T, want Model", updated)
		}
		if cmd != nil {
			t.Fatalf("resize produced a command at width %d", width)
		}
		if resized.screen != screenReview {
			t.Fatalf("resize changed screen to %v at width %d", resized.screen, width)
		}
		if resized.accepted {
			t.Fatalf("resize flipped acceptance at width %d", width)
		}
		view := stripANSI(resized.View())
		for _, needle := range []string{"rm", "block", "enter blocked"} {
			if !strings.Contains(view, needle) {
				t.Fatalf("width %d hides %q:\n%s", width, needle, view)
			}
		}
		m = resized
	}
}

func TestResizeDuringLoadingKeepsRequestAlive(t *testing.T) {
	m := NewWithProvider(stubProvider{})
	m.input.SetValue("anything")
	updated, compileCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.screen != screenLoading || compileCmd == nil {
		t.Fatalf("setup: screen=%v cmd nil=%v", m.screen, compileCmd == nil)
	}
	request := m.activeRequest

	updated, _ = m.Update(tea.WindowSizeMsg{Width: 30, Height: 8})
	m = updated.(Model)
	if m.screen != screenLoading {
		t.Fatalf("resize left loading: screen=%v", m.screen)
	}
	if m.activeRequest != request {
		t.Fatalf("resize disturbed request identity: %d -> %d", request, m.activeRequest)
	}
}

// TestDecisionsAreDistinguishableWithoutColor proves REQ-PERFORMANCE-007:
// review, warning, blocking, and invalidity are conveyed by words, not only
// color, so a monochrome terminal shows the same decisions.
func TestDecisionsAreDistinguishableWithoutColor(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{name: "allow", command: "git status", want: []string{"allow", "valid", "enter accept"}},
		{name: "warn", command: "curl https://example.com", want: []string{"warn", "valid", "enter accept"}},
		{name: "block", command: "rm -rf /", want: []string{"block", "enter blocked"}},
		{name: "invalid", command: "echo <placeholder>", want: []string{"invalid", "enter invalid"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := stubProvider{candidates: []provider.Candidate{{Command: tc.command, Explanation: "x"}}}
			m := submitIntent(t, NewWithProvider(p), "intent")
			view := stripANSI(m.View())
			for _, needle := range tc.want {
				if !strings.Contains(view, needle) {
					t.Fatalf("monochrome view lacks %q:\n%s", needle, view)
				}
			}
		})
	}
}

// stripANSI removes CSI escape sequences so assertions see exactly what a
// colorless terminal presents.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// TestUntrustedReasonTextIsSanitizedInReviewView is the regression proof for
// review finding F-1: safety and validation reasons embed untrusted command
// tokens (for example the unrecognized executable name), so the rendered view
// must contain no raw terminal-control byte or prohibited format rune arriving
// through the provider -> compileResult -> review path.
func TestUntrustedReasonTextIsSanitizedInReviewView(t *testing.T) {
	command := "fo\x1b]0;pwn\x07o\u202Ebar arg"
	p := stubProvider{candidates: []provider.Candidate{{Command: command, Explanation: "x"}}}
	m := submitIntent(t, NewWithProvider(p), "intent")
	if m.screen != screenReview {
		t.Fatalf("screen = %v, want review", m.screen)
	}

	view := m.View()
	if strings.ContainsRune(view, '\u202E') {
		t.Fatalf("review view contains a raw U+202E from a gate reason:\n%q", view)
	}
	for i := 0; i < len(view); i++ {
		if view[i] == 0x1b && i+1 < len(view) && view[i+1] != '[' {
			t.Fatalf("review view contains a raw non-CSI escape byte at %d:\n%q", i, view)
		}
	}
	if !strings.Contains(view, "U+202E") && !strings.Contains(stripANSI(view), "202E") {
		t.Fatalf("prohibited rune is not rendered visibly:\n%q", stripANSI(view))
	}
}

// TestReviewCollapsesOptionalRowsUnderHeightBudget proves the review screen
// keeps the hero command, its validation/safety status, and the action hints on
// a terminal too short to hold everything, dropping the optional echoed intent
// first rather than clipping an essential row.
func TestReviewCollapsesOptionalRowsUnderHeightBudget(t *testing.T) {
	p := stubProvider{candidates: []provider.Candidate{
		{Command: "git status", Explanation: "reports the tree state"},
	}}
	m := submitIntent(t, NewWithProvider(p), "SENTINEL_INTENT")
	if m.screen != screenReview {
		t.Fatalf("screen = %v, want review", m.screen)
	}

	short, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 3})
	shortView := stripANSI(short.(Model).View())
	for _, needle := range []string{"git", "valid", "allow", "enter accept"} {
		if !strings.Contains(shortView, needle) {
			t.Fatalf("short review drops essential %q:\n%s", needle, shortView)
		}
	}
	if strings.Contains(shortView, "SENTINEL_INTENT") {
		t.Fatalf("short review kept the optional intent row instead of dropping it:\n%s", shortView)
	}

	tall, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	tallView := stripANSI(tall.(Model).View())
	if !strings.Contains(tallView, "SENTINEL_INTENT") {
		t.Fatalf("tall review hides the intent row that fits its budget:\n%s", tallView)
	}
}

// TestReviewTogglesWhyAndContext proves the Why and Context used sections are
// collapsed by default and are revealed — then hidden again — by the "?" and
// "c" toggles, keeping the default review minimal while the detail stays one
// keystroke away.
func TestReviewTogglesWhyAndContext(t *testing.T) {
	p := stubProvider{candidates: []provider.Candidate{
		{Command: "git status", Explanation: "BECAUSE_EXPLANATION"},
	}}
	m := submitIntent(t, NewWithProvider(p), "intent")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	m = updated.(Model)

	if v := stripANSI(m.View()); strings.Contains(v, "BECAUSE_EXPLANATION") || strings.Contains(v, "cwd:") {
		t.Fatalf("default review is not minimal:\n%s", v)
	}

	press := func(m Model, r rune) Model {
		t.Helper()
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		return next.(Model)
	}

	m = press(m, '?')
	if v := stripANSI(m.View()); !strings.Contains(v, "BECAUSE_EXPLANATION") {
		t.Fatalf("? did not reveal the explanation:\n%s", v)
	}
	m = press(m, '?')
	if v := stripANSI(m.View()); strings.Contains(v, "BECAUSE_EXPLANATION") {
		t.Fatalf("? did not collapse the explanation again:\n%s", v)
	}

	m = press(m, 'c')
	if v := stripANSI(m.View()); !strings.Contains(v, "cwd:") {
		t.Fatalf("c did not reveal the context:\n%s", v)
	}
	m = press(m, 'c')
	if v := stripANSI(m.View()); strings.Contains(v, "cwd:") {
		t.Fatalf("c did not collapse the context again:\n%s", v)
	}
}

// TestExpandedSectionCollapsesToHintWhenItCannotFit proves a section the user
// opened but the height budget cannot hold collapses to a one-line hint rather
// than clipping an essential row, so the user learns the detail is hidden and
// how to see it.
func TestExpandedSectionCollapsesToHintWhenItCannotFit(t *testing.T) {
	p := stubProvider{candidates: []provider.Candidate{
		{Command: "git status", Explanation: "BECAUSE_EXPLANATION"},
	}}
	m := submitIntent(t, NewWithProvider(p), "intent")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 6})
	m = updated.(Model)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = next.(Model)

	view := stripANSI(m.View())
	if strings.Contains(view, "BECAUSE_EXPLANATION") {
		t.Fatalf("explanation body was rendered despite the height budget:\n%s", view)
	}
	if !strings.Contains(view, "why hidden") {
		t.Fatalf("collapsed Why section did not fall back to a hint:\n%s", view)
	}
	for _, needle := range []string{"git", "enter accept"} {
		if !strings.Contains(view, needle) {
			t.Fatalf("budget fallback dropped essential %q:\n%s", needle, view)
		}
	}
}

// TestFrameBudgetPaddingMatchesBaseStyle guards the height/width budget math
// against a silent drift in baseStyle's Padding(1, 2). Every view subtracts
// frameVerticalPadding from the terminal height and wraps the row measurer at the
// width less frameHorizontalPadding; both constants mirror baseStyle's padding.
// If someone changes the frame padding without updating the budget, the views
// would over- or under-estimate their rendered height and could clip an
// essential row. Deriving the expected totals from baseStyle.GetPadding* keeps
// the constants and the frame in lockstep.
func TestFrameBudgetPaddingMatchesBaseStyle(t *testing.T) {
	top := baseStyle.GetPaddingTop()
	right := baseStyle.GetPaddingRight()
	bottom := baseStyle.GetPaddingBottom()
	left := baseStyle.GetPaddingLeft()

	if got, want := frameVerticalPadding, top+bottom; got != want {
		t.Fatalf("frameVerticalPadding = %d, but baseStyle top+bottom padding = %d; update the height budget to match baseStyle's Padding", got, want)
	}
	if got, want := frameHorizontalPadding, left+right; got != want {
		t.Fatalf("frameHorizontalPadding = %d, but baseStyle left+right padding = %d; update the row measurer to match baseStyle's Padding", got, want)
	}
}

// TestAnthropicTransportOnlyNoPartialCandidate is the app-layer anchor for
// AC-ANTHROPIC-TRANSPORT-ONLY (REQ-PERFORMANCE-012, REQ-NON-GOAL-009). Streaming
// is a transport detail: the model has no partial-delta message type, so while a
// request is in flight the UI stays on loading with no candidate, command,
// explanation, or review text, and only one complete candidate — delivered by a
// single compileResult — ever enters review. The anthropic-package test of the
// same name proves the transport half; these subtests prove the app half by
// composing the model's existing loading / interrupted / cancelled / superseded
// invariants.
func TestAnthropicTransportOnlyNoPartialCandidate(t *testing.T) {
	t.Run("LoadingWhileInFlightThenOneCompleteCandidate", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		m := NewWithProvider(ignoresCancellationProvider{started: started, release: release})
		m.input.SetValue("show directory")
		updated, compileCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)

		result := make(chan tea.Msg, 1)
		go func() { result <- compileCmd() }()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("provider did not start")
		}

		// Stream in flight: nothing partial has reached the UI or model state.
		if m.screen != screenLoading {
			t.Fatalf("screen = %v, want loading while the stream is consumed", m.screen)
		}
		if m.command != "" || m.explanation != "" || m.Accepted() {
			t.Fatalf("partial state mid-stream: command=%q explanation=%q accepted=%v", m.command, m.explanation, m.Accepted())
		}
		if strings.Contains(m.View(), "pwd") {
			t.Fatalf("loading view leaked candidate text: %q", m.View())
		}

		close(release)
		var complete tea.Msg
		select {
		case complete = <-result:
		case <-time.After(time.Second):
			t.Fatal("provider did not return a complete result")
		}
		updated, _ = m.Update(complete)
		m = updated.(Model)
		if m.screen != screenReview || m.command != "pwd" {
			t.Fatalf("one complete candidate did not enter review: screen=%v command=%q", m.screen, m.command)
		}
	})
	t.Run("CompleteCandidateEntersReview", TestSubmitUsesFirstCandidate)
	t.Run("InterruptedStreamErrorYieldsNoCandidate", TestProviderErrorDiscardsReturnedCandidates)
	t.Run("TruncatedDeadlineYieldsNoCandidate", TestDeadlineExceededIsVisibleFailureWithNoExport)
	t.Run("CancelledStreamExposesNoCandidate", TestCancelledResultIsNotProviderFailure)
	t.Run("LateResultAfterEscapeNotReviewedOrExported", TestEscapeBeforeResultPreventsLateCandidateReviewOrExport)
	t.Run("SupersededResultIgnored", TestStaleResultFromSupersededRequestIsIgnored)
	t.Run("ResizeDuringLoadingKeepsRequestAlive", TestResizeDuringLoadingKeepsRequestAlive)
}

func TestApplicabilityIndependentThirdGate(t *testing.T) {
	candidate := provider.Candidate{
		Command:     "pwd",
		Explanation: "current directory",
		Requirements: []capability.Requirement{{
			Kind: capability.RequirementShell,
			Name: "fish",
		}},
	}
	model := submitIntent(t, NewFromDeps(Deps{
		Provider:        stubProvider{candidates: []provider.Candidate{candidate}},
		InventorySource: staticInventorySource{},
	}), "where am i")
	if !model.validation.Valid || model.safety.Decision != "allow" {
		t.Fatalf("precondition gates = validation %+v safety %+v", model.validation, model.safety)
	}
	if model.applicability.Decision != applicability.Rejected {
		t.Fatalf("applicability = %+v, want hard rejection", model.applicability)
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.(Model).Accepted() {
		t.Fatal("hard applicability rejection was accepted")
	}
}

// TestDevEndpointLabelledOnCandidateScreens proves a loopback development
// session is visibly labelled with the effective endpoint before any color
// styling, on every screen that can display a candidate, and that a normal
// session carries no such label (REQ-DEVENDPOINT-005, REQ-ACCEPT-DEVENDPOINT-003).
func TestDevEndpointLabelledOnCandidateScreens(t *testing.T) {
	const endpoint = "http://127.0.0.1:8747"
	candidate := provider.Candidate{Command: "pwd", Explanation: "current directory"}
	deps := Deps{
		Provider:        stubProvider{candidates: []provider.Candidate{candidate}},
		InventorySource: staticInventorySource{},
		DevEndpoint:     endpoint,
	}
	model := submitIntent(t, NewFromDeps(deps), "where am i")
	for _, want := range []string{"DEV ENDPOINT", endpoint, "not the real provider"} {
		if view := model.View(); !strings.Contains(view, want) {
			t.Fatalf("review view missing %q: %q", want, view)
		}
	}

	edit, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if view := edit.(Model).View(); !strings.Contains(view, "DEV ENDPOINT") {
		t.Fatalf("edit view missing the development label: %q", view)
	}

	none := submitIntent(t, NewFromDeps(Deps{
		Provider:        stubProvider{},
		InventorySource: staticInventorySource{},
		DevEndpoint:     endpoint,
	}), "nothing matches")
	if none.screen != screenNoSuggestion {
		t.Fatalf("screen = %v, want no-suggestion", none.screen)
	}
	if view := none.View(); !strings.Contains(view, "DEV ENDPOINT") {
		t.Fatalf("no-suggestion view missing the development label: %q", view)
	}

	production := submitIntent(t, NewFromDeps(Deps{
		Provider:        stubProvider{candidates: []provider.Candidate{candidate}},
		InventorySource: staticInventorySource{},
	}), "where am i")
	if view := production.View(); strings.Contains(view, "DEV ENDPOINT") {
		t.Fatalf("production session was labelled as a development one: %q", view)
	}
}

// TestDevEndpointLabelSanitizedAndSurvivesNarrowTerminal proves the label is
// terminal-control sanitized and is not dropped when the review must collapse
// optional sections to fit.
func TestDevEndpointLabelSanitizedAndSurvivesNarrowTerminal(t *testing.T) {
	model := submitIntent(t, NewFromDeps(Deps{
		Provider:        stubProvider{candidates: []provider.Candidate{{Command: "pwd", Explanation: "why"}}},
		InventorySource: staticInventorySource{},
		DevEndpoint:     "http://127.0.0.1:8747/\x1b]52;c;payload\a",
	}), "where am i")

	view := model.View()
	if strings.ContainsRune(view, '\x1b') || strings.ContainsRune(view, '\a') {
		t.Fatalf("development label leaked terminal controls: %q", view)
	}

	resized, _ := model.Update(tea.WindowSizeMsg{Width: 60, Height: 8})
	if view := resized.(Model).View(); !strings.Contains(view, "DEV ENDPOINT") {
		t.Fatalf("development label was dropped on a short terminal: %q", view)
	}
}

func TestReviewCommunicatesApplicableInWords(t *testing.T) {
	candidate := provider.Candidate{Command: "pwd", Explanation: "current directory"}
	model := submitIntent(t, NewFromDeps(Deps{
		Provider:        stubProvider{candidates: []provider.Candidate{candidate}},
		InventorySource: staticInventorySource{},
	}), "where am i")
	if model.applicability.Decision != applicability.Applicable {
		t.Fatalf("applicability = %+v, want applicable", model.applicability)
	}
	if view := model.View(); !strings.Contains(view, "applicable") {
		t.Fatalf("review view does not communicate applicability in words: %q", view)
	}
}

func TestUnknownShellInventoryHardRejectsUnknownRequirement(t *testing.T) {
	candidate := provider.Candidate{
		Command:     "pwd",
		Explanation: "current directory",
		Requirements: []capability.Requirement{{
			Kind: capability.RequirementShell,
			Name: "unknown",
		}},
	}
	inventory := capability.NewFixtureInventory("unknown", "arm64", capability.ShellIdentity{Family: capability.ShellUnknown}, nil)
	model := submitIntent(t, NewFromDeps(Deps{
		Provider:        stubProvider{candidates: []provider.Candidate{candidate}},
		InventorySource: staticInventorySource{inventory: inventory},
	}), "where am i")
	if model.applicability.Decision != applicability.Rejected {
		t.Fatalf("applicability = %+v, want hard rejection for unknown machine shell", model.applicability)
	}
	if view := model.View(); !strings.Contains(view, "not for this shell/OS") {
		t.Fatalf("review view does not communicate hard rejection: %q", view)
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.(Model).Accepted() {
		t.Fatal("unknown-shell hard rejection was accepted")
	}
}

func TestApplicabilitySelectionAndWidgetBoundary(t *testing.T) {
	candidate := provider.Candidate{
		Command:     "pwd",
		Explanation: "current directory",
		Requirements: []capability.Requirement{{
			Kind: capability.RequirementTool,
			Name: "definitely_missing_clai_tool",
		}},
	}
	model := submitIntent(t, NewFromDeps(Deps{
		Provider:        stubProvider{candidates: []provider.Candidate{candidate}},
		InventorySource: staticInventorySource{},
	}), "where am i")
	if model.applicability.Decision != applicability.Marked {
		t.Fatalf("applicability = %+v, want soft mark", model.applicability)
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	accepted := updated.(Model)
	if !accepted.Accepted() {
		t.Fatal("soft-marked candidate was not accepted")
	}
	outcome := accepted.Outcome()
	if outcome.Edited || len(outcome.Requirements) != 1 || outcome.Requirements[0].Name != "definitely_missing_clai_tool" {
		t.Fatalf("outcome = %+v, want immutable unedited requirements", outcome)
	}
}

func TestCandidateExplanationAndApplicabilityReasonsSanitized(t *testing.T) {
	candidate := provider.Candidate{
		Command:     "pwd",
		Explanation: "why\x1b]52;c;payload\a",
		Requirements: []capability.Requirement{{
			Kind: capability.RequirementTool,
			Name: "bad\x1btool",
		}},
	}
	model := submitIntent(t, NewFromDeps(Deps{
		Provider:        stubProvider{candidates: []provider.Candidate{candidate}},
		InventorySource: staticInventorySource{},
	}), "review")
	model.whyExpanded = true
	view := model.View()
	if strings.ContainsRune(view, '\x1b') || strings.ContainsRune(view, '\a') {
		t.Fatalf("view leaked terminal controls: %q", view)
	}
	for _, want := range []string{`\u{001B}`, "may not work", "tool bad"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q: %q", want, view)
		}
	}
}

func TestEditedOutcomeDiscriminator(t *testing.T) {
	candidate := provider.Candidate{
		Command:     "pwd",
		Explanation: "current directory",
		Requirements: []capability.Requirement{{
			Kind: capability.RequirementOS,
			Name: "darwin",
		}},
	}
	model := submitIntent(t, NewFromDeps(Deps{
		Provider:        stubProvider{candidates: []provider.Candidate{candidate}},
		InventorySource: staticInventorySource{},
	}), "where am i")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	model = updated.(Model)
	model.commandInput.SetValue("pwd")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.applicability.Decision != applicability.Marked {
		t.Fatalf("edited applicability = %+v, want presence-only mark", model.applicability)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	outcome := updated.(Model).Outcome()
	if !outcome.Accepted || !outcome.Edited || len(outcome.Requirements) != 0 {
		t.Fatalf("edited outcome = %+v, want accepted edited command without model requirements", outcome)
	}
}

func TestEditReviewDerivesExecutablesFromBytes(t *testing.T) {
	candidate := provider.Candidate{
		Command:     "pwd",
		Explanation: "current directory",
		Requirements: []capability.Requirement{{
			Kind: capability.RequirementShell,
			Name: "fish",
		}},
	}
	model := submitIntent(t, NewFromDeps(Deps{
		Provider:        stubProvider{candidates: []provider.Candidate{candidate}},
		InventorySource: staticInventorySource{},
	}), "run commands")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	model = updated.(Model)
	model.commandInput.SetValue("env FOO=bar /usr/bin/rg TODO | command missing --flag")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.applicability.Decision != applicability.Marked {
		t.Fatalf("edited applicability = %+v, want marked", model.applicability)
	}
	if len(model.applicability.Reasons) != 2 || !strings.Contains(model.applicability.Reasons[0], "tool rg") || !strings.Contains(model.applicability.Reasons[1], "tool missing") {
		t.Fatalf("edited reasons = %v, want executable-position order", model.applicability.Reasons)
	}
	if !model.edited || len(model.Outcome().Requirements) != 0 {
		t.Fatalf("edited state retained model requirements: %+v", model.Outcome())
	}
}

func TestEditedParseUncertaintyDefersToValidation(t *testing.T) {
	model := submitIntent(t, NewFromDeps(Deps{
		Provider: stubProvider{candidates: []provider.Candidate{{
			Command:     "pwd",
			Explanation: "current directory",
		}}},
		InventorySource: staticInventorySource{},
	}), "run a command")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	model = updated.(Model)
	model.commandInput.SetValue("echo $(missing)")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.validation.Valid {
		t.Fatal("structural validation accepted parser uncertainty")
	}
	if model.applicability.Decision != applicability.Applicable || len(model.applicability.Reasons) != 0 {
		t.Fatalf("applicability asserted on parser uncertainty: %+v", model.applicability)
	}
}

type reviewInventory struct {
	os    string
	shell capability.ShellFamily
	tools map[string]capability.ToolFact
}

func (inventory reviewInventory) OSFamily() string { return inventory.os }
func (inventory reviewInventory) Shell() capability.ShellIdentity {
	return capability.ShellIdentity{Family: inventory.shell, Path: "/secret/shell"}
}
func (inventory reviewInventory) LookupTool(name string) (capability.ToolFact, bool) {
	fact, ok := inventory.tools[name]
	return fact, ok
}

func TestReviewDisplaysRelevantCapabilityFacts(t *testing.T) {
	candidate := provider.Candidate{
		Command:     "rg TODO",
		Explanation: "searches TODO markers",
		Requirements: []capability.Requirement{
			{Kind: capability.RequirementTool, Name: "rg", MinVersion: "14.1"},
			{Kind: capability.RequirementShell, Name: "bash"},
			{Kind: capability.RequirementOS, Name: "linux"},
			{Kind: capability.RequirementTool, Name: "rg"},
		},
	}
	model := submitIntent(t, NewFromDeps(Deps{
		Provider:        stubProvider{candidates: []provider.Candidate{candidate}},
		InventorySource: staticInventorySource{},
	}), "search TODO")
	model.inventory = reviewInventory{
		os:    "darwin",
		shell: capability.ShellFish,
		tools: map[string]capability.ToolFact{
			"rg":  {Name: "rg", Present: true, Path: "/secret/bin/rg", Version: "14.1.0"},
			"git": {Name: "git", Present: true, Path: "/secret/bin/git", Version: "2.50.0"},
		},
	}
	model.applicability = applicability.Evaluate(model.candidate.Requirements, model.inventory)
	model.context = machinecontext.Context{Shell: "fish", OS: "darwin"}
	model.contextExpanded = true

	view := model.View()
	for _, want := range []string{"tool rg: present 14.1.0", "shell: fish", "os: darwin"} {
		if !strings.Contains(view, want) {
			t.Fatalf("review missing relevant capability fact %q: %q", want, view)
		}
	}
	if strings.Count(view, "tool rg: present 14.1.0") != 1 {
		t.Fatalf("duplicate relevant tool fact in review: %q", view)
	}
	for _, forbidden := range []string{"tool git:", "/secret/bin/rg", "/secret/bin/git", "/secret/shell"} {
		if strings.Contains(view, forbidden) {
			t.Fatalf("review exposed irrelevant or path metadata %q: %q", forbidden, view)
		}
	}

	model.edited = true
	model.command = "command /usr/bin/rg TODO | missing"
	model.applicability = applicability.EvaluateEdited(model.command, model.inventory)
	edited := model.View()
	for _, want := range []string{"tool rg: present 14.1.0", "tool missing: absent"} {
		if !strings.Contains(edited, want) {
			t.Fatalf("edited review missing derived fact %q: %q", want, edited)
		}
	}
	// The command hero intentionally displays exact command bytes; capability
	// facts themselves must use only resolved base names.
	facts := relevantCapabilityLines(model.inventory, model.candidate.Requirements, true, model.applicability.Tools)
	if strings.Contains(strings.Join(facts, "\n"), "/usr/bin/rg") {
		t.Fatalf("edited capability facts exposed executable path: %v", facts)
	}

	// A literal brace pair is word data, so an edited command using the xargs
	// replacement idiom parses and its executable positions are derived rather
	// than skipped for parse uncertainty. Only genuine executable positions are
	// derived: rg is an argument to xargs here, not a command of its own.
	braced := relevantCapabilityLines(model.inventory, nil, true, applicability.EvaluateEdited("rg --files | xargs -I{} du -h {}", model.inventory).Tools)
	joined := strings.Join(braced, "\n")
	for _, want := range []string{"tool rg: present 14.1.0", "tool xargs: absent"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("edited brace command missing derived fact %q: %v", want, braced)
		}
	}
	if strings.Contains(joined, "{}") || strings.Contains(joined, "-I") {
		t.Fatalf("capability facts leaked brace or flag syntax: %v", braced)
	}
}
