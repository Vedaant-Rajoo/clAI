package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/Vedaant-Rajoo/clai/internal/app"
	"github.com/Vedaant-Rajoo/clai/internal/applicability"
	"github.com/Vedaant-Rajoo/clai/internal/capability"
	"github.com/Vedaant-Rajoo/clai/internal/config"
	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
	"github.com/Vedaant-Rajoo/clai/internal/provider/anthropic"
	"github.com/Vedaant-Rajoo/clai/internal/provider/openrouter"
	"github.com/Vedaant-Rajoo/clai/internal/provider/rules"
	"github.com/Vedaant-Rajoo/clai/internal/safety"
	"github.com/Vedaant-Rajoo/clai/internal/shellinit"
	"github.com/Vedaant-Rajoo/clai/internal/validate"
)

func TestSelectProviderDefaultIsRules(t *testing.T) {
	p, err := selectProvider("rules", "", "", false, machinecontext.PolicyLocalOnly, nil, "")
	if err != nil {
		t.Fatalf("selectProvider: %v", err)
	}
	if _, ok := p.(rules.Provider); !ok {
		t.Errorf("got %T, want rules.Provider", p)
	}
}

func TestSelectProviderOpenRouterUsesEnvKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")
	p, err := selectProvider("openrouter", "some/model", "", false, machinecontext.PolicyRemoteMinimal, nil, "")
	if err != nil {
		t.Fatalf("selectProvider: %v", err)
	}
	orp, ok := p.(openrouter.Provider)
	if !ok {
		t.Fatalf("got %T, want openrouter.Provider", p)
	}
	if orp.APIKey != "sk-or-test" {
		t.Errorf("APIKey = %q, want env key", orp.APIKey)
	}
	if orp.Model != "some/model" {
		t.Errorf("Model = %q, want some/model", orp.Model)
	}
}

func TestSelectProviderOpenRouterExplicitKeyWins(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "env-key")
	p, err := selectProvider("openrouter", "", "flag-key", false, machinecontext.PolicyRemoteMinimal, nil, "")
	if err != nil {
		t.Fatalf("selectProvider: %v", err)
	}
	if p.(openrouter.Provider).APIKey != "flag-key" {
		t.Errorf("APIKey = %q, want flag-key", p.(openrouter.Provider).APIKey)
	}
}

func TestSelectProviderFallbackWraps(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")
	p, err := selectProvider("openrouter", "", "", true, machinecontext.PolicyRemoteMinimal, nil, "")
	if err != nil {
		t.Fatalf("selectProvider: %v", err)
	}
	if _, ok := p.(fallback); !ok {
		t.Errorf("got %T, want fallback wrapper", p)
	}
}

func TestSelectProviderUnknown(t *testing.T) {
	if _, err := selectProvider("bogus", "", "", false, machinecontext.PolicyLocalOnly, nil, ""); err == nil {
		t.Error("want error for unknown provider")
	}
}

func TestSelectProviderUnimplemented(t *testing.T) {
	if _, err := selectProvider("openai", "", "", false, machinecontext.PolicyRemoteMinimal, nil, ""); err == nil {
		t.Error("want error for unimplemented provider")
	}
}

func TestSelectProviderAnthropicUsesEnvKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	p, err := selectProvider("anthropic", "claude-opus-4-8", "", false, machinecontext.PolicyRemoteMinimal, nil, "")
	if err != nil {
		t.Fatalf("selectProvider: %v", err)
	}
	ap, ok := p.(anthropic.Provider)
	if !ok {
		t.Fatalf("got %T, want anthropic.Provider", p)
	}
	if ap.APIKey != "sk-ant-test" {
		t.Errorf("APIKey = %q, want env key", ap.APIKey)
	}
	if ap.Model != "claude-opus-4-8" {
		t.Errorf("Model = %q, want claude-opus-4-8", ap.Model)
	}
	if ap.Policy != machinecontext.PolicyRemoteMinimal {
		t.Errorf("Policy = %q, want remote-minimal", ap.Policy)
	}
}

func TestAnthropicModelDefaultAndOverride(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Run("default left empty for provider default", func(t *testing.T) {
		p, err := selectProvider("anthropic", "", "", false, machinecontext.PolicyRemoteMinimal, nil, "")
		if err != nil {
			t.Fatalf("selectProvider: %v", err)
		}
		if got := p.(anthropic.Provider).Model; got != "" {
			t.Fatalf("Model = %q, want empty so provider applies %q", got, anthropic.DefaultModel)
		}
	})
	t.Run("CLI override forwarded verbatim", func(t *testing.T) {
		p, err := selectProvider("anthropic", "claude-opus-4-8", "", false, machinecontext.PolicyRemoteMinimal, nil, "")
		if err != nil {
			t.Fatalf("selectProvider: %v", err)
		}
		if got := p.(anthropic.Provider).Model; got != "claude-opus-4-8" {
			t.Fatalf("Model = %q, want claude-opus-4-8", got)
		}
	})
}

func TestSelectProviderAnthropicExplicitKeyWins(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	p, err := selectProvider("anthropic", "", "flag-key", false, machinecontext.PolicyRemoteMinimal, nil, "")
	if err != nil {
		t.Fatalf("selectProvider: %v", err)
	}
	if p.(anthropic.Provider).APIKey != "flag-key" {
		t.Errorf("APIKey = %q, want flag-key", p.(anthropic.Provider).APIKey)
	}
}

