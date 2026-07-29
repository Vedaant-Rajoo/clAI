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

// TestResolveDelivery covers the boolean delivery precedence matrix for
// CONF-03, including the adjacency edge sentinels cannot express: an explicit
// --copy=false (value equal to the flag's default) must beat a config
// "clipboard" because explicit-set state decides, never value comparison
// (RESEARCH Pitfall 2).
func TestResolveDelivery(t *testing.T) {
	cases := []struct {
		name          string
		copyFlag      bool
		printFlag     bool
		copyExplicit  bool
		printExplicit bool
		env           string
		config        string
		wantCopy      bool
		wantPrint     bool
	}{
		{name: "config clipboard maps to copy", config: "clipboard", wantCopy: true},
		{name: "config stdout maps to print", config: "stdout", wantPrint: true},
		{name: "explicit copy=false beats config clipboard", copyExplicit: true, config: "clipboard"},
		{name: "explicit print flag makes flag layer win entirely", printFlag: true, printExplicit: true, config: "clipboard", wantPrint: true},
		{name: "env stdout beats config clipboard", env: "stdout", config: "clipboard", wantPrint: true},
		{name: "config insert is inert in interactive mode", config: "insert"},
		{name: "unknown config value is inert", config: "bogus"},
		{name: "nothing set anywhere preserves current behavior"},
		{name: "both flags explicitly true apply both behaviors", copyFlag: true, printFlag: true, copyExplicit: true, printExplicit: true, wantCopy: true, wantPrint: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotCopy, gotPrint := ResolveDelivery(tc.copyFlag, tc.printFlag, tc.copyExplicit, tc.printExplicit, tc.env, tc.config)
			if gotCopy != tc.wantCopy || gotPrint != tc.wantPrint {
				t.Fatalf("ResolveDelivery(copy=%v, print=%v, copyExplicit=%v, printExplicit=%v, env=%q, config=%q) = (%v, %v), want (%v, %v)",
					tc.copyFlag, tc.printFlag, tc.copyExplicit, tc.printExplicit, tc.env, tc.config,
					gotCopy, gotPrint, tc.wantCopy, tc.wantPrint)
			}
		})
	}
}
