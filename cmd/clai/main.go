package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/newedia/clai/internal/app"
	"codeberg.org/newedia/clai/internal/auth"
	machinecontext "codeberg.org/newedia/clai/internal/context"
	"codeberg.org/newedia/clai/internal/provider"
	"codeberg.org/newedia/clai/internal/provider/openrouter"
	"codeberg.org/newedia/clai/internal/provider/rules"
	"codeberg.org/newedia/clai/internal/safety"
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
	c := cli{stdout: os.Stdout, stderr: os.Stderr}
	os.Exit(c.run(os.Args[1:]))
}

// cli carries process-boundary dependencies so command behavior can be tested
// without using real credential, browser, listener, timer, or network services.
type cli struct {
	stdout io.Writer
	stderr io.Writer
	ctx    context.Context

	authRuntime         authRuntime
	authReadLine        func() (string, error)
	authStore           func(provider, key string) error
	authDelete          func(provider string) error
	authSourceWithError func(provider, explicit string) (string, error)
}

func (c cli) run(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "help", "-h", "--help":
			return c.runHelp(args[1:])
		case "auth":
			return c.runAuthCommand(args[1:])
		case "init":
			return c.runInit(args[1:])
		case "version":
			return c.runVersion(args[1:])
		case "widget":
			return c.runWidget(args[1:])
		default:
			if !strings.HasPrefix(args[0], "-") {
				fmt.Fprintf(c.stderr, "clai: unknown command %q\nRun 'clai help' for usage.\n", args[0])
				return exitUsage
			}
		}
	}

	return c.runInteractive(args)
}

// runAuthCommand routes the strict `clai auth <verb> [options]` grammar.
func (c cli) runAuthCommand(args []string) int {
	if len(args) == 1 && args[0] == "help" {
		return c.runHelp([]string{"auth"})
	}
	command, err := parseAuthArgs(args)
	if err != nil {
		fmt.Fprintf(c.stderr, "clai auth: %v\nRun 'clai auth help' for usage.\n", err)
		return exitUsage
	}
	return c.runAuth(command)
}

func (c cli) runVersion(args []string) int {
	if len(args) > 0 {
		if isHelpArg(args[0]) {
			return c.runHelp([]string{"version"})
		}
		fmt.Fprintf(c.stderr, "clai version: unknown argument %q\nRun 'clai version help' for usage.\n", args[0])
		return exitUsage
	}
	fmt.Fprintln(c.stdout, version)
	return exitOK
}

func (c cli) runInit(args []string) int {
	if len(args) == 1 && isHelpArg(args[0]) {
		return c.runHelp([]string{"init"})
	}
	if len(args) != 1 {
		fmt.Fprintln(c.stderr, "usage: clai init <fish|bash|zsh>\nRun 'clai init help' for usage.")
		return exitUsage
	}

	script, err := shellinit.Script(args[0])
	if err != nil {
		fmt.Fprintf(c.stderr, "clai init: %v\n", err)
		return exitUsage
	}
	if _, err := io.WriteString(c.stdout, script); err != nil {
		fmt.Fprintf(c.stderr, "clai init: write script: %v\n", err)
		return exitError
	}
	return exitOK
}

func (c cli) runInteractive(args []string) int {
	fs := flag.NewFlagSet("clai", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := registerInteractiveFlags(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printMainHelp(c.stdout)
			return exitOK
		}
		fmt.Fprintf(c.stderr, "clai: %v\nRun 'clai help' for usage.\n", err)
		return exitUsage
	}

	policy, sharedFields, err := f.contextOptions()
	if err != nil {
		fmt.Fprintf(c.stderr, "clai: %v\nRun 'clai help' for usage.\n", err)
		return exitUsage
	}

	if *f.showVersion {
		fmt.Fprintln(c.stdout, version)
		return exitOK
	}
	p, err := selectProvider(*f.providerName, *f.model, *f.apiKey, *f.fallbackRules, policy, sharedFields)
	if err != nil {
		fmt.Fprintf(c.stderr, "clai: %v\n", err)
		return exitError
	}

	command, accepted, err := executeTUI(p, "")
	if err != nil {
		fmt.Fprintf(c.stderr, "clai: %v\n", err)
		return exitError
	}
	if !accepted {
		return exitOK
	}

	if *f.copyCommand {
		if err := clipboard.WriteAll(command); err != nil {
			fmt.Fprintf(c.stderr, "clai: copy command: %v\n", err)
			return exitError
		}
	}
	if *f.printCommand {
		fmt.Fprintln(c.stdout, command)
	}
	return exitOK
}