func TestSelectProviderAnthropicFallbackWraps(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	p, err := selectProvider("anthropic", "", "", true, machinecontext.PolicyRemoteMinimal, nil, "")
	if err != nil {
		t.Fatalf("selectProvider: %v", err)
	}
	if _, ok := p.(fallback); !ok {
		t.Errorf("got %T, want fallback wrapper", p)
	}
}

func TestSelectProviderAnthropicSharedFieldsForwarded(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	shared := []string{machinecontext.FieldWorkingDirectory}
	p, err := selectProvider("anthropic", "", "", false, machinecontext.PolicyRemoteExplicit, shared, "")
	if err != nil {
		t.Fatalf("selectProvider: %v", err)
	}
	ap := p.(anthropic.Provider)
	if len(ap.SharedFields) != 1 || ap.SharedFields[0] != machinecontext.FieldWorkingDirectory {
		t.Errorf("SharedFields = %v, want [working_directory]", ap.SharedFields)
	}
}

// captureSelectedProvider swaps the executeTUI seam so tests can observe
// exactly which provider the command constructed, without running a real TUI.
// The returned pointer is filled in when the command reaches the TUI boundary.
func captureSelectedProvider(t *testing.T) *provider.Provider {
	t.Helper()
	var got provider.Provider
	original := executeTUI
	executeTUI = func(p provider.Provider, _ *capability.Cached, _, _ string) (app.Outcome, error) {
		got = p
		return app.Outcome{}, nil
	}
	t.Cleanup(func() { executeTUI = original })
	return &got
}

// swapLoadConfig swaps the loadConfig seam, mirroring the executeTUI pattern,
// so cmd tests control exactly what config.Load appears to return.
func swapLoadConfig(t *testing.T, cfg config.Config, err error) {
	t.Helper()
	original := loadConfig
	loadConfig = func() (config.Config, error) { return cfg, err }
	t.Cleanup(func() { loadConfig = original })
}

// TestProviderPrecedenceEndToEnd is the tracer proof for CONF-01/CONF-03: a
// config.json provider value selects the constructed provider at the
// selectProvider boundary, env overrides config, and an explicit flag
// overrides both — in the interactive path.
func TestProviderPrecedenceEndToEnd(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")

	cases := []struct {
		name           string
		cfg            config.Config
		envProvider    string
		args           []string
		wantOpenRouter bool
	}{
		{
			name:           "config selects provider when flag and env are unset",
			cfg:            config.Config{Contract: config.ConfigContract, Provider: "openrouter"},
			envProvider:    "",
			wantOpenRouter: true,
		},
		{
			name:           "env overrides config",
			cfg:            config.Config{Contract: config.ConfigContract, Provider: "openrouter"},
			envProvider:    "rules",
			wantOpenRouter: false,
		},
		{
			name:           "flag overrides env and config",
			cfg:            config.Config{Contract: config.ConfigContract, Provider: "openrouter"},
			envProvider:    "openrouter",
			args:           []string{"--provider", "rules"},
			wantOpenRouter: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAI_PROVIDER", tc.envProvider)
			swapLoadConfig(t, tc.cfg, nil)
			got := captureSelectedProvider(t)

			c, _, errBuf := captureCLI()
			if code := c.run(tc.args); code != exitOK {
				t.Fatalf("run(%v) = %d, want %d; stderr: %s", tc.args, code, exitOK, errBuf.String())
			}
			if *got == nil {
				t.Fatal("executeTUI was not reached")
			}
			_, isOpenRouter := (*got).(openrouter.Provider)
			if isOpenRouter != tc.wantOpenRouter {
				t.Fatalf("provider = %T, want openrouter=%v", *got, tc.wantOpenRouter)
			}
			if !tc.wantOpenRouter {
				if _, isRules := (*got).(rules.Provider); !isRules {
					t.Fatalf("provider = %T, want rules.Provider", *got)
				}
			}
		})
	}
}

// TestWidgetProviderPrecedenceUsesConfig proves the widget path performs the
// identical resolution: a config-selected provider reaches selectProvider even
// though no flag or env names it (Pitfall 3 — the widget path must not be
// skipped by the rewiring).
func TestWidgetProviderPrecedenceUsesConfig(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")
	t.Setenv("CLAI_PROVIDER", "")
	swapLoadConfig(t, config.Config{Contract: config.ConfigContract, Provider: "openrouter"}, nil)
	got := captureSelectedProvider(t)

	path := filepath.Join(t.TempDir(), "result")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	c, _, errBuf := captureCLI()
	code := c.run([]string{"widget", "--shell", "fish", "--result-file", path})
	if code != exitCancelled {
		t.Fatalf("run(widget) = %d, want %d; stderr: %s", code, exitCancelled, errBuf.String())
	}
	if *got == nil {
		t.Fatal("executeTUI was not reached")
	}
	if _, ok := (*got).(openrouter.Provider); !ok {
		t.Fatalf("provider = %T, want openrouter.Provider from config", *got)
	}
}

// swapClipboardWrite swaps the clipboardWrite seam so tests observe copy
// behavior without touching the system clipboard. The returned slice pointer
// collects every write in order.
func swapClipboardWrite(t *testing.T) *[]string {
	t.Helper()
	var writes []string
	original := clipboardWrite
	clipboardWrite = func(text string) error {
		writes = append(writes, text)
		return nil
	}
	t.Cleanup(func() { clipboardWrite = original })
	return &writes
}

