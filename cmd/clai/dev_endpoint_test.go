package main

import (
	"flag"
	"strings"
	"testing"

	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
	"github.com/Vedaant-Rajoo/clai/internal/provider/anthropic"
	"github.com/Vedaant-Rajoo/clai/internal/provider/openrouter"
)

// parseDevEndpoint runs the real flag registration and validation path so the
// tests exercise exactly what the CLI does (REQ-DEVENDPOINT-002/003).
func parseDevEndpoint(t *testing.T, args ...string) (string, error) {
	t.Helper()
	fs := flag.NewFlagSet("clai", flag.ContinueOnError)
	fs.SetOutput(nopWriter{})
	f := registerProviderFlags(fs)
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	return f.devEndpointOption(resolvedProviderForTest(f))
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestDevEndpointAcceptsOnlyLoopback(t *testing.T) {
	accepted := []string{
		"http://127.0.0.1:8747",
		"http://localhost:8747",
		"http://api.localhost:8747",
		"http://[::1]:8747",
		"https://127.0.0.1:8747",
		"http://127.9.9.9:8747",
	}
	for _, endpoint := range accepted {
		t.Run("accept "+endpoint, func(t *testing.T) {
			got, err := parseDevEndpoint(t, "--provider", "anthropic", "--dev-endpoint", endpoint)
			if err != nil || got != endpoint {
				t.Fatalf("devEndpointOption(%q) = %q, %v; want the endpoint and no error", endpoint, got, err)
			}
		})
	}

	rejected := []struct {
		endpoint string
		want     string
	}{
		{"https://api.anthropic.com", "loopback"},
		{"https://openrouter.ai/api/v1/chat/completions", "loopback"},
		{"http://evil.example.com", "loopback"},
		// Lexical classification: a hostname that resolves to loopback is still remote.
		{"http://localhost.evil.com", "loopback"},
		{"http://192.168.1.10:8747", "loopback"},
		{"not-a-url", "invalid"},
		{"", ""},
		{"ftp://127.0.0.1", "http or https"},
		// No host at all: rejected as an invalid endpoint before the scheme check.
		{"file:///etc/passwd", "invalid"},
		{"http://user:password@127.0.0.1:8747", "userinfo"},
	}
	for _, tt := range rejected {
		if tt.endpoint == "" {
			continue
		}
		t.Run("reject "+tt.endpoint, func(t *testing.T) {
			got, err := parseDevEndpoint(t, "--provider", "anthropic", "--dev-endpoint", tt.endpoint)
			if err == nil {
				t.Fatalf("devEndpointOption(%q) = %q, want a usage error", tt.endpoint, got)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestDevEndpointRejectedForRulesProvider(t *testing.T) {
	for _, name := range []string{"rules", ""} {
		args := []string{"--dev-endpoint", "http://127.0.0.1:8747"}
		if name != "" {
			args = append([]string{"--provider", name}, args...)
		}
		_, err := parseDevEndpoint(t, args...)
		if err == nil || !strings.Contains(err.Error(), "not valid for provider") {
			t.Fatalf("provider %q: err = %v, want rejection for the local provider", name, err)
		}
	}
}

func TestDevEndpointEmptyByDefault(t *testing.T) {
	got, err := parseDevEndpoint(t, "--provider", "anthropic")
	if err != nil || got != "" {
		t.Fatalf("devEndpointOption() = %q, %v; want empty and no error for a normal session", got, err)
	}
}

// TestDevEndpointDefaultsToLocalOnly is the regression for P0-DEVENDPOINT-001.
// A loopback development endpoint is a loopback provider endpoint, so with no
// explicit --context-policy the policy must default to local-only and the
// request must carry no context at all (REQ-DEVENDPOINT-004, REQ-CONTEXT-003).
// Defaulting on the provider name instead leaked a full context capsule to the
// development endpoint.
func TestDevEndpointDefaultsToLocalOnly(t *testing.T) {
	for _, name := range []string{"anthropic", "openrouter"} {
		t.Run(name, func(t *testing.T) {
			fs := flag.NewFlagSet("clai", flag.ContinueOnError)
			fs.SetOutput(nopWriter{})
			f := registerProviderFlags(fs)
			if err := fs.Parse([]string{"--provider", name, "--dev-endpoint", "http://127.0.0.1:8747"}); err != nil {
				t.Fatal(err)
			}
			devEndpoint, err := f.devEndpointOption(resolvedProviderForTest(f))
			if err != nil {
				t.Fatal(err)
			}
			policy, shared, err := f.contextOptions(devEndpoint, resolvedProviderForTest(f))
			if err != nil {
				t.Fatal(err)
			}
			if policy != machinecontext.PolicyLocalOnly || len(shared) != 0 {
				t.Fatalf("policy = %q shared = %v, want local-only with no shared fields for a loopback endpoint", policy, shared)
			}
		})
	}

	// Without the override the same provider still defaults to remote-minimal,
	// because its pinned production endpoint is remote.
	fs := flag.NewFlagSet("clai", flag.ContinueOnError)
	fs.SetOutput(nopWriter{})
	f := registerProviderFlags(fs)
	if err := fs.Parse([]string{"--provider", "anthropic"}); err != nil {
		t.Fatal(err)
	}
	policy, _, err := f.contextOptions("", resolvedProviderForTest(f))
	if err != nil {
		t.Fatal(err)
	}
	if policy != machinecontext.PolicyRemoteMinimal {
		t.Fatalf("policy = %q, want remote-minimal for the pinned production endpoint", policy)
	}

	// An explicit policy still wins over the loopback default so capsule and
	// receipt behavior remain exercisable against the stub.
	fs = flag.NewFlagSet("clai", flag.ContinueOnError)
	fs.SetOutput(nopWriter{})
	f = registerProviderFlags(fs)
	if err := fs.Parse([]string{"--provider", "anthropic", "--dev-endpoint", "http://127.0.0.1:8747", "--context-policy", "remote-minimal"}); err != nil {
		t.Fatal(err)
	}
	policy, _, err = f.contextOptions("http://127.0.0.1:8747", resolvedProviderForTest(f))
	if err != nil {
		t.Fatal(err)
	}
	if policy != machinecontext.PolicyRemoteMinimal {
		t.Fatalf("policy = %q, want the explicit remote-minimal to win", policy)
	}
}

func TestDevEndpointForwardedToRemoteProviders(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")
	const endpoint = "http://127.0.0.1:8747"

	p, err := selectProvider("anthropic", "", "", false, "", nil, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(anthropic.Provider).DevEndpoint; got != endpoint {
		t.Fatalf("anthropic DevEndpoint = %q, want %q", got, endpoint)
	}

	p, err = selectProvider("openrouter", "", "", false, "", nil, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(openrouter.Provider).DevEndpoint; got != endpoint {
		t.Fatalf("openrouter DevEndpoint = %q, want %q", got, endpoint)
	}

	// A normal session leaves it empty so the pinned production endpoint applies.
	p, err = selectProvider("anthropic", "", "", false, "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(anthropic.Provider).DevEndpoint; got != "" {
		t.Fatalf("anthropic DevEndpoint = %q, want empty for a production session", got)
	}
}
