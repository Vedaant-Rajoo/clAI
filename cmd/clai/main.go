package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Vedaant-Rajoo/clai/internal/app"
	"github.com/Vedaant-Rajoo/clai/internal/applicability"
	"github.com/Vedaant-Rajoo/clai/internal/auth"
	"github.com/Vedaant-Rajoo/clai/internal/capability"
	"github.com/Vedaant-Rajoo/clai/internal/config"
	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
	"github.com/Vedaant-Rajoo/clai/internal/provider/anthropic"
	"github.com/Vedaant-Rajoo/clai/internal/provider/openrouter"
	"github.com/Vedaant-Rajoo/clai/internal/provider/rules"
	"github.com/Vedaant-Rajoo/clai/internal/safety"
	"github.com/Vedaant-Rajoo/clai/internal/shellinit"
	"github.com/Vedaant-Rajoo/clai/internal/textsafe"
	"github.com/Vedaant-Rajoo/clai/internal/validate"
	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
)

var (
	version        = "dev"
	executeTUI     = runTUI
	loadConfig     = config.Load
	clipboardWrite = clipboard.WriteAll
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
	migrateLegacyConfigForRequestedOutput()
	fmt.Fprintln(c.stdout, version)
	return exitOK
}

// migrateLegacyConfigForRequestedOutput completes a pending one-way Darwin
// config migration without changing the version command's requested-output
// contract. Errors stay silent here because version historically ignores
// configuration failures; an interactive invocation will still report the
// existing warning if the preferred file is corrupt or inaccessible.
func migrateLegacyConfigForRequestedOutput() {
	if runtime.GOOS != "darwin" {
		return
	}
	_, _ = loadConfig()
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

	// Go's flag package stops at the first non-flag argument, so a trailing
	// positional would otherwise be silently ignored and start the TUI. A
	// trailing help request is honored; anything else is a usage error, matching
	// the widget command's stricter handling.
	if fs.NArg() != 0 {
		if fs.NArg() == 1 && isHelpArg(fs.Arg(0)) {
			printMainHelp(c.stdout)
			return exitOK
		}
		fmt.Fprintf(c.stderr, "clai: unexpected argument %q\nRun 'clai help' for usage.\n", fs.Arg(0))
		return exitUsage
	}

	// Explicit-set state is collected once, right after Parse: boolean
	// delivery flags cannot use empty-string sentinels because a parsed false
	// is indistinguishable from unset, so flag.Visit records which flags the
	// user actually passed (CONF-03 adjacency edge).
	explicit := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { explicit[fl.Name] = true })

	// A standalone version request consumes no configuration. Keep combinations
	// with other explicit flags on the normal validation path so invocation-local
	// usage errors still take precedence over the version shortcut.
	if *f.showVersion && len(explicit) == 1 {
		migrateLegacyConfigForRequestedOutput()
		fmt.Fprintln(c.stdout, version)
		return exitOK
	}

	// Settings resolve before any provider-dependent validation so that a
	// config-selected provider validates --dev-endpoint and defaults the
	// context policy exactly like a flag-selected one (CONF-03).
	cfg, resolvedProvider, resolvedModel := c.resolveSettings(f.providerFlags)

	// The endpoint is validated first because the context policy defaults on
	// endpoint classification (REQ-CONTEXT-003, REQ-DEVENDPOINT-004).
	devEndpoint, err := f.devEndpointOption(resolvedProvider)
	if err != nil {
		fmt.Fprintf(c.stderr, "clai: %v\nRun 'clai help' for usage.\n", err)
		return exitUsage
	}
	policy, sharedFields, err := f.contextOptions(devEndpoint, resolvedProvider)
	if err != nil {
		fmt.Fprintf(c.stderr, "clai: %v\nRun 'clai help' for usage.\n", err)
		return exitUsage
	}

	p, err := selectProvider(resolvedProvider, resolvedModel, *f.apiKey, *f.fallbackRules, policy, sharedFields, devEndpoint)
	if err != nil {
		fmt.Fprintf(c.stderr, "clai: %v\n", err)
		return exitError
	}

	inventory := capability.NewCached("")
	outcome, err := executeTUI(p, inventory, "", devEndpoint)
	if err != nil {
		fmt.Fprintf(c.stderr, "clai: %v\n", err)
		return exitError
	}
	if !outcome.Accepted {
		return exitOK
	}

	// Delivery resolves through the same precedence chain as provider and
	// model, with explicit-set state deciding the flag layer (CONF-03). Only
	// the user-ACCEPTED command is ever delivered, and the clipboard-then-
	// stdout order is preserved so both behaviors compose deterministically.
	copyOut, printOut := config.ResolveDelivery(*f.copyCommand, *f.printCommand, explicit["copy"], explicit["print-command"], os.Getenv("CLAI_DELIVERY"), cfg.Delivery)
	if copyOut {
		if err := clipboardWrite(outcome.Command); err != nil {
			fmt.Fprintf(c.stderr, "clai: copy command: %v\n", err)
			return exitError
		}
	}
	if printOut {
		fmt.Fprintln(c.stdout, outcome.Command)
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
	if fs.NArg() != 0 || !shellinit.Supported(*f.shell) || *f.resultFile == "" {
		fmt.Fprintln(c.stderr, "usage: clai widget --shell <fish|bash|zsh> --result-file <path>\nRun 'clai widget help' for usage.")
		return exitUsage
	}
	// The widget path performs the identical provider and model resolution as
	// the interactive path (CONF-03) — skipping it here would silently break
	// config.json and CLAI_PROVIDER/CLAI_MODEL users invoking through the
	// shell widget. Delivery resolution does not apply in widget mode: the
	// result-file transport is fixed.
	_, resolvedProvider, resolvedModel := c.resolveSettings(f.providerFlags)

	// Validate every usage error together, before touching the result file or
	// constructing a provider (REQ-DEVENDPOINT-002). The endpoint comes first
	// because the context policy defaults on its classification
	// (REQ-CONTEXT-003, REQ-DEVENDPOINT-004).
	devEndpoint, err := f.devEndpointOption(resolvedProvider)
	if err != nil {
		fmt.Fprintf(c.stderr, "clai widget: %v\nRun 'clai widget help' for usage.\n", err)
		return exitUsage
	}
	policy, sharedFields, err := f.contextOptions(devEndpoint, resolvedProvider)
	if err != nil {
		fmt.Fprintf(c.stderr, "clai widget: %v\nRun 'clai widget help' for usage.\n", err)
		return exitUsage
	}
	resultIdentity, err := inspectWidgetResult(*f.resultFile)
	if err != nil {
		fmt.Fprintf(c.stderr, "clai widget: invalid result file: %v\n", err)
		return exitError
	}
	keepResult := false
	defer func() {
		if !keepResult {
			removeWidgetResultIfSame(*f.resultFile, resultIdentity)
		}
	}()

	p, err := selectProvider(resolvedProvider, resolvedModel, *f.apiKey, *f.fallbackRules, policy, sharedFields, devEndpoint)
	if err != nil {
		fmt.Fprintf(c.stderr, "clai widget: %v\n", err)
		return exitError
	}

	inventory := capability.NewCached(*f.shell)
	outcome, err := executeTUI(p, inventory, *f.shell, devEndpoint)
	if err != nil {
		fmt.Fprintf(c.stderr, "clai widget: %v\n", err)
		return exitError
	}
	if !outcome.Accepted {
		return exitCancelled
	}
	// Transport boundary revalidation (REQ-INVARIANT-008): re-check the exact
	// bytes about to be exported against structural, safety, file-identity, and
	// applicability gates independently of the TUI review decision.
	if result := validate.Command(outcome.Command); !result.Valid {
		fmt.Fprintf(c.stderr, "clai widget: accepted command is not exportable: %s\n", textsafe.Visible(strings.Join(result.Reasons, " ")))
		return exitError
	}
	if decision := safety.Evaluate(outcome.Command); decision.Decision == safety.Block {
		fmt.Fprintf(c.stderr, "clai widget: accepted command is blocked by safety and not exportable: %s\n", textsafe.Visible(strings.Join(decision.Reasons, " ")))
		return exitError
	}
	if err := verifyWidgetResult(*f.resultFile, resultIdentity); err != nil {
		fmt.Fprintf(c.stderr, "clai widget: write result: %v\n", err)
		return exitError
	}
	appResult := widgetApplicability(outcome, inventory.Inventory(context.Background()))
	if appResult.Decision == applicability.Rejected {
		fmt.Fprintf(c.stderr, "clai widget: accepted command is not for this shell/OS: %s\n", textsafe.Visible(strings.Join(appResult.Reasons, " ")))
		return exitError
	}
	if err := writeWidgetResult(*f.resultFile, outcome.Command, resultIdentity); err != nil {
		fmt.Fprintf(c.stderr, "clai widget: write result: %v\n", err)
		return exitError
	}
	keepResult = true
	return exitOK
}