// acceptOutcome swaps the executeTUI seam to report the given command as
// accepted, so cmd tests can exercise the post-TUI delivery block.
func acceptOutcome(t *testing.T, command string) {
	t.Helper()
	original := executeTUI
	executeTUI = func(provider.Provider, *capability.Cached, string, string) (app.Outcome, error) {
		return app.Outcome{Command: command, Accepted: true}, nil
	}
	t.Cleanup(func() { executeTUI = original })
}

// TestDeliveryFromConfig locks the CONF-03 delivery matrix at the cmd level:
// a config delivery value drives copy/print behavior when no delivery flag is
// passed, an explicit --copy=false overrides config even though false equals
// the flag default (explicit-set adjacency edge), and CLAI_DELIVERY sits
// between flags and config.
func TestDeliveryFromConfig(t *testing.T) {
	t.Setenv("CLAI_PROVIDER", "")
	t.Setenv("CLAI_MODEL", "")
	const command = "go test ./..."

	cases := []struct {
		name       string
		cfg        config.Config
		env        string
		args       []string
		wantCopies int
		wantStdout string
	}{
		{
			name:       "config clipboard copies without printing",
			cfg:        config.Config{Contract: config.ConfigContract, Delivery: "clipboard"},
			wantCopies: 1,
			wantStdout: "",
		},
		{
			name:       "explicit copy=false beats config clipboard",
			cfg:        config.Config{Contract: config.ConfigContract, Delivery: "clipboard"},
			args:       []string{"--copy=false"},
			wantCopies: 0,
			wantStdout: "",
		},
		{
			name:       "config stdout prints the accepted command",
			cfg:        config.Config{Contract: config.ConfigContract, Delivery: "stdout"},
			wantCopies: 0,
			wantStdout: command + "\n",
		},
		{
			name:       "env stdout beats config clipboard",
			cfg:        config.Config{Contract: config.ConfigContract, Delivery: "clipboard"},
			env:        "stdout",
			wantCopies: 0,
			wantStdout: command + "\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAI_DELIVERY", tc.env)
			swapLoadConfig(t, tc.cfg, nil)
			acceptOutcome(t, command)
			writes := swapClipboardWrite(t)

			c, out, errBuf := captureCLI()
			if code := c.run(tc.args); code != exitOK {
				t.Fatalf("run(%v) = %d, want %d; stderr: %s", tc.args, code, exitOK, errBuf.String())
			}
			if len(*writes) != tc.wantCopies {
				t.Fatalf("clipboard writes = %v, want %d write(s)", *writes, tc.wantCopies)
			}
			if tc.wantCopies > 0 && (*writes)[0] != command {
				t.Fatalf("clipboard write = %q, want %q", (*writes)[0], command)
			}
			if out.String() != tc.wantStdout {
				t.Fatalf("stdout = %q, want %q", out.String(), tc.wantStdout)
			}
		})
	}
}

// TestPrecedenceMatrix locks the model dimension of CONF-03 end-to-end: the
// model reaching selectProvider resolves flag > CLAI_MODEL env > config.json,
// and when nothing is set the resolved model stays empty so the provider's
// own DefaultModel applies (the built-in must remain "").
func TestPrecedenceMatrix(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")
	t.Setenv("CLAI_PROVIDER", "")
	t.Setenv("CLAI_DELIVERY", "")

	cases := []struct {
		name      string
		cfg       config.Config
		envModel  string
		args      []string
		wantModel string
	}{
		{
			name:      "config model when flag and env unset",
			cfg:       config.Config{Contract: config.ConfigContract, Provider: "openrouter", Model: "anthropic/config-model"},
			wantModel: "anthropic/config-model",
		},
		{
			name:      "env beats config",
			cfg:       config.Config{Contract: config.ConfigContract, Provider: "openrouter", Model: "anthropic/config-model"},
			envModel:  "anthropic/env-model",
			wantModel: "anthropic/env-model",
		},
		{
			name:      "flag beats env and config",
			cfg:       config.Config{Contract: config.ConfigContract, Provider: "openrouter", Model: "anthropic/config-model"},
			envModel:  "anthropic/env-model",
			args:      []string{"--model", "anthropic/flag-model"},
			wantModel: "anthropic/flag-model",
		},
		{
			name:      "nothing set leaves model empty for provider default",
			cfg:       config.Config{Contract: config.ConfigContract, Provider: "openrouter"},
			wantModel: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAI_MODEL", tc.envModel)
			swapLoadConfig(t, tc.cfg, nil)
			got := captureSelectedProvider(t)

			c, _, errBuf := captureCLI()
			if code := c.run(tc.args); code != exitOK {
				t.Fatalf("run(%v) = %d, want %d; stderr: %s", tc.args, code, exitOK, errBuf.String())
			}
			orp, ok := (*got).(openrouter.Provider)
			if !ok {
				t.Fatalf("provider = %T, want openrouter.Provider", *got)
			}
			if orp.Model != tc.wantModel {
				t.Fatalf("Model = %q, want %q", orp.Model, tc.wantModel)
			}
		})
	}
}

