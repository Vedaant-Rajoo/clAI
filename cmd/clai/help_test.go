package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureCLI returns a cli whose stdout/stderr are captured in the returned
// buffers, for asserting output and stream routing in tests.
func captureCLI() (cli, *bytes.Buffer, *bytes.Buffer) {
	var out, errBuf bytes.Buffer
	return cli{stdout: &out, stderr: &errBuf}, &out, &errBuf
}

func TestHelpRouting(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"top help", []string{"help"}, exitOK},
		{"top -h", []string{"-h"}, exitOK},
		{"top --help", []string{"--help"}, exitOK},
		{"help auth", []string{"help", "auth"}, exitOK},
		{"auth help", []string{"auth", "help"}, exitOK},
		{"auth -h before verb", []string{"auth", "-h"}, exitOK},
		{"auth --help before verb", []string{"auth", "--help"}, exitOK},
		{"auth login -h", []string{"auth", "login", "-h"}, exitOK},
		{"auth login --help", []string{"auth", "login", "--help"}, exitOK},
		{"auth flag then --help", []string{"auth", "status", "--provider", "anthropic", "--help"}, exitOK},
		{"auth trailing help", []string{"auth", "status", "--provider", "anthropic", "help"}, exitOK},
		{"help init", []string{"help", "init"}, exitOK},
		{"init help", []string{"init", "help"}, exitOK},
		{"init -h", []string{"init", "-h"}, exitOK},
		{"version", []string{"version"}, exitOK},
		{"version help", []string{"version", "help"}, exitOK},
		{"help version", []string{"help", "version"}, exitOK},
		{"help widget", []string{"help", "widget"}, exitOK},
		{"help storage", []string{"help", "storage"}, exitOK},
		{"widget help", []string{"widget", "help"}, exitOK},
		{"widget -h", []string{"widget", "-h"}, exitOK},
		{"help unknown", []string{"help", "bogus"}, exitUsage},
		{"unknown command", []string{"bogus"}, exitUsage},
		{"version extra arg", []string{"version", "bogus"}, exitUsage},
		{"auth no verb", []string{"auth"}, exitUsage},
		{"auth unknown verb", []string{"auth", "bogus"}, exitUsage},
		// Go's flag package stops at the first non-flag argument, so a trailing
		// positional after interactive flags used to be silently ignored and
		// start the TUI. Help is honored; anything else is a usage error.
		{"flags then help", []string{"--provider", "rules", "help"}, exitOK},
		{"flags then -h", []string{"--provider", "rules", "-h"}, exitOK},
		{"flags then --help", []string{"--provider", "rules", "--help"}, exitOK},
		{"flags then stray positional", []string{"--provider", "rules", "bogus"}, exitUsage},
		{"flags then extra positionals", []string{"--provider", "rules", "help", "extra"}, exitUsage},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := captureCLI()
			if got := c.run(tc.args); got != tc.want {
				t.Fatalf("run(%v) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}

func TestMainHelpContent(t *testing.T) {
	var buf bytes.Buffer
	printMainHelp(&buf)
	out := buf.String()

	for _, want := range []string{
		"auth", "init", "version",
		"--copy", "--print-command", "--provider", "--model",
		"--api-key", "--fallback-rules", "--context-policy", "--share-context", "--version",
		"rules", "openrouter", "anthropic",
		"clai help storage",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("main help missing %q", want)
		}
	}
	if strings.Contains(out, "widget") {
		t.Error("main help should not list the internal widget command")
	}
}

func TestAuthHelpContent(t *testing.T) {
	c, ok := lookupCommand("auth")
	if !ok {
		t.Fatal("auth command missing from help table")
	}
	var buf bytes.Buffer
	printCommandHelp(&buf, c)
	out := buf.String()

	for _, want := range []string{
		"clai auth login [--provider <name>]",
		"clai auth status [--provider <name>] [--api-key <value>]",
		"clai auth logout [--provider <name>]",
		"space-separated",
		"--help",
		"clai help storage",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("auth help missing %q", want)
		}
	}
}

