package app

import (
	"fmt"
	"strings"
	"unicode"

	machinecontext "codeberg.org/newedia/clai/internal/context"
	"codeberg.org/newedia/clai/internal/provider"
	"codeberg.org/newedia/clai/internal/provider/rules"
	"codeberg.org/newedia/clai/internal/safety"
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
	input        textinput.Model
	commandInput textinput.Model
	provider     provider.Provider
	activeShell  string
	screen       Screen
	intent       string
	command      string
	explanation  string
	accepted     bool
	context      machinecontext.Context
	safety       safety.Result
	validation   validate.Result
	err          error
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
	context     machinecontext.Context
	candidates  []provider.Candidate
	err         error
}

// compile runs context collection and the provider off the UI goroutine so a
// slow network call never freezes the TUI.
func (m Model) compile() tea.Cmd {
	intent := m.intent
	shell := m.activeShell
	p := m.provider
	return func() tea.Msg {
		ctx := machinecontext.CollectWithShell(shell)
		candidates, err := p.Compile(provider.Request{Intent: intent, Context: ctx})
		return compileResult{context: ctx, candidates: candidates, err: err}
	}
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
	case compileResult:
		m.context = msg.context
		if msg.err != nil {
			m.err = msg.err
			m.screen = screenInput
			m.input.SetValue(m.intent)
			m.input.CursorEnd()
			m.input.Focus()
			return m, nil
		}
		candidates := msg.candidates
		if len(candidates) == 0 {
			m.command = `echo "No suggestion available yet"`
			m.explanation = "The provider returned no candidates for this intent."
		} else {
			m.command = candidates[0].Command
			m.explanation = candidates[0].Explanation
		}
		m.safety = safety.Evaluate(m.command)
		m.validation = validate.Command(m.command)
		m.screen = screenReview
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.screen == screenEditCommand {
				m.commandInput.Blur()
				m.screen = screenReview
				return m, nil
			}
			if m.screen == screenLoading {
				// The compile call is already in flight; ignore esc rather than
				// racing the result message. ctrl+c still quits.
				return m, nil
			}

			return m, tea.Quit
		case "enter":
			if m.screen == screenInput {
				m.intent = m.input.Value()
				m.accepted = false
				m.err = nil
				m.screen = screenLoading
				return m, m.compile()
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
			if m.screen == screenReview {
				m.input.Focus()
				m.screen = screenInput
				return m, nil
			}
		case "e":
			if m.screen == screenReview {
				// Strip terminal control characters rather than escaping them:
				// validation rejects control characters anyway, and escaping
				// them to literal sequences would corrupt the command on save.
				m.commandInput.SetValue(stripControl(m.command))
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
	}
	return ""
}

func (m Model) inputView() string {
	sections := []string{
		headerStyle.Render("What do you want to do?"),
		m.input.View(),
	}
	if m.err != nil {
		sections = append(sections, blockStyle.Render("Error: "+terminalSafe(m.err.Error())))
	}
	sections = append(sections, mutedStyle.Render("enter submit · esc quit"))
	return baseStyle.Render(strings.Join(sections, "\n\n"))
}

func (m Model) loadingView() string {
	return baseStyle.Render(strings.Join([]string{
		headerStyle.Render("What do you want to do?"),
		terminalSafe(m.intent),
		mutedStyle.Render("Compiling suggestion..."),
	}, "\n\n"))
}

func (m Model) reviewView() string {
	sections := []string{
		section("Intent", terminalSafe(m.intent)),
		section("Suggested command", commandStyle.Render(terminalSafe(m.command))),
		section("Why", terminalSafe(m.explanation)),
		section("Context used", strings.Join(contextLines(m.context), "\n")),
		section("Validation", validationText(m.validation)),
		section("Safety", safetyText(m.safety)),
		mutedStyle.Render(reviewActions(m.safety.Decision, m.validation.Valid)),
	}

	return baseStyle.Render(strings.Join(sections, "\n\n"))
}

func (m Model) editCommandView() string {
	return baseStyle.Render(strings.Join([]string{
		headerStyle.Render("Edit command"),
		m.commandInput.View(),
		mutedStyle.Render("enter save · esc discard"),
	}, "\n\n"))
}

// stripControl removes terminal control characters, keeping ordinary text
// intact for editing.
func stripControl(value string) string {
	var result strings.Builder
	for _, r := range value {
		if !unicode.IsControl(r) {
			result.WriteRune(r)
		}
	}
	return result.String()
}

func terminalSafe(value string) string {
	var result strings.Builder
	for _, r := range value {
		if !unicode.IsControl(r) {
			result.WriteRune(r)
			continue
		}
		switch r {
		case '\n':
			result.WriteString(`\n`)
		case '\r':
			result.WriteString(`\r`)
		case '\t':
			result.WriteString(`\t`)
		default:
			fmt.Fprintf(&result, `\u{%04X}`, r)
		}
	}
	return result.String()
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
		"cwd: " + terminalSafe(c.WorkingDirectory),
		"shell: " + terminalSafe(c.Shell),
		"os: " + terminalSafe(c.OS),
		"git repo: " + gitRepo,
		"git root: " + terminalSafe(contextValue(c.GitRoot, "none")),
		"git branch: " + terminalSafe(branch),
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
