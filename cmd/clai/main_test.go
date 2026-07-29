package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vedaant-Rajoo/clai/internal/app"
	"github.com/Vedaant-Rajoo/clai/internal/applicability"
	"github.com/Vedaant-Rajoo/clai/internal/capability"
	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
	"github.com/Vedaant-Rajoo/clai/internal/provider/anthropic"
	"github.com/Vedaant-Rajoo/clai/internal/provider/openrouter"
	"github.com/Vedaant-Rajoo/clai/internal/provider/rules"
	"github.com/Vedaant-Rajoo/clai/internal/safety"
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
		if !validShell(shell) {
			t.Errorf("validShell(%q) = false", shell)
		}
	}
	for _, shell := range []string{"", "sh", "/bin/fish", "FISH"} {
		if validShell(shell) {
			t.Errorf("validShell(%q) = true", shell)
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