func TestLookupCommand(t *testing.T) {
	for _, name := range []string{"auth", "init", "version", "storage", "widget"} {
		if _, ok := lookupCommand(name); !ok {
			t.Errorf("lookupCommand(%q) not found", name)
		}
	}
	if _, ok := lookupCommand("bogus"); ok {
		t.Error("lookupCommand(\"bogus\") unexpectedly found")
	}
}

func TestStorageHelpConfigRoot(t *testing.T) {
	var buf bytes.Buffer
	printMainHelp(&buf)
	mainHelp := buf.String()
	if !strings.Contains(mainHelp, "clai help storage") {
		t.Fatal("main help does not route detailed storage guidance")
	}
	if strings.Contains(mainHelp, "$XDG_CONFIG_HOME/clai/config.json") {
		t.Fatal("main help still front-loads detailed storage paths")
	}

	help := commandLong("storage")

	for _, want := range []string{
		"$XDG_CONFIG_HOME/clai/config.json",
		"$XDG_CONFIG_HOME/clai/credentials.json",
		"$HOME/.config/clai/config.json",
		"$HOME/.config/clai/credentials.json",
		"XDG_CONFIG_HOME must be absolute",
		"A relative XDG_CONFIG_HOME is invalid",
		"Windows keeps its platform user configuration directory",
		"old-only macOS Application Support state migrates one way",
		"Mixed-version downgrade",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("main help missing config-root contract %q", want)
		}
	}
	if strings.Contains(help, "<UserConfigDir>") {
		t.Fatal("main help retained the generic UserConfigDir active path")
	}
}

func TestAuthHelpRoutesStorageDetails(t *testing.T) {
	authHelp := commandLong("auth")
	if !strings.Contains(authHelp, "clai help storage") {
		t.Fatal("auth help does not route detailed storage guidance")
	}
	if strings.Contains(authHelp, "$XDG_CONFIG_HOME/clai/credentials.json") {
		t.Fatal("auth help still front-loads detailed storage paths")
	}

	docsPath := filepath.Join("..", "..", "docs", "providers.md")
	data, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("read provider documentation: %v", err)
	}
	docs := string(data)
	for _, want := range []string{
		"$XDG_CONFIG_HOME/clai/config.json",
		"$HOME/.config/clai/config.json",
		"$XDG_CONFIG_HOME/clai/credentials.json",
		"$HOME/.config/clai/credentials.json",
		"Relative XDG_CONFIG_HOME values are invalid",
		"Windows keeps its existing platform user configuration directory",
		"OS keychain service and user identities do not move",
		"preferred file is authoritative",
		"corrupt preferred config.json",
		"Store, Delete, and successful keyring cleanup reconcile both file locations",
		"`0600`",
		"`0700`",
		"selected base root itself may be a symlink",
		"descendants are opened without following symlinks",
		"Mixed-version downgrade",
		"unsupported",
	} {
		if !strings.Contains(docs, want) {
			t.Errorf("provider documentation missing storage contract %q", want)
		}
	}

	const legacyPath = "$HOME/Library/Application Support/clai"
	if count := strings.Count(docs, legacyPath); count != 1 {
		t.Fatalf("legacy macOS path occurs %d times, want exactly once inside migration guidance", count)
	}
	migration := markdownSection(docs, "## One-way macOS migration")
	if migration == "" || !strings.Contains(migration, legacyPath) {
		t.Fatal("legacy macOS path is not confined to the one-way migration section")
	}
	if strings.Contains(strings.Replace(docs, migration, "", 1), legacyPath) {
		t.Fatal("legacy macOS path appears outside the one-way migration section")
	}
}

func markdownSection(markdown, heading string) string {
	start := strings.Index(markdown, heading)
	if start < 0 {
		return ""
	}
	rest := markdown[start+len(heading):]
	if next := strings.Index(rest, "\n## "); next >= 0 {
		return markdown[start : start+len(heading)+next]
	}
	return markdown[start:]
}
