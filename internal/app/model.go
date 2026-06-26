package app

import (
	"fmt"
	"strings"

	"codeberg.org/newedia/clai/internal/compiler"
	machinecontext "codeberg.org/newedia/clai/internal/context"
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
	screenReview
	screenEditCommand
)

type Model struct {
	input        textinput.Model
	commandInput textinput.Model
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
	input := textinput.New()
	input.Placeholder = "Describe what you want to do..."
	configureCursor(&input)
	input.Focus()

	commandInput := textinput.New()
	commandInput.Placeholder = "Edit command..."
	commandInput.Prompt = "$ "
	configureCursor(&commandInput)

	return Model{input: input, commandInput: commandInput, screen: screenInput}
}

func (m Model) Init() tea.Cmd {
	return nil
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

			return m, tea.Quit
		case "enter":
			if m.screen == screenInput {
				m.intent = m.input.Value()
				m.accepted = false
				m.context = machinecontext.Collect()
				result := compiler.Compile(m.intent)
				m.command = result.Command
				m.explanation = result.Explanation
				m.safety = safety.Evaluate(m.command)
				m.validation = validate.Command(m.command)
				m.screen = screenReview
				return m, nil
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
				m.commandInput.SetValue(m.command)
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
	if m.err != nil {
		return fmt.Sprintf("\n  Error: %v\n\n", m.err)
	}
	switch m.screen {
	case screenInput:
		return m.inputView()
	case screenReview:
		return m.reviewView()
	case screenEditCommand:
		return m.editCommandView()
	}
	return ""
}

func (m Model) inputView() string {
	return "\n  What do you want to do?\n\n  " + m.input.View() + "\n\n  enter submit · esc quit\n"
}

func (m Model) reviewView() string {
	return fmt.Sprintf("\n  Intent:\n    %s\n\n  Suggested command:\n    %s\n\n  Why:\n    %s\n\n  Context used:\n    %s\n\n  Validation:\n    %s\n\n  Safety:\n    %s\n    %s\n\n  %s\n", m.intent, m.command, m.explanation, strings.Join(contextLines(m.context), "\n    "), validationText(m.validation), m.safety.Decision, strings.Join(m.safety.Reasons, "\n    "), reviewActions(m.safety.Decision, m.validation.Valid))
}

func (m Model) editCommandView() string {
	return "\n  Edit command:\n\n  " + m.commandInput.View() + "\n\n  enter save · esc discard\n"
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
		"cwd: " + c.WorkingDirectory,
		"shell: " + c.Shell,
		"os: " + c.OS,
		"git repo: " + gitRepo,
		"git branch: " + branch,
	}
}

func validationText(result validate.Result) string {
	if result.Valid {
		return "valid"
	}

	return "invalid\n    " + strings.Join(result.Reasons, "\n    ")
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