// TestWidgetConfigPrecedence proves the widget path resolves the model
// identically to the interactive path: a config model reaches selectProvider
// with no flag or env naming it (delivery does not apply in widget mode — the
// result-file transport is fixed).
func TestWidgetConfigPrecedence(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")
	t.Setenv("CLAI_PROVIDER", "")
	t.Setenv("CLAI_MODEL", "")
	swapLoadConfig(t, config.Config{Contract: config.ConfigContract, Provider: "openrouter", Model: "anthropic/config-model"}, nil)
	got := captureSelectedProvider(t)

	path := filepath.Join(t.TempDir(), "result")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	c, _, errBuf := captureCLI()
	code := c.run([]string{"widget", "--shell", "fish", "--result-file", path})
	if code != exitCancelled {
		t.Fatalf("run(widget) = %d, want %d; stderr: %s", code, exitCancelled, errBuf.String())
	}
	orp, ok := (*got).(openrouter.Provider)
	if !ok {
		t.Fatalf("provider = %T, want openrouter.Provider from config", *got)
	}
	if orp.Model != "anthropic/config-model" {
		t.Fatalf("Model = %q, want config model", orp.Model)
	}
}

// TestCorruptConfigWarnsOnceAndContinues locks the CONF-05 cmd contract: any
// non-nil Load error — corrupt file or plain I/O failure — produces exactly
// one stderr warning, leaves stdout untouched, keeps the exit code identical
// to a no-config run, and falls back to the built-in rules provider.
func TestCorruptConfigWarnsOnceAndContinues(t *testing.T) {
	t.Setenv("CLAI_PROVIDER", "")

	// Baseline: a missing config (zero Config, nil error) is fully silent.
	swapLoadConfig(t, config.Config{}, nil)
	baselineGot := captureSelectedProvider(t)
	c, out, errBuf := captureCLI()
	baselineCode := c.run(nil)
	if baselineCode != exitOK {
		t.Fatalf("baseline run = %d, want %d; stderr: %s", baselineCode, exitOK, errBuf.String())
	}
	if out.String() != "" || errBuf.String() != "" {
		t.Fatalf("baseline streams not empty: stdout=%q stderr=%q", out.String(), errBuf.String())
	}
	if _, ok := (*baselineGot).(rules.Provider); !ok {
		t.Fatalf("baseline provider = %T, want rules.Provider", *baselineGot)
	}

	warning := regexp.MustCompile(`clai: warning: .*continuing with defaults`)
	cases := []struct {
		name    string
		loadErr error
	}{
		{
			name:    "corrupt config file",
			loadErr: fmt.Errorf("parse config file %q: %w (invalid character 'n')", "/tmp/config.json", config.ErrCorrupt),
		},
		{
			name:    "non-corrupt IO error",
			loadErr: fmt.Errorf("read config file %q: %w", "/tmp/config.json", errors.New("permission denied")),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			swapLoadConfig(t, config.Config{}, tc.loadErr)
			got := captureSelectedProvider(t)

			c, out, errBuf := captureCLI()
			code := c.run(nil)
			if code != baselineCode {
				t.Fatalf("exit = %d, want the no-config exit %d", code, baselineCode)
			}
			if out.String() != "" {
				t.Fatalf("stdout = %q, want empty (warning must not corrupt piped output)", out.String())
			}
			lines := warning.FindAllString(errBuf.String(), -1)
			if len(lines) != 1 {
				t.Fatalf("stderr warnings = %d, want exactly one; stderr: %q", len(lines), errBuf.String())
			}
			if _, ok := (*got).(rules.Provider); !ok {
				t.Fatalf("provider = %T, want rules.Provider default", *got)
			}
		})
	}
}

type failingProvider struct{ calls *int }

func (p failingProvider) Compile(context.Context, provider.Request) ([]provider.Candidate, error) {
	if p.calls != nil {
		*p.calls++
	}
	return nil, errors.New("boom")
}

func TestFallbackFallsBackToRules(t *testing.T) {
	calls := 0
	p := fallback{primary: failingProvider{calls: &calls}}
	candidates, err := p.Compile(context.Background(), provider.Request{Intent: "run tests"})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(candidates) == 0 || candidates[0].Command != "go test ./..." {
		t.Errorf("candidates = %+v, want rules result", candidates)
	}
	if calls != 1 {
		t.Fatalf("remote primary calls = %d, want exactly one before local fallback", calls)
	}
}

type contextErrorProvider struct {
	err error
}

func (p contextErrorProvider) Compile(context.Context, provider.Request) ([]provider.Candidate, error) {
	return nil, p.err
}

func TestFallbackDoesNotConvertContextErrorsToRules(t *testing.T) {
	for _, wantErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(wantErr.Error(), func(t *testing.T) {
			candidates, err := (fallback{primary: contextErrorProvider{err: wantErr}}).Compile(
				context.Background(), provider.Request{Intent: "run tests"},
			)
			if !errors.Is(err, wantErr) {
				t.Fatalf("err = %v, want %v", err, wantErr)
			}
			if len(candidates) != 0 {
				t.Fatalf("candidates = %+v, want none", candidates)
			}
		})
	}
}

type noSuggestionProvider struct{}

func (noSuggestionProvider) Compile(context.Context, provider.Request) ([]provider.Candidate, error) {
	return nil, nil
}