// widgetApplicability is the fresh transport-boundary applicability decision.
// The Edited discriminator selects the branch: an unedited outcome reevaluates
// the transported candidate requirements, an edited outcome rederives
// executables from the accepted command bytes and ignores those requirements.
func widgetApplicability(outcome app.Outcome, snapshot capability.Inventory) applicability.Result {
	if outcome.Edited {
		return applicability.EvaluateEdited(outcome.Command, snapshot)
	}
	return applicability.Evaluate(outcome.Requirements, snapshot)
}

// providerFlags holds the flags shared by the interactive and widget commands.
type providerFlags struct {
	providerName  *string
	model         *string
	apiKey        *string
	fallbackRules *bool
	contextPolicy *contextPolicyFlag
	sharedContext *sharedContextFlag
	devEndpoint   *string
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
		providerName:  fs.String("provider", "", "provider: rules | openrouter | anthropic"),
		model:         fs.String("model", "", "model override for LLM providers"),
		apiKey:        fs.String("api-key", "", "API key override for LLM providers"),
		fallbackRules: fs.Bool("fallback-rules", false, "fall back to local rules when the selected provider errors"),
		contextPolicy: policy,
		sharedContext: shared,
		devEndpoint:   fs.String("dev-endpoint", "", "development only: send provider requests to a loopback endpoint"),
	}
}

