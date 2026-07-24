package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
	"github.com/Vedaant-Rajoo/clai/internal/provider/rules"
	"github.com/Vedaant-Rajoo/clai/internal/safety"
	"github.com/Vedaant-Rajoo/clai/internal/textsafe"
	"github.com/Vedaant-Rajoo/clai/internal/validate"
	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Screen int

const (
	screenInput Screen = iota
	screenLoading
	screenReview
	screenEditCommand
	screenNoSuggestion
)

var (
	baseStyle    = lipgloss.NewStyle().Padding(1, 2)
	headerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	commandStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Background(lipgloss.Color("235")).Padding(0, 1)
	mutedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	allowStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	blockStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

const (
	// frameVerticalPadding and frameHorizontalPadding mirror baseStyle's
	// Padding(1, 2): one row of padding above and below the frame, two columns to
	// its left and right. Every view's height budget subtracts the vertical
	// padding, and the row measurer wraps at the width less the horizontal
	// padding, so estimates match what the frame actually renders.
	// TestFrameBudgetPaddingMatchesBaseStyle fails if baseStyle's padding drifts
	// from these numbers, catching a change to one without the other.
	frameVerticalPadding   = 2
	frameHorizontalPadding = 4

	// compileTimeout bounds a single provider compile so a truly-hung call can no
	// longer pin the loading screen until the user presses esc. It is deliberately
	// generous: a legitimate slow network round-trip to a real provider must not
	// be cut off, so only a stuck call ever trips it.
	compileTimeout = 120 * time.Second
)

type Model struct {
	input         textinput.Model
	commandInput  textinput.Model
	spinner       spinner.Model
	provider      provider.Provider
	activeShell   string
	screen        Screen
	intent        string
	command       string
	explanation   string
	accepted      bool
	context       machinecontext.Context
	safety        safety.Result
	validation    validate.Result
	err           error
	nextRequest   uint64
	activeRequest uint64
	cancelCompile context.CancelFunc
	width         int
	height        int
	// whyExpanded and contextExpanded gate the two progressively disclosed
	// review sections. Both default to collapsed so the suggested command stays
	// the hero and the review fits short terminals; the user reveals them with
	// "?" and "c" respectively.
	whyExpanded     bool
	contextExpanded bool
}

func New() Model {
	return NewWithProviderAndShell(rules.Provider{}, "")
}

func NewWithProvider(p provider.Provider) Model {
	return NewWithProviderAndShell(p, "")
}

func NewWithProviderAndShell(p provider.Provider, shell string) Model {
	input := textinput.New()
	input.Placeholder = "Describe what you want to do..."
	configureCursor(&input)
	input.Focus()

	commandInput := textinput.New()
	commandInput.Placeholder = "Edit command..."
	commandInput.Prompt = "$ "
	configureCursor(&commandInput)

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	return Model{
		input:        input,
		commandInput: commandInput,
		spinner:      sp,
		provider:     p,
		activeShell:  shell,
		screen:       screenInput,
	}
}

// Init seeds the spinner's animation loop. The loop self-perpetuates through
// spinner.TickMsg (handled in Update) and the spinner is only rendered by
// loadingView, so it visibly animates while a compile is in flight and is
// idle-but-silent on every other screen. The tick is seeded here rather than
// batched into startCompile's command because the white-box tests invoke that
// command directly and require it to yield a compileResult.
func (m Model) Init() tea.Cmd {
	return m.spinner.Tick
}

// compileResult carries the outcome of an asynchronous Compile call back into
// the update loop.
type compileResult struct {
	requestID  uint64
	context    machinecontext.Context
	candidates []provider.Candidate
	err        error
}

// compile runs context collection and the provider off the UI goroutine so a
// slow network call never freezes the TUI.
func (m Model) compile(ctx context.Context, requestID uint64) tea.Cmd {
	intent := m.intent
	shell := m.activeShell
	p := m.provider
	return func() tea.Msg {
		collected := machinecontext.CollectWithShell(shell)
		if err := ctx.Err(); err != nil {
			return compileResult{requestID: requestID, context: collected, err: err}
		}
		candidates, err := p.Compile(ctx, provider.Request{Intent: intent, Context: collected})
		// If the client deadline fired, report it as the deadline error even when
		// a provider surfaces the timeout in its own vocabulary, so the loading
		// screen always resolves to the clear "compile timed out" failure.
		if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = context.DeadlineExceeded
		}
		return compileResult{requestID: requestID, context: collected, candidates: candidates, err: err}
	}
}