func (c cli) runWidget(args []string) int {
	if len(args) > 0 && args[0] == "help" {
		return c.runHelp([]string{"widget"})
	}
	fs := flag.NewFlagSet("widget", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := registerWidgetFlags(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return c.runHelp([]string{"widget"})
		}
		fmt.Fprintf(c.stderr, "clai widget: %v\nRun 'clai widget help' for usage.\n", err)
		return exitUsage
	}
	if fs.NArg() != 0 || !validShell(*f.shell) || *f.resultFile == "" {
		fmt.Fprintln(c.stderr, "usage: clai widget --shell <fish|bash|zsh> --result-file <path>\nRun 'clai widget help' for usage.")
		return exitUsage
	}
	policy, sharedFields, err := f.contextOptions()
	if err != nil {
		fmt.Fprintf(c.stderr, "clai widget: %v\nRun 'clai widget help' for usage.\n", err)
		return exitUsage
	}
	if _, err := inspectWidgetResult(*f.resultFile); err != nil {
		fmt.Fprintf(c.stderr, "clai widget: invalid result file: %v\n", err)
		return exitError
	}
	keepResult := false
	defer func() {
		if !keepResult {
			_ = os.Remove(*f.resultFile)
		}
	}()

	p, err := selectProvider(*f.providerName, *f.model, *f.apiKey, *f.fallbackRules, policy, sharedFields)
	if err != nil {
		fmt.Fprintf(c.stderr, "clai widget: %v\n", err)
		return exitError
	}

	command, accepted, err := executeTUI(p, *f.shell)
	if err != nil {
		fmt.Fprintf(c.stderr, "clai widget: %v\n", err)
		return exitError
	}
	if !accepted {
		return exitCancelled
	}
	// Transport boundary revalidation (REQ-INVARIANT-008): re-check the exact
	// bytes about to be exported against both the structural gate
	// (REQ-INVARIANT-003) and the safety gate (REQ-INVARIANT-004) independently
	// of the TUI's in-model acceptance gating, so a blocked or invalid command
	// can never reach widget transport even if the interactive gate regresses.
	if result := validate.Command(command); !result.Valid {
		fmt.Fprintf(c.stderr, "clai widget: accepted command is not exportable: %s\n", strings.Join(result.Reasons, " "))
		return exitError
	}
	if decision := safety.Evaluate(command); decision.Decision == safety.Block {
		fmt.Fprintf(c.stderr, "clai widget: accepted command is blocked by safety and not exportable: %s\n", strings.Join(decision.Reasons, " "))
		return exitError
	}
	if err := writeWidgetResult(*f.resultFile, command); err != nil {
		fmt.Fprintf(c.stderr, "clai widget: write result: %v\n", err)
		return exitError
	}
	keepResult = true
	return exitOK
}

// providerFlags holds the flags shared by the interactive and widget commands.
type providerFlags struct {
	providerName  *string
	model         *string
	apiKey        *string
	fallbackRules *bool
	contextPolicy *contextPolicyFlag
	sharedContext *sharedContextFlag
}

type contextPolicyFlag struct {
	value machinecontext.Policy
	set   bool
}

func (f *contextPolicyFlag) String() string { return string(f.value) }
func (f *contextPolicyFlag) Set(value string) error {
	policy, err := machinecontext.ParsePolicy(value)
	if err != nil {
		return err
	}
	if f.set {
		return errors.New("context-policy may be supplied only once")
	}
	f.value = policy
	f.set = true
	return nil
}

type sharedContextFlag struct{ values []string }

func (f *sharedContextFlag) String() string { return strings.Join(f.values, ",") }
func (f *sharedContextFlag) Set(value string) error {
	if !machinecontext.IsExplicitField(value) {
		return fmt.Errorf("invalid shared context field %q (want working_directory, git_root, or git_branch)", value)
	}
	f.values = append(f.values, value)
	return nil
}

