package main

import (
	"errors"
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