func (m *Model) startCompile() tea.Cmd {
	if m.cancelCompile != nil {
		m.cancelCompile()
	}
	m.nextRequest++
	m.activeRequest = m.nextRequest
	// A generous client deadline bounds a hung provider; cancel is still stored in
	// m.cancelCompile and invoked on the normal result path, on supersede, and on
	// esc/ctrl+c, so the timer is always released and the context never leaks.
	ctx, cancel := context.WithTimeout(context.Background(), compileTimeout)
	m.cancelCompile = cancel
	m.accepted = false
	m.command = ""
	m.explanation = ""
	m.err = nil
	// A new suggestion starts from the collapsed default so disclosure state
	// never leaks across intents.
	m.whyExpanded = false
	m.contextExpanded = false
	m.screen = screenLoading
	return m.compile(ctx, m.activeRequest)
}

func (m *Model) cancelActiveCompile() {
	if m.cancelCompile != nil {
		m.cancelCompile()
	}
	m.cancelCompile = nil
	m.activeRequest = 0
}

func configureCursor(input *textinput.Model) {
	input.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	input.Cursor.SetMode(cursor.CursorStatic)
}

func (m Model) Accepted() bool {
	return m.accepted
}

func (m Model) Command() string {
	return m.command
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Track the terminal size so views wrap long content instead of letting
		// the renderer truncate it, and so the review screen can budget its
		// height and drop optional rows before anything essential clips. Resize
		// never touches acceptance, screen, or request state.
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case spinner.TickMsg:
		// Advance the spinner and keep its loop alive. The frame only shows on the
		// loading screen, so this animates the compile indicator while other
		// screens ignore the still image.
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case compileResult:
		if msg.requestID == 0 || msg.requestID != m.activeRequest {
			return m, nil
		}
		if m.cancelCompile != nil {
			m.cancelCompile()
		}
		m.cancelCompile = nil
		m.activeRequest = 0
		m.context = msg.context
		if msg.err != nil {
			m.command = ""
			m.explanation = ""
			m.accepted = false
			m.screen = screenInput
			m.input.SetValue(m.intent)
			m.input.CursorEnd()
			m.input.Focus()
			if !errors.Is(msg.err, context.Canceled) {
				m.err = msg.err
				if errors.Is(msg.err, context.DeadlineExceeded) {
					// Keep the deadline in the error chain (errors.Is still matches)
					// while giving the user a plain-language reason on the input screen.
					m.err = fmt.Errorf("compile timed out after %s: %w", compileTimeout, msg.err)
				}
			}
			return m, nil
		}
		if len(msg.candidates) == 0 {
			m.command = ""
			m.explanation = ""
			m.accepted = false
			m.err = nil
			m.screen = screenNoSuggestion
			return m, nil
		}
		m.command = msg.candidates[0].Command
		m.explanation = msg.candidates[0].Explanation
		m.safety = safety.Evaluate(m.command)
		m.validation = validate.Command(m.command)
		m.screen = screenReview
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.cancelActiveCompile()
			return m, tea.Quit
		case "esc":
			if m.screen == screenEditCommand {
				m.commandInput.Blur()
				m.screen = screenReview
				return m, nil
			}
			if m.screen == screenLoading {
				m.cancelActiveCompile()
				m.command = ""
				m.explanation = ""
				m.accepted = false
				m.err = nil
				m.input.SetValue(m.intent)
				m.input.CursorEnd()
				m.input.Focus()
				m.screen = screenInput
				return m, nil
			}

			return m, tea.Quit
		case "enter":
			if m.screen == screenInput {
				m.intent = m.input.Value()
				return m, m.startCompile()
			}

			if m.screen == screenNoSuggestion {
				return m, m.startCompile()
			}

			if m.screen == screenReview {
				if m.safety.Decision == safety.Block || !m.validation.Valid {
					return m, nil
				}
				m.accepted = true
				return m, tea.Quit
			}

			if m.screen == screenEditCommand {
				m.command = m.commandInput.Value()
				m.safety = safety.Evaluate(m.command)
				m.validation = validate.Command(m.command)
				m.commandInput.Blur()
				m.screen = screenReview
				return m, nil
			}
		case "b":
			if m.screen == screenReview || m.screen == screenNoSuggestion {
				m.command = ""
				m.explanation = ""
				m.accepted = false
				m.input.SetValue(m.intent)
				m.input.CursorEnd()
				m.input.Focus()
				m.screen = screenInput
				return m, nil
			}
		case "r":
			if m.screen == screenNoSuggestion {
				return m, m.startCompile()
			}
		case "e":
			if m.screen == screenReview {
				// Render prohibited command-format code points as visible U+XXXX
				// text and drop terminal control characters. The transformation is
				// one-way: the edit buffer can never reconstruct the original
				// prohibited rune, and validation rejects control characters.
				m.commandInput.SetValue(textsafe.EditableCommand(m.command))
				cmd := m.commandInput.Focus()
				m.screen = screenEditCommand
				return m, cmd
			}
		case "?":
			if m.screen == screenReview {
				m.whyExpanded = !m.whyExpanded
				return m, nil
			}
		case "c":
			if m.screen == screenReview {
				m.contextExpanded = !m.contextExpanded
				return m, nil
			}
		}
	}

	if m.screen == screenInput {
		m.input, cmd = m.input.Update(msg)
	} else if m.screen == screenEditCommand {
		m.commandInput, cmd = m.commandInput.Update(msg)
	}

	return m, cmd
}