func registerProviderFlags(fs *flag.FlagSet) providerFlags {
	policy := &contextPolicyFlag{}
	shared := &sharedContextFlag{}
	fs.Var(policy, "context-policy", "context policy: local-only | remote-minimal | remote-explicit")
	fs.Var(shared, "share-context", "share one context field (repeatable): working_directory | git_root | git_branch")
	return providerFlags{
		providerName:  fs.String("provider", envOr("CLAI_PROVIDER", "rules"), "provider: rules | openrouter"),
		model:         fs.String("model", "", "model override for LLM providers"),
		apiKey:        fs.String("api-key", "", "API key override for LLM providers"),
		fallbackRules: fs.Bool("fallback-rules", false, "fall back to local rules when the selected provider errors"),
		contextPolicy: policy,
		sharedContext: shared,
	}
}

func (f providerFlags) contextOptions() (machinecontext.Policy, []string, error) {
	shared, err := machinecontext.NormalizeExplicitFields(f.sharedContext.values)
	if err != nil {
		return "", nil, err
	}
	policy := f.contextPolicy.value
	if !f.contextPolicy.set {
		if len(shared) != 0 {
			return "", nil, errors.New("--share-context requires an explicit --context-policy remote-explicit in the same invocation")
		}
		if *f.providerName == "openrouter" {
			policy = machinecontext.PolicyRemoteMinimal
		} else {
			policy = machinecontext.PolicyLocalOnly
		}
	}
	if policy == machinecontext.PolicyRemoteExplicit {
		if !f.contextPolicy.set || len(shared) == 0 {
			return "", nil, errors.New("--context-policy remote-explicit requires at least one --share-context field in the same invocation")
		}
	} else if len(shared) != 0 {
		return "", nil, errors.New("--share-context is valid only with --context-policy remote-explicit")
	}
	return policy, shared, nil
}

// interactiveFlags are the flags for the default (TUI) command.
type interactiveFlags struct {
	copyCommand  *bool
	printCommand *bool
	showVersion  *bool
	providerFlags
}

func registerInteractiveFlags(fs *flag.FlagSet) interactiveFlags {
	return interactiveFlags{
		copyCommand:   fs.Bool("copy", false, "copy the accepted command to the clipboard"),
		printCommand:  fs.Bool("print-command", false, "print the accepted command to stdout"),
		showVersion:   fs.Bool("version", false, "print version and exit"),
		providerFlags: registerProviderFlags(fs),
	}
}

// widgetFlags are the flags for the internal shell-adapter command.
type widgetFlags struct {
	shell      *string
	resultFile *string
	providerFlags
}

func registerWidgetFlags(fs *flag.FlagSet) widgetFlags {
	return widgetFlags{
		shell:         fs.String("shell", "", "active shell: fish | bash | zsh"),
		resultFile:    fs.String("result-file", "", "caller-created result file"),
		providerFlags: registerProviderFlags(fs),
	}
}

func runTUI(p provider.Provider, activeShell string) (string, bool, error) {
	model := app.NewWithProvider(p)
	options := []tea.ProgramOption{tea.WithOutput(os.Stderr), tea.WithAltScreen()}
	if activeShell != "" {
		model = app.NewWithProviderAndShell(p, activeShell)

		tty, err := openControllingTerminal()
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

func (f fallback) Compile(ctx context.Context, request provider.Request) ([]provider.Candidate, error) {
	candidates, err := f.primary.Compile(ctx, request)
	if err == nil {
		return candidates, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	return rules.Provider{}.Compile(ctx, request)
}

func selectProvider(name, model, apiKey string, fallbackRules bool, policy machinecontext.Policy, sharedFields []string) (provider.Provider, error) {
	switch name {
	case "rules", "":
		return rules.Provider{}, nil
	case "openrouter":
		key, err := auth.Resolve("openrouter", apiKey)
		if err != nil {
			return nil, err
		}
		var p provider.Provider = openrouter.Provider{APIKey: key, Model: model, Policy: policy, SharedFields: sharedFields}
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
