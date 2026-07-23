package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	machinecontext "codeberg.org/newedia/clai/internal/context"
	"codeberg.org/newedia/clai/internal/provider"
	tea "github.com/charmbracelet/bubbletea"
)

type stubProvider struct {
	candidates []provider.Candidate
	err        error
}

func (s stubProvider) Compile(context.Context, provider.Request) ([]provider.Candidate, error) {
	return s.candidates, s.err
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
	case "intent":
		m.screen = screenReview
		m.intent = payload
	case "context":
		m.screen = screenReview
		m.context = machinecontext.Context{WorkingDirectory: payload}
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
