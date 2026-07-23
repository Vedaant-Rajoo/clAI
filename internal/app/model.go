package app

import (
	"context"
	"errors"
	"strings"

	machinecontext "codeberg.org/newedia/clai/internal/context"
	"codeberg.org/newedia/clai/internal/provider"
	"codeberg.org/newedia/clai/internal/provider/rules"
	"codeberg.org/newedia/clai/internal/safety"
	"codeberg.org/newedia/clai/internal/textsafe"
	"codeberg.org/newedia/clai/internal/validate"
	"github.com/charmbracelet/bubbles/cursor"
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

type Model struct {
	input         textinput.Model
	commandInput  textinput.Model
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

	return Model{
		input:        input,
		commandInput: commandInput,
		provider:     p,
		activeShell:  shell,
		screen:       screenInput,
	}
}

func (m Model) Init() tea.Cmd {
	return nil
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
		return compileResult{requestID: requestID, context: collected, candidates: candidates, err: err}
	}
}

func (m *Model) startCompile() tea.Cmd {
	if m.cancelCompile != nil {
		m.cancelCompile()
	}
	m.nextRequest++
	m.activeRequest = m.nextRequest
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelCompile = cancel
	m.accepted = false
	m.command = ""
	m.explanation = ""
	m.err = nil
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
		// Track the terminal width so views wrap long content instead of
		// letting the renderer truncate it. Resize never touches acceptance,
		// screen, or request state.
		m.width = msg.Width
		return m, nil
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
	return m.frame(strings.Join(sections, "\n\n"))
}

func (m Model) loadingView() string {
	return m.frame(strings.Join([]string{
		headerStyle.Render("What do you want to do?"),
		textsafe.Visible(m.intent),
		mutedStyle.Render("Compiling suggestion..."),
	}, "\n\n"))
}

func (m Model) reviewView() string {
	sections := []string{
		section("Intent", textsafe.Visible(m.intent)),
		section("Suggested command", commandStyle.Render(textsafe.Visible(m.command))),
		section("Why", textsafe.Visible(m.explanation)),
		section("Context used", strings.Join(contextLines(m.context), "\n")),
		section("Validation", validationText(m.validation)),
		section("Safety", safetyText(m.safety)),
		mutedStyle.Render(reviewActions(m.safety.Decision, m.validation.Valid)),
	}

	return m.frame(strings.Join(sections, "\n\n"))
}

func (m Model) editCommandView() string {
	return m.frame(strings.Join([]string{
		headerStyle.Render("Edit command"),
		m.commandInput.View(),
		mutedStyle.Render("enter save · esc discard"),
	}, "\n\n"))
}

func (m Model) noSuggestionView() string {
	return m.frame(strings.Join([]string{
		headerStyle.Render("No suggestion"),
		section("Intent", textsafe.Visible(m.intent)),
		"The provider returned no command candidate.",
		mutedStyle.Render("enter/r retry · b back · esc cancel"),
	}, "\n\n"))
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

func validationText(result validate.Result) string {
	if result.Valid {
		return allowStyle.Render("valid")
	}

	return blockStyle.Render("invalid") + "\n" + strings.Join(result.Reasons, "\n")
}

func safetyText(result safety.Result) string {
	return decisionStyle(result.Decision).Render(string(result.Decision)) + "\n" + strings.Join(result.Reasons, "\n")
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

func reviewActions(decision safety.Decision, valid bool) string {
	if !valid {
		return "enter invalid · e edit · b back · esc cancel"
	}

	if decision == safety.Block {
		return "enter blocked · e edit · b back · esc cancel"
	}

	return "enter accept · e edit · b back · esc cancel"
}