// frame renders the outer padded frame, constrained to the last reported
// terminal width so long untrusted content wraps instead of being truncated
// by the renderer. Review information must stay visible on narrow terminals.
func (m Model) frame(content string) string {
	style := baseStyle
	if m.width > 0 {
		style = style.Width(m.width)
	}
	return style.Render(content)
}

// budgetHeight reports how many terminal rows the frame's content area can use:
// the reported terminal height less the frame's vertical padding, floored at a
// single row. A non-positive terminal height means the size is unknown and
// returns -1 so callers keep every row. reviewView and fitSections share this so
// the height budget is defined in exactly one place.
func (m Model) budgetHeight() int {
	if m.height <= 0 {
		return -1
	}
	budget := m.height - frameVerticalPadding
	if budget < 1 {
		budget = 1
	}
	return budget
}

// fitSections stacks essential view sections inside the frame, separating them
// with a blank line while the height budget can afford the extra rows and
// collapsing to single-line spacing when the terminal is too short. Every
// section is content the screen must show — the prompt/hero, its status, and the
// action hints — so only the blank-line decoration between them is dropped, and
// never a line itself. Behavior is identical on a tall terminal (or when the
// size is unknown), where the roomy blank-line layout always fits.
func (m Model) fitSections(sections []string) string {
	sep := "\n\n"
	if budget := m.budgetHeight(); budget >= 0 {
		measure := m.rowMeasurer()
		content := 0
		for _, s := range sections {
			content += measure(s)
		}
		// Blank-line spacing adds one row between each pair of sections; drop it
		// when the roomy layout would overflow the budget.
		if content+len(sections)-1 > budget {
			sep = "\n"
		}
	}
	return m.frame(strings.Join(sections, sep))
}

