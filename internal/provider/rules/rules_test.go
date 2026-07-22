package rules

import (
	"testing"

	machinecontext "codeberg.org/newedia/clai/internal/context"
	"codeberg.org/newedia/clai/internal/provider"
)

func TestProviderCompile(t *testing.T) {
	var rulesProvider provider.Provider = Provider{}

	candidates, err := rulesProvider.Compile(provider.Request{
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
