package app

import (
	"fmt"
	"strings"

	"codeberg.org/newedia/clai/internal/compiler"
	machinecontext "codeberg.org/newedia/clai/internal/context"
	"codeberg.org/newedia/clai/internal/safety"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type Screen int

const (
	screenInput Screen = iota
	screenReview
)

type Model struct {
	input       textinput.Model
	screen      Screen
	intent      string
	command     string
	explanation string
	context     machinecontext.Context
	safety      safety.Result
	err         error
}

func New() Model {
	input := textinput.New()
	input.Placeholder = "Describe what you want to do..."
	input.Focus()
	return Model{input: input, screen: screenInput}
}

func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			if m.screen == screenInput {
				m.intent = m.input.Value()
				m.context = machinecontext.Collect()
				result := compiler.Compile(m.intent)
				m.command = result.Command
				m.explanation = result.Explanation
				m.safety = safety.Evaluate(m.command)
				m.screen = screenReview
				return m, nil
			}

			if m.screen == screenReview {
				if m.safety.Decision == safety.Block {
					return m, nil
				}

				return m, tea.Quit
			}
		case "b":
			if m.screen == screenReview {
				m.screen = screenInput
				return m, nil
			}
		}
	}

	if m.screen == screenInput {
		m.input, cmd = m.input.Update(msg)
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
	}
	return ""
}

func (m Model) inputView() string {
	return "\n  What do you want to do?\n\n  " + m.input.View() + "\n\n  enter submit · esc quit\n"
}

func (m Model) reviewView() string {
	return fmt.Sprintf("\n  Intent:\n    %s\n\n  Suggested command:\n    %s\n\n  Why:\n    %s\n\n  Context used:\n    %s\n\n  Safety:\n    %s\n    %s\n\n  %s\n", m.intent, m.command, m.explanation, strings.Join(contextLines(m.context), "\n    "), m.safety.Decision, strings.Join(m.safety.Reasons, "\n    "), reviewActions(m.safety.Decision))
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

func reviewActions(decision safety.Decision) string {
	if decision == safety.Block {
		return "enter blocked · b back · esc cancel"
	}

	return "enter accept · b back · esc cancel"
}
