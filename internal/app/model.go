package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Vedaant-Rajoo/clai/internal/applicability"
	"github.com/Vedaant-Rajoo/clai/internal/capability"
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

type InventorySource interface {
	Inventory(context.Context) capability.Inventory
}

type Deps struct {
	Provider        provider.Provider
	ActiveShell     string
	InventorySource InventorySource
	// DevEndpoint is the loopback development endpoint in use, or empty for a
	// production session. When set, every candidate-bearing screen is labelled
	// so a stubbed session cannot be mistaken for a real one (REQ-DEVENDPOINT-005).
	DevEndpoint string
}

type Outcome struct {
	Command      string
	Accepted     bool
	Requirements []capability.Requirement
	Edited       bool
}

type Model struct {
	input           textinput.Model
	commandInput    textinput.Model
	spinner         spinner.Model
	provider        provider.Provider
	inventorySource InventorySource
	activeShell     string
	devEndpoint     string
	screen          Screen
	intent          string
	candidate       provider.Candidate
	command         string
	explanation     string
	accepted        bool
	edited          bool
	context         machinecontext.Context
	inventory       applicability.Inventory
	applicability   applicability.Result
	safety          safety.Result
	validation      validate.Result
	err             error
	nextRequest     uint64
	activeRequest   uint64
	cancelCompile   context.CancelFunc
	width           int
	height          int
	// whyExpanded and contextExpanded gate the two progressively disclosed
	// review sections. Both default to collapsed so the suggested command stays
	// the hero and the review fits short terminals; the user reveals them with
	// "?" and "c" respectively.
	whyExpanded     bool
	contextExpanded bool
	// notice is a terminal-safe-on-render explanation of the most recent input
	// refusal. Limit notices contain only fixed application text and counters;
	// other errors are passed through textsafe.Visible before display.
	notice string
}

func New() Model {
	return NewWithProviderAndShell(rules.Provider{}, "")
}

func NewWithProvider(p provider.Provider) Model {
	return NewWithProviderAndShell(p, "")
}

func NewWithProviderAndShell(p provider.Provider, shell string) Model {
	return NewFromDeps(Deps{
		Provider:        p,
		ActiveShell:     shell,
		InventorySource: capability.NewCached(shell),
	})
}

func NewFromDeps(deps Deps) Model {
	input := textinput.New()
	input.Placeholder = "Describe what you want to do..."
	// CharLimit is an additional rune-count backstop. updateBoundedInput applies
	// the authoritative byte-aware limit before every user insertion.
	input.CharLimit = MaxIntentBytes
	configureCursor(&input)
	input.Focus()

	commandInput := textinput.New()
	commandInput.Placeholder = "Edit command..."
	commandInput.Prompt = "$ "
	commandInput.CharLimit = validate.MaxCommandBytes
	configureCursor(&commandInput)

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	if deps.InventorySource == nil {
		deps.InventorySource = capability.NewCached(deps.ActiveShell)
	}
	return Model{
		input:           input,
		commandInput:    commandInput,
		spinner:         sp,
		provider:        deps.Provider,
		inventorySource: deps.InventorySource,
		activeShell:     deps.ActiveShell,
		devEndpoint:     deps.DevEndpoint,
		screen:          screenInput,
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
	inventory  capability.Inventory
	candidates []provider.Candidate
	err        error
}

// compile runs context collection and the provider off the UI goroutine so a
// slow network call never freezes the TUI.
func (m Model) compile(ctx context.Context, requestID uint64) tea.Cmd {
	// The input event path already enforces this bound. Reapply it at the
	// provider boundary as defense in depth for programmatically constructed
	// models and future callers.
	intent, _ := completeRunePrefix(m.intent, MaxIntentBytes)
	shell := m.activeShell
	p := m.provider
	inventorySource := m.inventorySource
	return func() tea.Msg {
		if err := ctx.Err(); err != nil {
			return compileResult{requestID: requestID, err: err}
		}

		collected := machinecontext.CollectContext(ctx, shell)
		if err := ctx.Err(); err != nil {
			return compileResult{requestID: requestID, context: collected, err: err}
		}

		inventory := inventorySource.Inventory(ctx)
		if err := ctx.Err(); err != nil {
			return compileResult{requestID: requestID, context: collected, inventory: inventory, err: err}
		}
		candidates, err := p.Compile(ctx, provider.Request{Intent: intent, Context: collected, Capabilities: inventory})
		// If the client deadline fired, report it as the deadline error even when
		// a provider surfaces the timeout in its own vocabulary, so the loading
		// screen always resolves to the clear "compile timed out" failure.
		if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = context.DeadlineExceeded
		}
		return compileResult{requestID: requestID, context: collected, inventory: inventory, candidates: candidates, err: err}
	}
}

