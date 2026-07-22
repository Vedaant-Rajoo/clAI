package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"codeberg.org/newedia/clai/internal/provider"
	"codeberg.org/newedia/clai/internal/provider/openrouter"
	"codeberg.org/newedia/clai/internal/provider/rules"
)

func TestSelectProviderDefaultIsRules(t *testing.T) {
	p, err := selectProvider("rules", "", "", false)
	if err != nil {
		t.Fatalf("selectProvider: %v", err)
	}
	if _, ok := p.(rules.Provider); !ok {
		t.Errorf("got %T, want rules.Provider", p)
	}
}

func TestSelectProviderOpenRouterUsesEnvKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")
	p, err := selectProvider("openrouter", "some/model", "", false)
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
	p, err := selectProvider("openrouter", "", "flag-key", false)
	if err != nil {
		t.Fatalf("selectProvider: %v", err)
	}
	if p.(openrouter.Provider).APIKey != "flag-key" {
		t.Errorf("APIKey = %q, want flag-key", p.(openrouter.Provider).APIKey)
	}
}

func TestSelectProviderFallbackWraps(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")
	p, err := selectProvider("openrouter", "", "", true)
	if err != nil {
		t.Fatalf("selectProvider: %v", err)
	}
	if _, ok := p.(fallback); !ok {
		t.Errorf("got %T, want fallback wrapper", p)
	}
}

func TestSelectProviderUnknown(t *testing.T) {
	if _, err := selectProvider("bogus", "", "", false); err == nil {
		t.Error("want error for unknown provider")
	}
}

func TestSelectProviderUnimplemented(t *testing.T) {
	if _, err := selectProvider("anthropic", "", "", false); err == nil {
		t.Error("want error for unimplemented provider")
	}
}

type failingProvider struct{}

func (failingProvider) Compile(provider.Request) ([]provider.Candidate, error) {
	return nil, errors.New("boom")
}

func TestFallbackFallsBackToRules(t *testing.T) {
	p := fallback{primary: failingProvider{}}
	candidates, err := p.Compile(provider.Request{Intent: "run tests"})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(candidates) == 0 || candidates[0].Command != "go test ./..." {
		t.Errorf("candidates = %+v, want rules result", candidates)
	}
}

func TestFallbackPassesThroughSuccess(t *testing.T) {
	p := fallback{primary: rules.Provider{}}
	candidates, err := p.Compile(provider.Request{Intent: "run tests"})
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
	if code := run([]string{"widget"}); code != exitUsage {
		t.Errorf("run(widget) = %d, want %d", code, exitUsage)
	}
	if code := run([]string{"widget", "--shell", "sh", "--result-file", "/tmp/result"}); code != exitUsage {
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
	executeTUI = func(provider.Provider, string) (string, bool, error) {
		called = true
		return "pwd", true, nil
	}
	t.Cleanup(func() { executeTUI = original })

	code := run([]string{"widget", "--shell", "fish", "--result-file", path})
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
	executeTUI = func(provider.Provider, string) (string, bool, error) {
		return `printf '%s' "hello * $world 日本語"`, true, nil
	}
	t.Cleanup(func() { executeTUI = original })

	code := run([]string{"widget", "--shell", "fish", "--result-file", path})
	if code != exitOK {
		t.Fatalf("run(widget) = %d, want %d", code, exitOK)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `printf '%s' "hello * $world 日本語"`
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
	executeTUI = func(provider.Provider, string) (string, bool, error) {
		return "", false, nil
	}
	t.Cleanup(func() { executeTUI = original })

	code := run([]string{"widget", "--shell", "zsh", "--result-file", path})
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
	executeTUI = func(provider.Provider, string) (string, bool, error) {
		return "", false, errors.New("tui failed")
	}
	t.Cleanup(func() { executeTUI = original })

	code := run([]string{"widget", "--shell", "bash", "--result-file", path})
	if code != exitError {
		t.Fatalf("run(widget) = %d, want %d", code, exitError)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("result file was not removed: %v", err)
	}
}

func TestWriteWidgetResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	const command = `printf '%s' "hello * world"`
	if err := writeWidgetResult(path, command); err != nil {
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
		if err := writeWidgetResult("result", "pwd"); err == nil {
			t.Fatal("expected relative path error")
		}
	})

	t.Run("nonempty", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "result")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeWidgetResult(path, "pwd"); err == nil {
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
		if err := writeWidgetResult(path, "pwd"); err == nil {
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
		if err := writeWidgetResult(link, "pwd"); err == nil {
			t.Fatal("expected symlink error")
		}
	})
}
