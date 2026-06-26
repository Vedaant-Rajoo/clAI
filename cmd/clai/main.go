package main

import (
	"flag"
	"fmt"
	"os"

	"codeberg.org/newedia/clai/internal/app"
	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	copyCommand := flag.Bool("copy", false, "copy the accepted command to the clipboard")
	printCommand := flag.Bool("print-command", false, "print the accepted command to stdout")
	flag.Parse()

	p := tea.NewProgram(app.New(), tea.WithOutput(os.Stderr), tea.WithAltScreen())

	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clai: %v\n", err)
		os.Exit(1)
	}

	model, ok := finalModel.(app.Model)
	if !ok || !model.Accepted() {
		return
	}

	command := model.Command()
	if *copyCommand {
		if err := clipboard.WriteAll(command); err != nil {
			fmt.Fprintf(os.Stderr, "clai: copy command: %v\n", err)
			os.Exit(1)
		}
	}

	if *printCommand {
		fmt.Println(command)
	}
}
