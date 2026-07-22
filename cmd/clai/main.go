package main

import (
	"flag"
	"fmt"
	"os"

	"codeberg.org/newedia/clai/internal/app"
	"codeberg.org/newedia/clai/internal/auth"
	"codeberg.org/newedia/clai/internal/provider"
	"codeberg.org/newedia/clai/internal/provider/openrouter"
	"codeberg.org/newedia/clai/internal/provider/rules"
	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// Subcommand: clai auth ...
	if len(args) > 0 && args[0] == "auth" {
		fs := flag.NewFlagSet("auth", flag.ContinueOnError)
		apiKey := fs.String("api-key", "", "API key override")
		_ = fs.Parse(args[1:])
		return runAuth(fs.Args(), *apiKey)
	}

	fs := flag.NewFlagSet("clai", flag.ContinueOnError)
	copyCommand := fs.Bool("copy", false, "copy the accepted command to the clipboard")
	printCommand := fs.Bool("print-command", false, "print the accepted command to stdout")
	showVersion := fs.Bool("version", false, "print version and exit")
	providerName := fs.String("provider", envOr("CLAI_PROVIDER", "rules"), "provider: rules | openrouter")
	model := fs.String("model", "", "model override for LLM providers")
	apiKey := fs.String("api-key", "", "API key override for LLM providers")
	fallbackRules := fs.Bool("fallback-rules", false, "fall back to local rules when the selected provider errors")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		fmt.Println(version)
		return 0
	}

	p, err := selectProvider(*providerName, *model, *apiKey, *fallbackRules)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clai: %v\n", err)
		return 1
	}

	program := tea.NewProgram(app.NewWithProvider(p), tea.WithOutput(os.Stderr), tea.WithAltScreen())

	finalModel, err := program.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clai: %v\n", err)
		return 1
	}

	model2, ok := finalModel.(app.Model)
	if !ok || !model2.Accepted() {
		return 0
	}

	command := model2.Command()
	if *copyCommand {
		if err := clipboard.WriteAll(command); err != nil {
			fmt.Fprintf(os.Stderr, "clai: copy command: %v\n", err)
			return 1
		}
	}

	if *printCommand {
		fmt.Println(command)
	}
	return 0
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// fallback wraps a provider so that on error it retries with local rules,
// while still letting genuine no-suggestion results pass through.
type fallback struct {
	primary provider.Provider
}

func (f fallback) Compile(request provider.Request) ([]provider.Candidate, error) {
	candidates, err := f.primary.Compile(request)
	if err == nil {
		return candidates, nil
	}
	return rules.Provider{}.Compile(request)
}

func selectProvider(name, model, apiKey string, fallbackRules bool) (provider.Provider, error) {
	switch name {
	case "rules", "":
		return rules.Provider{}, nil
	case "openrouter":
		key, err := auth.Resolve("openrouter", apiKey)
		if err != nil {
			return nil, err
		}
		var p provider.Provider = openrouter.Provider{APIKey: key, Model: model}
		if fallbackRules {
			p = fallback{primary: p}
		}
		return p, nil
	case "anthropic", "openai":
		return nil, fmt.Errorf("provider %q is not implemented yet; use openrouter or rules", name)
	default:
		return nil, fmt.Errorf("unknown provider %q (known: rules, openrouter)", name)
	}
}