func TestFallbackPassesThroughNoSuggestion(t *testing.T) {
	candidates, err := (fallback{primary: noSuggestionProvider{}}).Compile(
		context.Background(), provider.Request{Intent: "run tests"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %+v, want primary no-suggestion result", candidates)
	}
}

func TestFallbackPassesThroughSuccess(t *testing.T) {
	p := fallback{primary: rules.Provider{}}
	candidates, err := p.Compile(context.Background(), provider.Request{Intent: "run tests"})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("no candidates")
	}
}

func TestValidShell(t *testing.T) {
	for _, shell := range []string{"fish", "bash", "zsh"} {
		if !shellinit.Supported(shell) {
			t.Errorf("shellinit.Supported(%q) = false", shell)
		}
	}
	for _, shell := range []string{"", "sh", "/bin/fish", "FISH"} {
		if shellinit.Supported(shell) {
			t.Errorf("shellinit.Supported(%q) = true", shell)
		}
	}
}

func TestWidgetRequiresShellAndResultFile(t *testing.T) {
	c, _, _ := captureCLI()
	if code := c.run([]string{"widget"}); code != exitUsage {
		t.Errorf("run(widget) = %d, want %d", code, exitUsage)
	}
	if code := c.run([]string{"widget", "--shell", "sh", "--result-file", "/tmp/result"}); code != exitUsage {
		t.Errorf("run(widget invalid shell) = %d, want %d", code, exitUsage)
	}
}

func TestWidgetRejectsUnsafeResultBeforeTUI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result")
	if err := os.WriteFile(path, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}

	called := false
	original := executeTUI
	executeTUI = func(provider.Provider, *capability.Cached, string, string) (app.Outcome, error) {
		called = true
		return app.Outcome{Command: "pwd", Accepted: true}, nil
	}
	t.Cleanup(func() { executeTUI = original })

	c, _, _ := captureCLI()
	code := c.run([]string{"widget", "--shell", "fish", "--result-file", path})
	if code != exitError {
		t.Fatalf("run(widget) = %d, want %d", code, exitError)
	}
	if called {
		t.Fatal("TUI ran for an invalid result target")
	}
}

func TestWidgetAcceptedWritesExactResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	original := executeTUI
	executeTUI = func(provider.Provider, *capability.Cached, string, string) (app.Outcome, error) {
		return app.Outcome{
			Command:  `printf '%s' 'hello * $world 日本語'`,
			Accepted: true,
			Requirements: []capability.Requirement{{
				Kind: capability.RequirementTool,
				Name: "definitely_missing_clai_tool",
			}},
		}, nil
	}
	t.Cleanup(func() { executeTUI = original })

	c, _, errBuf := captureCLI()
	code := c.run([]string{"widget", "--shell", "fish", "--result-file", path})
	if code != exitOK {
		t.Fatalf("run(widget) = %d, want %d; stderr: %s", code, exitOK, errBuf.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `printf '%s' 'hello * $world 日本語'`
	if string(got) != want {
		t.Errorf("result = %q, want %q", got, want)
	}
}

func TestWidgetCancelledRemovesResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	original := executeTUI
	executeTUI = func(provider.Provider, *capability.Cached, string, string) (app.Outcome, error) {
		return app.Outcome{}, nil
	}
	t.Cleanup(func() { executeTUI = original })

	c, _, _ := captureCLI()
	code := c.run([]string{"widget", "--shell", "zsh", "--result-file", path})
	if code != exitCancelled {
		t.Fatalf("run(widget) = %d, want %d", code, exitCancelled)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("result file was not removed: %v", err)
	}
}

func TestWidgetErrorRemovesResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	original := executeTUI
	executeTUI = func(provider.Provider, *capability.Cached, string, string) (app.Outcome, error) {
		return app.Outcome{}, errors.New("tui failed")
	}
	t.Cleanup(func() { executeTUI = original })

	c, _, _ := captureCLI()
	code := c.run([]string{"widget", "--shell", "bash", "--result-file", path})
	if code != exitError {
		t.Fatalf("run(widget) = %d, want %d", code, exitError)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("result file was not removed: %v", err)
	}
}

// TestWidgetBlockedCommandNotExportedAtBoundary proves the transport boundary
// independently enforces the safety gate (REQ-INVARIANT-004): even if the TUI
// reports a structurally valid but safety-blocked command as accepted, widget
// export refuses it and never writes the result file.
func TestWidgetBlockedCommandNotExportedAtBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	const blocked = "rm -rf /"
	if result := validate.Command(blocked); !result.Valid {
		t.Fatalf("precondition: %q must be structurally valid so only the safety gate can refuse it", blocked)
	}
	if decision := safety.Evaluate(blocked); decision.Decision != safety.Block {
		t.Fatalf("precondition: %q must be safety-blocked, got %v", blocked, decision.Decision)
	}

	original := executeTUI
	executeTUI = func(provider.Provider, *capability.Cached, string, string) (app.Outcome, error) {
		return app.Outcome{Command: blocked, Accepted: true}, nil
	}
	t.Cleanup(func() { executeTUI = original })

	c, _, errBuf := captureCLI()
	code := c.run([]string{"widget", "--shell", "fish", "--result-file", path})
	if code != exitError {
		t.Fatalf("run(widget) = %d, want %d; stderr: %s", code, exitError, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "blocked by safety") {
		t.Fatalf("stderr = %q, want it to report the safety block", errBuf.String())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocked command left a result file behind: %v", err)
	}
}