// resolveSettings loads config.json and resolves the effective provider and
// model through the flag > env > config > built-in precedence chain
// (CONF-03). The model built-in stays "" so each remote provider applies its
// own DefaultModel when no layer names one. A failed load — corrupt file or
// I/O error alike — warns exactly once on stderr and continues with a zero
// Config; config state never changes an exit code (CONF-05). The Load error
// text already carries the quoted path.
func (c cli) resolveSettings(f providerFlags) (config.Config, string, string) {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(c.stderr, "clai: warning: %v; continuing with defaults\n", err)
		cfg = config.Config{}
	}
	resolvedProvider := config.Resolve(*f.providerName, os.Getenv("CLAI_PROVIDER"), cfg.Provider, "rules")
	resolvedModel := config.Resolve(*f.model, os.Getenv("CLAI_MODEL"), cfg.Model, "")
	return cfg, resolvedProvider, resolvedModel
}

// devEndpointOption validates the loopback-only development override
// (REQ-DEVENDPOINT-002/003). Every rejection happens here, before provider
// construction, credential resolution, DNS, or network activity. Classification
// is lexical, so a hostname that merely resolves to loopback is rejected.
// resolvedProvider is the post-precedence provider name (CONF-03): switching
// on the raw flag here would treat a config-selected remote provider as rules.
func (f providerFlags) devEndpointOption(resolvedProvider string) (string, error) {
	endpoint := strings.TrimSpace(*f.devEndpoint)
	if endpoint == "" {
		return "", nil
	}
	switch resolvedProvider {
	case "openrouter", "anthropic":
	default:
		return "", fmt.Errorf("--dev-endpoint is not valid for provider %q; it applies only to openrouter and anthropic", resolvedProvider)
	}
	class, err := machinecontext.ClassifyEndpoint(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid --dev-endpoint: %w", err)
	}
	// ClassifyEndpoint accepts any non-empty scheme; the transport only speaks
	// HTTP, so restrict it here rather than failing later inside the provider.
	if parsed, err := url.Parse(endpoint); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("--dev-endpoint must use http or https, got %q", endpoint)
	}
	if class != machinecontext.EndpointLoopback {
		return "", fmt.Errorf("--dev-endpoint must address a loopback host, got %q", endpoint)
	}
	return endpoint, nil
}

