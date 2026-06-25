package main

import (
	"flag"
	"fmt"
	"os"

	"codeberg.org/newedia/clai/internal/app"
	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	printCommand := flag.Bool("print-command", false, "print the accepted command to stdout")
	flag.Parse()

	p := tea.NewProgram(app.New(), tea.WithOutput(os.Stderr), tea.WithAltScreen())

	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clai: %v\n", err)
		os.Exit(1)
	}

	model, ok := finalModel.(app.Model)
	if *printCommand && ok && model.Accepted() {
		fmt.Println(model.Command())
	}
}
