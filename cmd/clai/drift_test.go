package main

import (
	"bytes"
	"flag"
	"regexp"
	"testing"
)

// flagToken matches a long flag as written in help prose, e.g. "--print-command".
var flagToken = regexp.MustCompile(`--[a-zA-Z][a-zA-Z0-9-]*`)

// driftCase pairs a command's flag registration with the help text that is
// supposed to document those flags. allowInHelp lists flags that legitimately
// appear in the help but are not registered on the FlagSet (e.g. flags handled
// by manual argument scanning rather than the flag package).
type driftCase struct {
	name        string
	register    func(*flag.FlagSet)
	help        func() string
	allowInHelp map[string]bool
}

func driftCases() []driftCase {
	return []driftCase{
		{
			name:     "top-level",
			register: func(fs *flag.FlagSet) { registerInteractiveFlags(fs) },
			help: func() string {
				var buf bytes.Buffer
				printMainHelp(&buf)
				return buf.String()
			},
		},
		{
			name:     "widget",
			register: func(fs *flag.FlagSet) { registerWidgetFlags(fs) },
			help:     func() string { return commandLong("widget") },
		},
		{
			name:        "auth",
			register:    func(*flag.FlagSet) {},
			help:        func() string { return commandLong("auth") },
			allowInHelp: map[string]bool{"--provider": true, "--api-key": true},
		},
	}
}

// commandLong returns the full help page for a named command, or "" if the
// command is not in the help table (which surfaces as a drift failure because
// its registered flags will appear undocumented).
func commandLong(name string) string {
	c, ok := lookupCommand(name)
	if !ok {
		return ""
	}
	return c.long
}

// TestNoFlagHelpDrift fails when a registered flag is missing from its command's
// help, or when the help documents a "--flag" that the command does not
// register (and is not explicitly allow-listed).
func TestNoFlagHelpDrift(t *testing.T) {
	for _, tc := range driftCases() {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet(tc.name, flag.ContinueOnError)
			tc.register(fs)

			registered := map[string]bool{}
			fs.VisitAll(func(f *flag.Flag) { registered["--"+f.Name] = true })

			help := tc.help()
			documented := map[string]bool{}
			for _, tok := range flagToken.FindAllString(help, -1) {
				documented[tok] = true
			}

			for name := range registered {
				if !documented[name] {
					t.Errorf("flag %s is registered but not documented in %s help", name, tc.name)
				}
			}
			for tok := range documented {
				if !registered[tok] && !tc.allowInHelp[tok] {
					t.Errorf("%s help documents %s but no such flag is registered", tc.name, tok)
				}
			}
		})
	}
}