func TestWidgetRejectsResultFileReplacementDuringTUI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result")
	originalPath := filepath.Join(dir, "original")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	original := executeTUI
	executeTUI = func(provider.Provider, *capability.Cached, string, string) (app.Outcome, error) {
		if err := os.Rename(path, originalPath); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		return app.Outcome{Command: "pwd", Accepted: true}, nil
	}
	t.Cleanup(func() { executeTUI = original })

	c, _, errBuf := captureCLI()
	code := c.run([]string{"widget", "--shell", "fish", "--result-file", path})
	if code != exitError {
		t.Fatalf("run(widget) = %d, want %d; stderr: %s", code, exitError, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "changed while the TUI was open") {
		t.Fatalf("stderr = %q, want identity-change error", errBuf.String())
	}
	if data, err := os.ReadFile(path); err != nil || len(data) != 0 {
		t.Fatalf("replacement file changed or removed: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(originalPath); err != nil {
		t.Fatalf("original inspected file missing: %v", err)
	}
}

func TestWriteWidgetResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	expected, err := inspectWidgetResult(path)
	if err != nil {
		t.Fatal(err)
	}
	const command = `printf '%s' "hello * world"`
	if err := writeWidgetResult(path, command, expected); err != nil {
		t.Fatalf("writeWidgetResult: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != command {
		t.Errorf("result = %q, want %q", got, command)
	}
}

func TestWriteWidgetResultRejectsUnsafeTargets(t *testing.T) {
	t.Run("relative", func(t *testing.T) {
		if err := writeWidgetResult("result", "pwd", nil); err == nil {
			t.Fatal("expected relative path error")
		}
	})

	t.Run("nonempty", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "result")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeWidgetResult(path, "pwd", nil); err == nil {
			t.Fatal("expected nonempty file error")
		}
	})

	t.Run("permissions", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "result")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeWidgetResult(path, "pwd", nil); err == nil {
			t.Fatal("expected permissions error")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		link := filepath.Join(dir, "result")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err := writeWidgetResult(link, "pwd", nil); err == nil {
			t.Fatal("expected symlink error")
		}
	})
}

func TestApplicabilityHardRejectPreservesWidgetBuffer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	original := executeTUI
	executeTUI = func(_ provider.Provider, inventory *capability.Cached, shell, _ string) (app.Outcome, error) {
		if shell != "fish" {
			t.Fatalf("shell = %q, want fish", shell)
		}
		_ = inventory.Inventory(context.Background())
		return app.Outcome{
			Command:  "pwd",
			Accepted: true,
			Requirements: []capability.Requirement{{
				Kind: capability.RequirementShell,
				Name: "bash",
			}},
		}, nil
	}
	t.Cleanup(func() { executeTUI = original })

	c, _, errBuf := captureCLI()
	code := c.run([]string{"widget", "--shell", "fish", "--result-file", path})
	if code != exitError {
		t.Fatalf("run(widget) = %d, want %d; stderr: %s", code, exitError, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "not for this shell/OS") {
		t.Fatalf("stderr = %q, want applicability rejection", errBuf.String())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hard-rejected command left result transport behind: %v", err)
	}
}

