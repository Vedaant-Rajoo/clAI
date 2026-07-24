package rules

import (
	"context"
	"errors"
	"testing"

	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
)

func TestProviderCompile(t *testing.T) {
	var rulesProvider provider.Provider = Provider{}

	candidates, err := rulesProvider.Compile(context.Background(), provider.Request{
		Intent:  "show status",
		Context: machinecontext.Context{GitRepository: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(candidates))
	}

	if candidates[0].Command != "git status" {
		t.Fatalf("Command = %q, want git status", candidates[0].Command)
	}

	if candidates[0].Explanation == "" {
		t.Fatal("Explanation is empty")
	}
}

func TestProviderCompileNoSuggestion(t *testing.T) {
	candidates, err := (Provider{}).Compile(context.Background(), provider.Request{Intent: "make me coffee"})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %+v, want no suggestion", candidates)
	}
}

func TestProviderGitRulesOutsideRepositoryReturnNoSuggestion(t *testing.T) {
	intents := []string{"show diff", "cached diff", "commit history", "branch name", "remote url", "git tags"}
	for _, intent := range intents {
		t.Run(intent, func(t *testing.T) {
			candidates, err := (Provider{}).Compile(context.Background(), provider.Request{
				Intent:  intent,
				Context: machinecontext.Context{GitRepository: false},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(candidates) != 0 {
				t.Fatalf("candidates = %+v, want no suggestion", candidates)
			}
		})
	}
}

func TestProviderCompileCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	candidates, err := (Provider{}).Compile(ctx, provider.Request{Intent: "run tests"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %+v, want none", candidates)
	}
}
