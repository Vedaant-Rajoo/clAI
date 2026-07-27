package main

import (
	"flag"
	"io"
	"reflect"
	"testing"

	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
)

func parseProviderFlagsForTest(t *testing.T, widget bool, args ...string) (providerFlags, error) {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if widget {
		flags := registerWidgetFlags(fs)
		err := fs.Parse(args)
		return flags.providerFlags, err
	}
	flags := registerInteractiveFlags(fs)
	err := fs.Parse(args)
	return flags.providerFlags, err
}

func TestContextPolicyDefaults(t *testing.T) {
	for _, tt := range []struct {
		name     string
		provider string
		want     machinecontext.Policy
	}{
		{"rules", "rules", machinecontext.PolicyLocalOnly},
		{"openrouter", "openrouter", machinecontext.PolicyRemoteMinimal},
		{"anthropic", "anthropic", machinecontext.PolicyRemoteMinimal},
	} {
		t.Run(tt.name, func(t *testing.T) {
			flags, err := parseProviderFlagsForTest(t, false, "--provider", tt.provider)
			if err != nil {
				t.Fatal(err)
			}
			got, shared, err := flags.contextOptions()
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want || len(shared) != 0 {
				t.Fatalf("options = %q, %v; want %q, none", got, shared, tt.want)
			}
		})
	}
}

func TestRemoteExplicitGrammarAndDeterministicSharing(t *testing.T) {
	for _, widget := range []bool{false, true} {
		flags, err := parseProviderFlagsForTest(t, widget,
			"--provider", "openrouter",
			"--context-policy", "remote-explicit",
			"--share-context", "git_branch",
			"--share-context", "working_directory",
			"--share-context", "git_branch",
		)
		if err != nil {
			t.Fatal(err)
		}
		policy, shared, err := flags.contextOptions()
		if err != nil {
			t.Fatal(err)
		}
		if policy != machinecontext.PolicyRemoteExplicit {
			t.Fatalf("policy = %q", policy)
		}
		want := []string{machinecontext.FieldWorkingDirectory, machinecontext.FieldGitBranch}
		if !reflect.DeepEqual(shared, want) {
			t.Fatalf("shared = %v, want %v", shared, want)
		}
	}
}

func TestContextFlagConflicts(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"explicit without share", []string{"--context-policy", "remote-explicit"}},
		{"share without explicit policy", []string{"--share-context", "git_branch"}},
		{"share with minimal", []string{"--context-policy", "remote-minimal", "--share-context", "git_branch"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags, parseErr := parseProviderFlagsForTest(t, false, tt.args...)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if _, _, err := flags.contextOptions(); err == nil {
				t.Fatal("conflicting flags accepted")
			}
		})
	}
}

func TestContextFlagInvalidValues(t *testing.T) {
	for _, args := range [][]string{
		{"--context-policy", "everything"},
		{"--share-context", "hostname"},
		{"--context-policy", "remote-minimal", "--context-policy", "local-only"},
	} {
		if _, err := parseProviderFlagsForTest(t, false, args...); err == nil {
			t.Fatalf("args %v accepted", args)
		}
	}
}

func TestInteractiveRejectsContextConflictBeforeVersionShortcut(t *testing.T) {
	c, _, _ := captureCLI()
	if code := c.run([]string{"--version", "--context-policy", "remote-explicit"}); code != exitUsage {
		t.Fatalf("exit = %d, want usage error", code)
	}
}

func TestRemoteExplicitApprovalIsInvocationScoped(t *testing.T) {
	first, err := parseProviderFlagsForTest(t, false, "--context-policy", "remote-explicit", "--share-context", "git_branch")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := first.contextOptions(); err != nil {
		t.Fatal(err)
	}

	second, err := parseProviderFlagsForTest(t, false, "--context-policy", "remote-explicit")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := second.contextOptions(); err == nil {
		t.Fatal("later invocation reused earlier sharing approval")
	}
}
