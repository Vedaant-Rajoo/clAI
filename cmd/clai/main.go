package main

import (
	"fmt"
	"os"

	"codeberg.org/newedia/clai/internal/app"
	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	p := tea.NewProgram(app.New(), tea.WithOutput(os.Stderr))

	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clai: %v\n", err)
		os.Exit(1)
	}

	model, ok := finalModel.(app.Model)
	if ok && model.Accepted() {
		fmt.Println(model.Command())
	}
}