func (m *Model) startCompile() tea.Cmd {
	if m.cancelCompile != nil {
		m.cancelCompile()
	}
	m.nextRequest++
	m.activeRequest = m.nextRequest
	m.intent, _ = completeRunePrefix(m.intent, MaxIntentBytes)
	// A generous client deadline bounds a hung provider; cancel is still stored in
	// m.cancelCompile and invoked on the normal result path, on supersede, and on
	// esc/ctrl+c, so the timer is always released and the context never leaks.
	ctx, cancel := context.WithTimeout(context.Background(), compileTimeout)
	m.cancelCompile = cancel
	m.accepted = false
	m.edited = false
	m.candidate = provider.Candidate{}
	m.command = ""
	m.explanation = ""
	m.applicability = applicability.Result{}
	m.err = nil
	m.notice = ""
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

func (m *Model) returnToInput() {
	m.candidate = provider.Candidate{}
	m.command = ""
	m.explanation = ""
	m.accepted = false
	m.edited = false
	m.applicability = applicability.Result{}
	m.notice = ""
	m.input.SetValue(m.intent)
	m.input.CursorEnd()
	m.input.Focus()
	m.screen = screenInput
}

func configureCursor(input *textinput.Model) {
	input.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	input.Cursor.SetMode(cursor.CursorStatic)
}

func (m Model) Outcome() Outcome {
	outcome := Outcome{
		Command:  m.command,
		Accepted: m.accepted,
		Edited:   m.edited,
	}
	if !m.edited {
		outcome.Requirements = append([]capability.Requirement(nil), m.candidate.Requirements...)
	}
	return outcome
}

func (m Model) Accepted() bool { return m.accepted }

func (m Model) Command() string { return m.command }

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
		m.notice = ""
		m.context = msg.context
		m.inventory = msg.inventory
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
				if msg.err == context.DeadlineExceeded {
					// Keep the deadline in the error chain (errors.Is still matches)
					// while giving the user a plain-language reason on the input screen.
					// A provider-labeled inner deadline stays intact instead of being
					// misleadingly relabeled with the outer compile timeout.
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
		m.candidate = cloneCandidate(msg.candidates[0])
		m.command = m.candidate.Command
		m.explanation = m.candidate.Explanation
		m.edited = false
		m.safety = safety.Evaluate(m.command)
		m.validation = validate.Command(m.command)
		m.applicability = applicability.Evaluate(m.candidate.Requirements, m.inventory)
		m.screen = screenReview
		return m, nil
	case tea.KeyMsg:
		// SetValue is used only for bounded application-owned values in production,
		// but white-box callers can construct an oversize multibyte buffer despite
		// CharLimit's rune semantics. Bound it before handling any subsequent key
		// and require another action so truncation is never mistaken for submission.
		if m.screen == screenInput && m.boundActiveInput(&m.input, MaxIntentBytes) {
			return m, nil
		}
		if m.screen == screenEditCommand && m.boundActiveInput(&m.commandInput, validate.MaxCommandBytes) {
			return m, nil
		}

		switch msg.String() {
		case "ctrl+c":
			m.cancelActiveCompile()
			return m, tea.Quit
		case "esc":
			if m.screen == screenEditCommand {
				m.commandInput.Blur()
				m.notice = ""
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
			if m.screen == screenReview || m.screen == screenNoSuggestion {
				m.returnToInput()
				return m, nil
			}

			return m, tea.Quit
		case "enter":
			if m.screen == screenInput {
				m.intent = m.input.Value()
				m.notice = ""
				return m, m.startCompile()
			}

			if m.screen == screenNoSuggestion {
				return m, m.startCompile()
			}

			if m.screen == screenReview {
				if _, canAccept := acceptVerb(m.safety.Decision, m.validation.Class, m.applicability.Decision); !canAccept {
					return m, nil
				}
				m.accepted = true
				return m, tea.Quit
			}

			if m.screen == screenEditCommand {
				m.command = m.commandInput.Value()
				m.edited = true
				m.safety = safety.Evaluate(m.command)
				m.validation = validate.Command(m.command)
				m.applicability = applicability.EvaluateEdited(m.command, m.inventory)
				m.commandInput.Blur()
				m.notice = ""
				m.screen = screenReview
				return m, nil
			}
		case "b":
			if m.screen == screenReview || m.screen == screenNoSuggestion {
				m.returnToInput()
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
				editable, refusal := editableCommand(m.command)
				if refusal != "" {
					m.notice = refusal
					return m, nil
				}
				m.commandInput.SetValue(editable)
				m.commandInput.CursorEnd()
				m.notice = ""
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
		m.input, cmd, m.notice = updateBoundedInput(m.input, msg, MaxIntentBytes, m.notice)
	} else if m.screen == screenEditCommand {
		m.commandInput, cmd, m.notice = updateBoundedInput(m.commandInput, msg, validate.MaxCommandBytes, m.notice)
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
	if m.notice != "" {
		sections = append(sections, warnStyle.Render(textsafe.Visible(m.notice)))
	}
	sections = append(sections, mutedStyle.Render("enter submit · esc/ctrl+c quit"))
	return m.fitSections(sections)
}

func (m Model) loadingView() string {
	return m.fitSections([]string{
		headerStyle.Render("What do you want to do?"),
		textsafe.Visible(m.intent),
		m.spinner.View() + mutedStyle.Render("Compiling suggestion..."),
		mutedStyle.Render("esc cancel · ctrl+c quit"),
	})
}

// reviewView renders the minimal, height-aware review screen. Three essential
// rows — the hero command, the one-line validation/safety status, and the action
// hints — are always kept so the decision and the way to act on it can never be
// clipped away. Optional rows are added in keep-priority order only while they
// fit the terminal's height budget; a toggled section that cannot fit collapses
// to a one-line hint rather than pushing an essential row off a short screen.
// devEndpointBanner labels a session pointed at a loopback development endpoint
// so it cannot be mistaken for a production one (REQ-DEVENDPOINT-005). It names
// the effective endpoint in sanitized words before any color styling, and is
// empty for a normal session. Callers place it in a non-droppable row so a
// cramped terminal cannot hide it.
func (m Model) devEndpointBanner() string {
	if m.devEndpoint == "" {
		return ""
	}
	return warnStyle.Render("DEV ENDPOINT — provider requests go to " + textsafe.Visible(m.devEndpoint) + ", not the real provider")
}

func (m Model) reviewView() string {
	command := commandStyle.Render(textsafe.Visible(m.command))
	status := statusLine(m.validation, m.safety, m.applicability)
	_, editRefusal := editableCommand(m.command)
	actions := mutedStyle.Render(reviewActions(m.safety.Decision, m.validation.Class, m.applicability.Decision, editRefusal == "", m.whyExpanded, m.contextExpanded))
	notice := ""
	if m.notice != "" {
		notice = warnStyle.Render(textsafe.Visible(m.notice))
	}

	reasons := reviewReasons(m.validation, m.safety, m.applicability)
	why := ""
	if m.whyExpanded {
		title := "Why"
		if m.edited {
			title = "Explanation for the original suggestion"
		}
		why = section(title, textsafe.Visible(m.explanation))
	}
	usedContext := ""
	if m.contextExpanded {
		usedContext = section("Context used", strings.Join(contextLines(m.context, m.inventory, m.candidate.Requirements, m.edited, m.applicability.Tools), "\n"))
	}
	intent := mutedStyle.Render("intent: " + textsafe.Visible(m.intent))

	measure := m.rowMeasurer()

	// A non-positive height means the size is unknown (budget < 0 keeps every
	// row); otherwise budgetHeight reserves the frame's vertical padding and never
	// drops below a single content row.
	budget := m.budgetHeight()

	banner := m.devEndpointBanner()
	used := measure(command) + measure(status) + measure(actions)
	if notice != "" {
		used += measure(notice)
	}
	if banner != "" {
		used += measure(banner)
	}

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

	rows := []string{}
	if banner != "" {
		rows = append(rows, banner)
	}
	rows = append(rows, command, status)
	if notice != "" {
		rows = append(rows, notice)
	}
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
	sections := []string{
		headerStyle.Render("Edit command"),
		m.commandInput.View(),
	}
	if m.notice != "" {
		sections = append(sections, warnStyle.Render(textsafe.Visible(m.notice)))
	}
	sections = append(sections, mutedStyle.Render("enter save · esc discard · ctrl+c quit"))
	return m.fitSections(m.withDevBanner(sections))
}

// withDevBanner prefixes the development-endpoint label when one is active.
func (m Model) withDevBanner(sections []string) []string {
	banner := m.devEndpointBanner()
	if banner == "" {
		return sections
	}
	return append([]string{banner}, sections...)
}

func (m Model) noSuggestionView() string {
	return m.fitSections(m.withDevBanner([]string{
		headerStyle.Render("No suggestion"),
		section("Intent", textsafe.Visible(m.intent)),
		"The provider returned no command candidate.",
		mutedStyle.Render("enter/r retry · b/esc back · ctrl+c quit"),
	}))
}

func section(title, body string) string {
	return headerStyle.Render(title) + "\n" + body
}

func contextLines(c machinecontext.Context, inventory applicability.Inventory, requirements []capability.Requirement, edited bool, editedTools []string) []string {
	gitRepo := "no"
	if c.GitRepository {
		gitRepo = "yes"
	}

	branch := c.GitBranch
	if branch == "" {
		branch = "none"
	}

	lines := []string{
		"cwd: " + textsafe.Visible(c.WorkingDirectory),
		"shell: " + textsafe.Visible(c.Shell),
		"os: " + textsafe.Visible(c.OS),
		"git repo: " + gitRepo,
		"git root: " + textsafe.Visible(contextValue(c.GitRoot, "none")),
		"git branch: " + textsafe.Visible(branch),
	}
	seen := make(map[string]bool, len(lines))
	for _, line := range lines {
		seen[line] = true
	}
	for _, line := range relevantCapabilityLines(inventory, requirements, edited, editedTools) {
		if !seen[line] {
			lines = append(lines, line)
			seen[line] = true
		}
	}
	return lines
}

// relevantCapabilityLines exposes only facts used by the current applicability
// decision. It never includes inventory paths, probe output, or unrelated tools.
func relevantCapabilityLines(inventory applicability.Inventory, requirements []capability.Requirement, edited bool, editedTools []string) []string {
	if inventory == nil {
		return nil
	}
	if edited {
		lines := make([]string, 0, len(editedTools))
		for _, name := range editedTools {
			lines = append(lines, toolCapabilityLine(inventory, name))
		}
		return lines
	}

	seen := make(map[string]bool)
	lines := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		switch requirement.Kind {
		case capability.RequirementTool:
			key := "tool:" + requirement.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			lines = append(lines, toolCapabilityLine(inventory, requirement.Name))
		case capability.RequirementShell:
			if seen["shell"] {
				continue
			}
			seen["shell"] = true
			lines = append(lines, "shell: "+textsafe.Visible(string(inventory.Shell().Family)))
		case capability.RequirementOS:
			if seen["os"] {
				continue
			}
			seen["os"] = true
			lines = append(lines, "os: "+textsafe.Visible(inventory.OSFamily()))
		}
	}
	return lines
}

func toolCapabilityLine(inventory applicability.Inventory, name string) string {
	fact, known := inventory.LookupTool(name)
	status := "not probed (outside the fixed inventory)"
	if known && !fact.Present {
		status = "absent"
	} else if known {
		status = "present"
		if fact.Version.Known() {
			status += " " + fact.Version.String()
		} else {
			status += " (version unknown)"
		}
	}
	return "tool " + textsafe.Visible(name) + ": " + textsafe.Visible(status)
}

func contextValue(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

func cloneCandidate(candidate provider.Candidate) provider.Candidate {
	candidate.Requirements = append([]capability.Requirement(nil), candidate.Requirements...)
	return candidate
}

// statusLine names all three independent gates in words so the review remains
// understandable without color; styling only reinforces those words.
func statusLine(validation validate.Result, result safety.Result, appResult applicability.Result) string {
	validity := blockStyle.Render(string(validate.Invalid))
	switch validation.Class {
	case validate.Valid:
		validity = allowStyle.Render(string(validate.Valid))
	case validate.Warning:
		validity = warnStyle.Render(string(validate.Warning))
	}
	decision := decisionStyle(result.Decision).Render(string(result.Decision))
	appWord := "applicable"
	appStyle := allowStyle
	switch appResult.Decision {
	case applicability.Marked:
		appWord = "may not work"
		appStyle = warnStyle
	case applicability.Rejected:
		appWord = "not for this shell/OS"
		appStyle = blockStyle
	}
	return strings.Join([]string{validity, decision, appStyle.Render(appWord)}, mutedStyle.Render(" · "))
}

// reviewReasons renders the sanitized reasons behind a non-allow, validation
// warning/invalidity, or applicability mark, with validation reasons first. It
// returns an empty string only when every gate has no reason to show.
func reviewReasons(validation validate.Result, result safety.Result, appResult applicability.Result) string {
	var reasons []string
	if validation.Class != validate.Valid {
		reasons = append(reasons, validation.Reasons...)
	}
	if result.Decision != safety.Allow {
		reasons = append(reasons, result.Reasons...)
	}
	if appResult.Decision != applicability.Applicable {
		reasons = append(reasons, appResult.Reasons...)
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

// acceptVerb names the outcome of pressing enter on the review screen and
// reports whether accepting is permitted. The enter gate and the action hint
// both derive from it, so the two can never drift.
func acceptVerb(decision safety.Decision, validity validate.Class, appDecision applicability.Decision) (string, bool) {
	switch {
	case validity != validate.Valid && validity != validate.Warning:
		return "invalid", false
	case decision == safety.Block:
		return "blocked", false
	case appDecision == applicability.Rejected:
		return "inapplicable", false
	}
	return "accept", true
}

// reviewActions builds the single action hint line. The leading "enter <verb>"
// token names the accept outcome in words (accept/blocked/invalid) so the
// consequence of pressing enter is legible without color, and the "?"/"c" hints
// reflect whether each disclosure section is currently open. Esc returns to the
// input screen; Ctrl-C remains the review-screen quit action.
func reviewActions(decision safety.Decision, validity validate.Class, appDecision applicability.Decision, canEdit, whyExpanded, contextExpanded bool) string {
	verb, _ := acceptVerb(decision, validity, appDecision)
	edit := "e edit"
	if !canEdit {
		edit = "e edit unavailable"
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
		edit,
		why,
		usedContext,
		"b/esc back",
		"ctrl+c quit",
	}, " · ")
}