func TestWidgetReDerivesEditedExecutables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	original := executeTUI
	executeTUI = func(_ provider.Provider, _ *capability.Cached, _, _ string) (app.Outcome, error) {
		return app.Outcome{
			Command:  "pwd",
			Accepted: true,
			Edited:   true,
			Requirements: []capability.Requirement{{
				Kind: capability.RequirementShell,
				Name: "bash",
			}},
		}, nil
	}
	t.Cleanup(func() { executeTUI = original })

	c, _, errBuf := captureCLI()
	code := c.run([]string{"widget", "--shell", "fish", "--result-file", path})
	if code != exitOK {
		t.Fatalf("run(widget) = %d, want %d; stderr: %s", code, exitOK, errBuf.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "pwd" {
		t.Fatalf("result = %q, want exact edited command", data)
	}
}

func TestWidgetApplicabilityBranchesAndEditedDerivationObservable(t *testing.T) {
	inventory := capability.NewFixtureInventory(
		"linux", "amd64",
		capability.ShellIdentity{Family: capability.ShellFish},
		[]capability.ToolFact{{Name: "grep", Present: true}},
	)
	hardRequirement := []capability.Requirement{{Kind: capability.RequirementShell, Name: "bash"}}

	unedited := widgetApplicability(app.Outcome{Command: "pwd", Requirements: hardRequirement}, inventory)
	if unedited.Decision != applicability.Rejected {
		t.Fatalf("unedited branch = %+v, want fresh hard rejection from transported requirements", unedited)
	}

	// The edited branch must ignore the transported hard requirement and
	// rederive executables from the accepted bytes: the soft-mark reason names
	// the derived base name, proving the parse/resolve walk actually ran.
	edited := widgetApplicability(app.Outcome{
		Command:      "env FOO=bar /opt/tools/definitely-absent-tool --flag | grep x",
		Edited:       true,
		Requirements: hardRequirement,
	}, inventory)
	if edited.Decision != applicability.Marked {
		t.Fatalf("edited branch = %+v, want soft mark for derived missing executable", edited)
	}
	found := false
	for _, reason := range edited.Reasons {
		if strings.Contains(reason, "definitely-absent-tool") {
			found = true
		}
		if strings.Contains(reason, "/opt/tools") {
			t.Fatalf("edited reason leaked an executable path: %q", reason)
		}
		if strings.Contains(reason, "bash") {
			t.Fatalf("edited branch consulted discarded model requirements: %q", reason)
		}
	}
	if !found {
		t.Fatalf("edited reasons = %v, want derived base name naming the missing executable", edited.Reasons)
	}
}

type realConfigRootFixture struct {
	home      string
	xdg       string
	preferred string
	legacy    string
}

// TestRealConfigRootPrecedence restores the production config.Load seam and
// proves provider, model, delivery, explicit-default, environment, and widget
// resolution against real isolated config-file/v1 bytes (REQ-CONFIG-008).
func TestRealConfigRootPrecedence(t *testing.T) {
	t.Run("config layer drives provider model delivery and init state", func(t *testing.T) {
		fixture := useRealConfigRoot(t)
		writeRealConfigFixture(t, fixture.preferred, `{
  "contract": "config-file/v1",
  "provider": "openrouter",
  "model": "config-model",
  "delivery": "stdout",
  "init_completed": true
}
`)
		t.Setenv("OPENROUTER_API_KEY", "isolated-openrouter-key")
		const command = "printf config-layer"
		got := captureSelectedProviderWithOutcome(t, app.Outcome{Command: command, Accepted: true})
		writes := swapClipboardWrite(t)

		c, out, errBuf := captureCLI()
		if code := c.run(nil); code != exitOK {
			t.Fatalf("run = %d, want %d; stderr: %s", code, exitOK, errBuf.String())
		}
		selected, ok := (*got).(openrouter.Provider)
		if !ok || selected.Model != "config-model" {
			t.Fatalf("provider = %#v (%T), want openrouter model config-model", *got, *got)
		}
		if out.String() != command+"\n" || errBuf.String() != "" || len(*writes) != 0 {
			t.Fatalf("delivery streams/writes = stdout %q stderr %q clipboard %v", out.String(), errBuf.String(), *writes)
		}
		loaded, err := config.Load()
		if err != nil {
			t.Fatalf("load real config: %v", err)
		}
		if loaded.Contract != config.ConfigContract || !loaded.InitCompleted {
			t.Fatalf("loaded config = %+v, want config-file/v1 with init_completed", loaded)
		}
	})

	t.Run("environment overrides real config", func(t *testing.T) {
		fixture := useRealConfigRoot(t)
		writeRealConfigFixture(t, fixture.preferred, `{
  "contract": "config-file/v1",
  "provider": "rules",
  "model": "config-model",
  "delivery": "clipboard",
  "init_completed": true
}
`)
		t.Setenv("CLAI_PROVIDER", "openrouter")
		t.Setenv("CLAI_MODEL", "env-model")
		t.Setenv("CLAI_DELIVERY", "stdout")
		t.Setenv("OPENROUTER_API_KEY", "isolated-openrouter-key")
		const command = "printf env-layer"
		got := captureSelectedProviderWithOutcome(t, app.Outcome{Command: command, Accepted: true})
		writes := swapClipboardWrite(t)

		c, out, errBuf := captureCLI()
		if code := c.run(nil); code != exitOK {
			t.Fatalf("run = %d, want %d; stderr: %s", code, exitOK, errBuf.String())
		}
		selected, ok := (*got).(openrouter.Provider)
		if !ok || selected.Model != "env-model" {
			t.Fatalf("provider = %#v (%T), want environment-selected openrouter model env-model", *got, *got)
		}
		if out.String() != command+"\n" || errBuf.String() != "" || len(*writes) != 0 {
			t.Fatalf("environment delivery = stdout %q stderr %q clipboard %v", out.String(), errBuf.String(), *writes)
		}
	})

	t.Run("flag values equal to built-in defaults remain explicit", func(t *testing.T) {
		fixture := useRealConfigRoot(t)
		writeRealConfigFixture(t, fixture.preferred, `{
  "contract": "config-file/v1",
  "provider": "openrouter",
  "model": "config-model",
  "delivery": "clipboard",
  "init_completed": true
}
`)
		got := captureSelectedProviderWithOutcome(t, app.Outcome{Command: "printf explicit-default", Accepted: true})
		writes := swapClipboardWrite(t)

		c, out, errBuf := captureCLI()
		if code := c.run([]string{"--provider", "rules", "--copy=false"}); code != exitOK {
			t.Fatalf("run = %d, want %d; stderr: %s", code, exitOK, errBuf.String())
		}
		if _, ok := (*got).(rules.Provider); !ok {
			t.Fatalf("provider = %T, want explicit rules value to beat real config", *got)
		}
		if out.String() != "" || errBuf.String() != "" || len(*writes) != 0 {
			t.Fatalf("explicit defaults = stdout %q stderr %q clipboard %v, want no delivery", out.String(), errBuf.String(), *writes)
		}
	})

	t.Run("model flag beats environment and real config", func(t *testing.T) {
		fixture := useRealConfigRoot(t)
		writeRealConfigFixture(t, fixture.preferred, `{
  "contract": "config-file/v1",
  "provider": "openrouter",
  "model": "config-model",
  "init_completed": true
}
`)
		t.Setenv("CLAI_MODEL", "env-model")
		t.Setenv("OPENROUTER_API_KEY", "isolated-openrouter-key")
		got := captureSelectedProvider(t)

		c, _, errBuf := captureCLI()
		if code := c.run([]string{"--model", "flag-model"}); code != exitOK {
			t.Fatalf("run = %d, want %d; stderr: %s", code, exitOK, errBuf.String())
		}
		selected, ok := (*got).(openrouter.Provider)
		if !ok || selected.Model != "flag-model" {
			t.Fatalf("provider = %#v (%T), want flag model", *got, *got)
		}
	})

	t.Run("widget resolves model from the real preferred file", func(t *testing.T) {
		fixture := useRealConfigRoot(t)
		writeRealConfigFixture(t, fixture.preferred, `{
  "contract": "config-file/v1",
  "provider": "openrouter",
  "model": "widget-config-model",
  "delivery": "clipboard",
  "init_completed": true
}
`)
		t.Setenv("OPENROUTER_API_KEY", "isolated-openrouter-key")
		got := captureSelectedProvider(t)
		resultPath := filepath.Join(t.TempDir(), "result")
		if err := os.WriteFile(resultPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}

		c, out, errBuf := captureCLI()
		code := c.run([]string{"widget", "--shell", "fish", "--result-file", resultPath})
		if code != exitCancelled {
			t.Fatalf("widget exit = %d, want %d; stderr: %s", code, exitCancelled, errBuf.String())
		}
		selected, ok := (*got).(openrouter.Provider)
		if !ok || selected.Model != "widget-config-model" {
			t.Fatalf("widget provider = %#v (%T), want preferred model", *got, *got)
		}
		if out.String() != "" || errBuf.String() != "" {
			t.Fatalf("widget streams = stdout %q stderr %q, want silent cancellation", out.String(), errBuf.String())
		}
	})
}

// TestRealConfigRootCorruptPreferredNeverFallsBack proves the no-seam cmd path
// warns once, defaults to rules, retains corrupt preferred bytes, and never
// exposes a valid legacy sentinel (REQ-CONFIG-003/008).
func TestRealConfigRootCorruptPreferredNeverFallsBack(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("Darwin Application Support legacy config is unavailable on %s", runtime.GOOS)
	}
	fixture := useRealConfigRoot(t)
	corrupt := `{"provider":"preferred-broken"`
	writeRealConfigFixture(t, fixture.preferred, corrupt)
	writeRealConfigFixture(t, fixture.legacy, `{
  "contract": "config-file/v1",
  "provider": "legacy-provider-sentinel",
  "model": "legacy-model-sentinel",
  "delivery": "clipboard",
  "init_completed": true
}
`)
	got := captureSelectedProvider(t)

	c, out, errBuf := captureCLI()
	if code := c.run(nil); code != exitOK {
		t.Fatalf("run = %d, want %d; stderr: %s", code, exitOK, errBuf.String())
	}
	if _, ok := (*got).(rules.Provider); !ok {
		t.Fatalf("provider = %T, want built-in rules after corrupt preferred", *got)
	}
	if out.String() != "" {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
	if strings.Count(errBuf.String(), "clai: warning:") != 1 || !strings.Contains(errBuf.String(), "continuing with defaults") {
		t.Fatalf("stderr = %q, want one completed corrupt-config warning", errBuf.String())
	}
	for _, forbidden := range []string{"legacy-provider-sentinel", "legacy-model-sentinel"} {
		if strings.Contains(errBuf.String(), forbidden) {
			t.Fatalf("stderr disclosed legacy sentinel %q: %q", forbidden, errBuf.String())
		}
	}
	data, err := os.ReadFile(fixture.preferred)
	if err != nil {
		t.Fatalf("read corrupt preferred: %v", err)
	}
	if string(data) != corrupt {
		t.Fatalf("corrupt preferred bytes changed: got %q want %q", data, corrupt)
	}
	if _, err := os.Lstat(fixture.legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy config remains after preferred authority: %v", err)
	}
}

func useRealConfigRoot(t *testing.T) realConfigRootFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize real-config test root: %v", err)
	}
	home := filepath.Join(root, "home")
	xdg := filepath.Join(root, "xdg")
	for _, path := range []string{home, xdg} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create isolated config root %q: %v", path, err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	for _, name := range []string{"CLAI_PROVIDER", "CLAI_MODEL", "CLAI_DELIVERY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(name, "")
	}
	original := loadConfig
	loadConfig = config.Load
	t.Cleanup(func() { loadConfig = original })
	return realConfigRootFixture{
		home:      home,
		xdg:       xdg,
		preferred: filepath.Join(xdg, "clai", "config.json"),
		legacy:    filepath.Join(home, "Library", "Application Support", "clai", "config.json"),
	}
}

func writeRealConfigFixture(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create config fixture directory: %v", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("set config fixture directory mode: %v", err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("set config fixture mode: %v", err)
	}
}

func captureSelectedProviderWithOutcome(t *testing.T, outcome app.Outcome) *provider.Provider {
	t.Helper()
	var got provider.Provider
	original := executeTUI
	executeTUI = func(p provider.Provider, _ *capability.Cached, _, _ string) (app.Outcome, error) {
		got = p
		return outcome, nil
	}
	t.Cleanup(func() { executeTUI = original })
	return &got
}