func (m Model) View() string {
	switch m.screen {
	case screenInput:
		return m.inputView()
	case screenLoading:
		return m.loadingView()
	case screenReview:
		return m.reviewView()
	case screenEditCommand:
		return m.editCommandView()
	case screenNoSuggestion:
		return m.noSuggestionView()
	}
	return ""
}

func (m Model) inputView() string {
	sections := []string{
		headerStyle.Render("What do you want to do?"),
		m.input.View(),
	}
	if m.err != nil {
		sections = append(sections, blockStyle.Render("Error: "+textsafe.Visible(m.err.Error())))
	}
	sections = append(sections, mutedStyle.Render("enter submit · esc quit"))
	return m.fitSections(sections)
}

func (m Model) loadingView() string {
	return m.fitSections([]string{
		headerStyle.Render("What do you want to do?"),
		textsafe.Visible(m.intent),
		m.spinner.View() + mutedStyle.Render("Compiling suggestion..."),
	})
}

// reviewView renders the minimal, height-aware review screen. Three essential
// rows — the hero command, the one-line validation/safety status, and the action
// hints — are always kept so the decision and the way to act on it can never be
// clipped away. Optional rows are added in keep-priority order only while they
// fit the terminal's height budget; a toggled section that cannot fit collapses
// to a one-line hint rather than pushing an essential row off a short screen.
func (m Model) reviewView() string {
	command := commandStyle.Render(textsafe.Visible(m.command))
	status := statusLine(m.validation, m.safety)
	actions := mutedStyle.Render(reviewActions(m.safety.Decision, m.validation.Valid, m.whyExpanded, m.contextExpanded))

	reasons := reviewReasons(m.validation, m.safety)
	why := ""
	if m.whyExpanded {
		why = section("Why", textsafe.Visible(m.explanation))
	}
	usedContext := ""
	if m.contextExpanded {
		usedContext = section("Context used", strings.Join(contextLines(m.context), "\n"))
	}
	intent := mutedStyle.Render("intent: " + textsafe.Visible(m.intent))

	measure := m.rowMeasurer()

	// A non-positive height means the size is unknown (budget < 0 keeps every
	// row); otherwise budgetHeight reserves the frame's vertical padding and never
	// drops below a single content row.
	budget := m.budgetHeight()

	used := measure(command) + measure(status) + measure(actions)

	selected := map[string]string{}
	consider := func(key, body, hint string) {
		if body == "" {
			return
		}
		if budget < 0 || used+measure(body) <= budget {
			selected[key] = body
			used += measure(body)
			return
		}
		if hint != "" && used+measure(hint) <= budget {
			selected[key] = hint
			used += measure(hint)
		}
	}

	// Keep priority, highest first: the reason a command is blocked or invalid
	// outranks the optional explanation and context, and the echoed intent is the
	// first thing to drop on a cramped screen.
	consider("reasons", reasons, "")
	consider("why", why, mutedStyle.Render("why hidden — resize or collapse to view"))
	consider("context", usedContext, mutedStyle.Render("context hidden — resize or collapse to view"))
	consider("intent", intent, "")

	rows := []string{command, status}
	for _, key := range []string{"reasons", "why", "context", "intent"} {
		if body, ok := selected[key]; ok {
			rows = append(rows, body)
		}
	}
	rows = append(rows, actions)

	return m.frame(strings.Join(rows, "\n"))
}

// rowMeasurer returns a function reporting how many terminal rows a rendered
// block occupies once wrapped to the review frame's inner width. Wrapping at the
// inner width (never wider) keeps the height estimate conservative, so the
// budget errs toward dropping an optional row rather than clipping an essential
// one. An empty block occupies no rows.
func (m Model) rowMeasurer() func(string) int {
	inner := 0
	if m.width > frameHorizontalPadding {
		inner = m.width - frameHorizontalPadding
	}
	return func(block string) int {
		if block == "" {
			return 0
		}
		if inner > 0 {
			block = lipgloss.NewStyle().Width(inner).Render(block)
		}
		return lipgloss.Height(block)
	}
}