// contextOptions resolves the effective context policy and shared fields.
// devEndpoint is the already-validated loopback development endpoint, or empty
// for a normal session; it is required here because REQ-CONTEXT-003 defaults on
// endpoint classification, and a loopback development endpoint must default to
// local-only exactly as any other loopback provider does (REQ-DEVENDPOINT-004).
// resolvedProvider is the post-precedence provider name (CONF-03) so a
// config-selected remote provider defaults to remote-minimal exactly like a
// flag-selected one.
func (f providerFlags) contextOptions(devEndpoint, resolvedProvider string) (machinecontext.Policy, []string, error) {
	shared, err := machinecontext.NormalizeExplicitFields(f.sharedContext.values)
	if err != nil {
		return "", nil, err
	}
	policy := f.contextPolicy.value
	if !f.contextPolicy.set {
		if len(shared) != 0 {
			return "", nil, errors.New("--share-context requires an explicit --context-policy remote-explicit in the same invocation")
		}
		policy = machinecontext.PolicyLocalOnly
		if devEndpoint == "" {
			switch resolvedProvider {
			case "openrouter", "anthropic":
				policy = machinecontext.PolicyRemoteMinimal
			}
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

func runTUI(p provider.Provider, inventory *capability.Cached, activeShell, devEndpoint string) (app.Outcome, error) {
	model := app.NewFromDeps(app.Deps{Provider: p, ActiveShell: activeShell, InventorySource: inventory, DevEndpoint: devEndpoint})
	options := []tea.ProgramOption{tea.WithOutput(os.Stderr), tea.WithAltScreen()}
	if activeShell != "" {

		tty, err := openControllingTerminal()
		if err != nil {
			return app.Outcome{}, fmt.Errorf("open controlling terminal: %w", err)
		}
		defer tty.Close()
		options = []tea.ProgramOption{tea.WithInput(tty), tea.WithOutput(tty), tea.WithAltScreen()}
	}

	program := tea.NewProgram(model, options...)
	finalModel, err := program.Run()
	if err != nil {
		return app.Outcome{}, err
	}

	result, ok := finalModel.(app.Model)
	if !ok {
		return app.Outcome{}, nil
	}
	return result.Outcome(), nil
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

func removeWidgetResultIfSame(path string, expected os.FileInfo) {
	current, err := os.Lstat(path)
	if err != nil || expected == nil || !os.SameFile(expected, current) {
		return
	}
	_ = os.Remove(path)
}

func verifyWidgetResult(path string, expected os.FileInfo) error {
	before, err := inspectWidgetResult(path)
	if err != nil {
		return err
	}
	if expected == nil || !os.SameFile(expected, before) {
		return errors.New("result file changed while the TUI was open")
	}
	return nil
}

func writeWidgetResult(path, command string, expected os.FileInfo) error {
	before, err := inspectWidgetResult(path)
	if err != nil {
		return err
	}
	if expected == nil || !os.SameFile(expected, before) {
		return errors.New("result file changed while the TUI was open")
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
	if !after.Mode().IsRegular() || !os.SameFile(expected, after) || !os.SameFile(before, after) {
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

func selectProvider(name, model, apiKey string, fallbackRules bool, policy machinecontext.Policy, sharedFields []string, devEndpoint string) (provider.Provider, error) {
	switch name {
	case "rules", "":
		return rules.Provider{}, nil
	case "openrouter":
		key, err := auth.Resolve("openrouter", apiKey)
		if err != nil {
			return nil, err
		}
		var p provider.Provider = openrouter.Provider{APIKey: key, Model: model, Policy: policy, SharedFields: sharedFields, DevEndpoint: devEndpoint}
		if fallbackRules {
			p = fallback{primary: p}
		}
		return p, nil
	case "anthropic":
		key, err := auth.Resolve("anthropic", apiKey)
		if err != nil {
			return nil, err
		}
		var p provider.Provider = anthropic.Provider{APIKey: key, Model: model, Policy: policy, SharedFields: sharedFields, DevEndpoint: devEndpoint}
		if fallbackRules {
			p = fallback{primary: p}
		}
		return p, nil
	case "openai":
		return nil, fmt.Errorf("provider %q is not implemented yet; use anthropic, openrouter, or rules", name)
	default:
		return nil, fmt.Errorf("unknown provider %q (known: rules, openrouter, anthropic)", name)
	}
}
