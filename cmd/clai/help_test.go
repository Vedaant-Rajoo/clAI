package main

import (
	"bytes"
	"strings"
	"testing"
)

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
		{"auth -h", []string{"auth", "-h"}, exitOK},
		{"auth --help", []string{"auth", "--help"}, exitOK},
		{"help init", []string{"help", "init"}, exitOK},
		{"init help", []string{"init", "help"}, exitOK},
		{"init -h", []string{"init", "-h"}, exitOK},
		{"version", []string{"version"}, exitOK},
		{"version help", []string{"version", "help"}, exitOK},
		{"help version", []string{"help", "version"}, exitOK},
		{"help widget", []string{"help", "widget"}, exitOK},
		{"widget help", []string{"widget", "help"}, exitOK},
		{"widget -h", []string{"widget", "-h"}, exitOK},
		{"help unknown", []string{"help", "bogus"}, exitUsage},
		{"unknown command", []string{"bogus"}, exitUsage},
		{"version extra arg", []string{"version", "bogus"}, exitUsage},
		{"auth no verb", []string{"auth"}, exitUsage},
		{"auth unknown verb", []string{"auth", "bogus"}, exitUsage},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(tc.args); got != tc.want {
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
		"--api-key", "--fallback-rules", "--version",
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

	for _, want := range []string{"login", "status", "logout", "--provider"} {
		if !strings.Contains(out, want) {
			t.Errorf("auth help missing %q", want)
		}
	}
}

func TestLookupCommand(t *testing.T) {
	for _, name := range []string{"auth", "init", "version", "widget"} {
		if _, ok := lookupCommand(name); !ok {
			t.Errorf("lookupCommand(%q) not found", name)
		}
	}
	if _, ok := lookupCommand("bogus"); ok {
		t.Error("lookupCommand(\"bogus\") unexpectedly found")
	}
}
