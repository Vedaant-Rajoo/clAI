package rules

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Vedaant-Rajoo/clai/internal/capability"
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

func TestControlledCapabilityFixtureSearchTODO(t *testing.T) {
	tests := []struct {
		name  string
		tools []string
		want  provider.Candidate
	}{
		{
			name:  "rg present",
			tools: []string{"rg", "grep"},
			want: provider.Candidate{
				Command:      "rg TODO",
				Explanation:  "Selected rg because it is installed.",
				Requirements: []capability.Requirement{{Kind: capability.RequirementTool, Name: "rg"}},
			},
		},
		{
			name:  "rg absent and grep present",
			tools: []string{"grep"},
			want: provider.Candidate{
				Command:      "grep -r TODO .",
				Explanation:  "Selected grep fallback because rg is absent.",
				Requirements: []capability.Requirement{{Kind: capability.RequirementTool, Name: "grep"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inventory := controlledInventory(t, tt.tools)
			candidates, err := (Provider{}).Compile(t.Context(), provider.Request{
				Intent:       "search for TODO",
				Capabilities: inventory,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(candidates) != 1 || !reflect.DeepEqual(candidates[0], tt.want) {
				t.Fatalf("candidates = %#v, want %#v", candidates, tt.want)
			}
		})
	}
}

func controlledInventory(t *testing.T, tools []string) capability.Inventory {
	t.Helper()
	directory := t.TempDir()
	for _, name := range tools {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", directory)
	t.Setenv("SHELL", "/bin/bash")
	return capability.Collect(t.Context(), "")
}
