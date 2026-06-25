package main

import (
	"fmt"
	"os"

	"codeberg.org/newedia/clai/internal/app"
	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	p := tea.NewProgram(app.New())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "clai: %v\n", err)
		os.Exit(1)
	}
}