func (m Model) editCommandView() string {
	return m.fitSections([]string{
		headerStyle.Render("Edit command"),
		m.commandInput.View(),
		mutedStyle.Render("enter save · esc discard"),
	})
}

func (m Model) noSuggestionView() string {
	return m.fitSections([]string{
		headerStyle.Render("No suggestion"),
		section("Intent", textsafe.Visible(m.intent)),
		"The provider returned no command candidate.",
		mutedStyle.Render("enter/r retry · b back · esc cancel"),
	})
}

func section(title, body string) string {
	return headerStyle.Render(title) + "\n" + body
}

func contextLines(c machinecontext.Context) []string {
	gitRepo := "no"
	if c.GitRepository {
		gitRepo = "yes"
	}

	branch := c.GitBranch
	if branch == "" {
		branch = "none"
	}

	return []string{
		"cwd: " + textsafe.Visible(c.WorkingDirectory),
		"shell: " + textsafe.Visible(c.Shell),
		"os: " + textsafe.Visible(c.OS),
		"git repo: " + gitRepo,
		"git root: " + textsafe.Visible(contextValue(c.GitRoot, "none")),
		"git branch: " + textsafe.Visible(branch),
	}
}

func contextValue(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

// statusLine collapses validation and safety into one row. Both decisions are
// named in words — valid/invalid and allow/warn/block — so the review stays
// unambiguous on a monochrome terminal; color only reinforces the words.
func statusLine(validation validate.Result, result safety.Result) string {
	validity := allowStyle.Render("valid")
	if !validation.Valid {
		validity = blockStyle.Render("invalid")
	}
	decision := decisionStyle(result.Decision).Render(string(result.Decision))
	return validity + mutedStyle.Render(" · ") + decision
}

// reviewReasons renders the sanitized reasons behind a non-allow or invalid
// decision, validation reasons first. It returns an empty string when the
// command is both valid and allowed, keeping the default review screen minimal.
func reviewReasons(validation validate.Result, result safety.Result) string {
	var reasons []string
	if !validation.Valid {
		reasons = append(reasons, validation.Reasons...)
	}
	if result.Decision != safety.Allow {
		reasons = append(reasons, result.Reasons...)
	}
	if len(reasons) == 0 {
		return ""
	}
	return visibleReasons(reasons)
}

// visibleReasons sanitizes gate reasons before rendering. Reasons embed
// untrusted command tokens (for example the unrecognized executable name), so
// they get the same one-way visible transformation as every other untrusted
// field on the review screen.
func visibleReasons(reasons []string) string {
	visible := make([]string, len(reasons))
	for i, reason := range reasons {
		visible[i] = textsafe.Visible(reason)
	}
	return strings.Join(visible, "\n")
}

func decisionStyle(decision safety.Decision) lipgloss.Style {
	switch decision {
	case safety.Allow:
		return allowStyle
	case safety.Warn:
		return warnStyle
	case safety.Block:
		return blockStyle
	default:
		return mutedStyle
	}
}

// reviewActions builds the single action hint line. The leading "enter <verb>"
// token names the accept outcome in words (accept/blocked/invalid) so the
// consequence of pressing enter is legible without color, and the "?"/"c" hints
// reflect whether each disclosure section is currently open.
func reviewActions(decision safety.Decision, valid, whyExpanded, contextExpanded bool) string {
	verb := "accept"
	switch {
	case !valid:
		verb = "invalid"
	case decision == safety.Block:
		verb = "blocked"
	}

	why := "? why"
	if whyExpanded {
		why = "? hide why"
	}

	usedContext := "c context"
	if contextExpanded {
		usedContext = "c hide context"
	}

	return strings.Join([]string{
		"enter " + verb,
		"e edit",
		why,
		usedContext,
		"b back",
		"esc cancel",
	}, " · ")
}
