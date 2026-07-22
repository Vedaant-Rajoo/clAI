package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/newedia/clai/internal/app"
	"codeberg.org/newedia/clai/internal/auth"
	"codeberg.org/newedia/clai/internal/provider"
	"codeberg.org/newedia/clai/internal/provider/openrouter"
	"codeberg.org/newedia/clai/internal/provider/rules"
	"codeberg.org/newedia/clai/internal/shellinit"
	"codeberg.org/newedia/clai/internal/validate"
	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
)

var (
	version    = "dev"
	executeTUI = runTUI
)

const (
	exitOK        = 0
	exitError     = 1
	exitUsage     = 2
	exitCancelled = 3
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "auth":
			return runAuthCommand(args[1:])
		case "init":
			return runInit(args[1:])
		case "widget":
			return runWidget(args[1:])
		}
	}

	return runInteractive(args)
}

func runAuthCommand(args []string) int {
	fs := flag.NewFlagSet("auth", flag.ContinueOnError)
	apiKey := fs.String("api-key", "", "API key override")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	return runAuth(fs.Args(), *apiKey)
}

func runInit(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: clai init <fish|bash|zsh>")
		return exitUsage
	}

	script, err := shellinit.Script(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "clai init: %v\n", err)
		return exitUsage
	}
	if _, err := io.WriteString(os.Stdout, script); err != nil {
		fmt.Fprintf(os.Stderr, "clai init: write script: %v\n", err)
		return exitError
	}
	return exitOK
}

func runInteractive(args []string) int {
	fs := flag.NewFlagSet("clai", flag.ContinueOnError)
	copyCommand := fs.Bool("copy", false, "copy the accepted command to the clipboard")
	printCommand := fs.Bool("print-command", false, "print the accepted command to stdout")
	showVersion := fs.Bool("version", false, "print version and exit")
	providerName, model, apiKey, fallbackRules := providerFlagSet(fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if *showVersion {
		fmt.Println(version)
		return exitOK
	}

	p, err := selectProvider(*providerName, *model, *apiKey, *fallbackRules)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clai: %v\n", err)
		return exitError
	}

	command, accepted, err := executeTUI(p, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "clai: %v\n", err)
		return exitError
	}
	if !accepted {
		return exitOK
	}

	if *copyCommand {
		if err := clipboard.WriteAll(command); err != nil {
			fmt.Fprintf(os.Stderr, "clai: copy command: %v\n", err)
			return exitError
		}
	}
	if *printCommand {
		fmt.Println(command)
	}
	return exitOK
}

func runWidget(args []string) int {
	fs := flag.NewFlagSet("widget", flag.ContinueOnError)
	shell := fs.String("shell", "", "active shell: fish | bash | zsh")
	resultFile := fs.String("result-file", "", "caller-created result file")
	providerName, model, apiKey, fallbackRules := providerFlagSet(fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 || !validShell(*shell) || *resultFile == "" {
		fmt.Fprintln(os.Stderr, "usage: clai widget --shell <fish|bash|zsh> --result-file <path>")
		return exitUsage
	}
	if _, err := inspectWidgetResult(*resultFile); err != nil {
		fmt.Fprintf(os.Stderr, "clai widget: invalid result file: %v\n", err)
		return exitError
	}
	keepResult := false
	defer func() {
		if !keepResult {
			_ = os.Remove(*resultFile)
		}
	}()

	p, err := selectProvider(*providerName, *model, *apiKey, *fallbackRules)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clai widget: %v\n", err)
		return exitError
	}

	command, accepted, err := executeTUI(p, *shell)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clai widget: %v\n", err)
		return exitError
	}
	if !accepted {
		return exitCancelled
	}
	if result := validate.Command(command); !result.Valid {
		fmt.Fprintf(os.Stderr, "clai widget: accepted command is not exportable: %s\n", strings.Join(result.Reasons, " "))
		return exitError
	}
	if err := writeWidgetResult(*resultFile, command); err != nil {
		fmt.Fprintf(os.Stderr, "clai widget: write result: %v\n", err)
		return exitError
	}
	keepResult = true
	return exitOK
}

func providerFlagSet(fs *flag.FlagSet) (providerName, model, apiKey *string, fallbackRules *bool) {
	providerName = fs.String("provider", envOr("CLAI_PROVIDER", "rules"), "provider: rules | openrouter")
	model = fs.String("model", "", "model override for LLM providers")
	apiKey = fs.String("api-key", "", "API key override for LLM providers")
	fallbackRules = fs.Bool("fallback-rules", false, "fall back to local rules when the selected provider errors")
	return providerName, model, apiKey, fallbackRules
}

func runTUI(p provider.Provider, activeShell string) (string, bool, error) {
	model := app.NewWithProvider(p)
	options := []tea.ProgramOption{tea.WithOutput(os.Stderr), tea.WithAltScreen()}
	if activeShell != "" {
		model = app.NewWithProviderAndShell(p, activeShell)

		tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err != nil {
			return "", false, fmt.Errorf("open controlling terminal: %w", err)
		}
		defer tty.Close()
		options = []tea.ProgramOption{tea.WithInput(tty), tea.WithOutput(tty), tea.WithAltScreen()}
	}

	program := tea.NewProgram(model, options...)
	finalModel, err := program.Run()
	if err != nil {
		return "", false, err
	}

	result, ok := finalModel.(app.Model)
	if !ok || !result.Accepted() {
		return "", false, nil
	}
	return result.Command(), true, nil
}

func validShell(shell string) bool {
	switch shell {
	case "fish", "bash", "zsh":
		return true
	default:
		return false
	}
}

func inspectWidgetResult(path string) (os.FileInfo, error) {
	if path == "" {
		return nil, errors.New("result file path is empty")
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("result file path must be absolute")
	}

	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("result file must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("result file permissions must not allow group or other access")
	}
	if info.Size() != 0 {
		return nil, errors.New("result file must be empty")
	}
	return info, nil
}

func writeWidgetResult(path, command string) error {
	before, err := inspectWidgetResult(path)
	if err != nil {
		return err
	}

	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()

	after, err := file.Stat()
	if err != nil {
		return err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return errors.New("result file changed while opening")
	}
	if after.Mode().Perm()&0o077 != 0 {
		return errors.New("result file permissions changed while opening")
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := io.WriteString(file, command); err != nil {
		return err
	}
	return file.Sync()
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
