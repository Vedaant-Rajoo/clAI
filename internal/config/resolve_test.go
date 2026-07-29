package config

import "testing"

// TestResolvePrecedence covers the pairwise precedence cases for CONF-03,
// explicitly including config-beats-builtin — the exact shadowing bug that a
// value-comparison precedence scheme would ship.
func TestResolvePrecedence(t *testing.T) {
	cases := []struct {
		name    string
		flag    string
		env     string
		config  string
		builtin string
		want    string
	}{
		{"config beats builtin", "", "", "openrouter", "rules", "openrouter"},
		{"env beats config", "", "rules", "openrouter", "rules", "rules"},
		{"flag beats env", "anthropic", "rules", "openrouter", "rules", "anthropic"},
		{"builtin when all layers empty", "", "", "", "rules", "rules"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.flag, tc.env, tc.config, tc.builtin)
			if got != tc.want {
				t.Fatalf("Resolve(%q, %q, %q, %q) = %q, want %q",
					tc.flag, tc.env, tc.config, tc.builtin, got, tc.want)
			}
		})
	}
}
