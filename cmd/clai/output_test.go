package main

import (
	"strings"
	"testing"
)

// These tests lock the contract for which stream each command writes to:
// user-requested output (version, help, generated scripts) goes to stdout,
// while diagnostics (usage errors, failures) go to stderr. They guard against
// a future refactor silently swapping the streams.

func TestOutputStreamRouting(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string // substring that must appear on stdout ("" = stdout must be empty)
		wantStderr string // substring that must appear on stderr ("" = stderr must be empty)
	}{
		{
			name:       "version to stdout",
			args:       []string{"version"},
			wantCode:   exitOK,
			wantStdout: version,
			wantStderr: "",
		},
		{
			name:       "--version flag to stdout",
			args:       []string{"--version"},
			wantCode:   exitOK,
			wantStdout: version,
			wantStderr: "",
		},
		{
			name:       "top-level help to stdout",
			args:       []string{"help"},
			wantCode:   exitOK,
			wantStdout: "Usage:",
			wantStderr: "",
		},
		{
			name:       "command help to stdout",
			args:       []string{"help", "auth"},
			wantCode:   exitOK,
			wantStdout: "clai auth",
			wantStderr: "",
		},
		{
			name:       "init script to stdout",
			args:       []string{"init", "fish"},
			wantCode:   exitOK,
			wantStdout: "clai",
			wantStderr: "",
		},
		{
			name:       "unknown command to stderr",
			args:       []string{"bogus"},
			wantCode:   exitUsage,
			wantStdout: "",
			wantStderr: "unknown command",
		},
		{
			name:       "unknown flag to stderr",
			args:       []string{"--nope"},
			wantCode:   exitUsage,
			wantStdout: "",
			wantStderr: "clai:",
		},
		{
			name:       "auth without verb to stderr",
			args:       []string{"auth"},
			wantCode:   exitUsage,
			wantStdout: "",
			wantStderr: "usage: clai auth",
		},
		{
			name:       "unknown init shell to stderr",
			args:       []string{"init", "tcsh"},
			wantCode:   exitUsage,
			wantStdout: "",
			wantStderr: "clai init:",
		},
		{
			name:       "help for unknown command to stderr",
			args:       []string{"help", "bogus"},
			wantCode:   exitUsage,
			wantStdout: "",
			wantStderr: "unknown command",
		},
		{
			name:       "version with extra arg to stderr",
			args:       []string{"version", "bogus"},
			wantCode:   exitUsage,
			wantStdout: "",
			wantStderr: "unknown argument",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, out, errBuf := captureCLI()
			code := c.run(tc.args)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d", code, tc.wantCode)
			}
			assertStream(t, "stdout", out.String(), tc.wantStdout)
			assertStream(t, "stderr", errBuf.String(), tc.wantStderr)
		})
	}
}

// assertStream checks that got contains want, or is empty when want is "".
func assertStream(t *testing.T, name, got, want string) {
	t.Helper()
	if want == "" {
		if got != "" {
			t.Errorf("%s = %q, want empty", name, got)
		}
		return
	}
	if !strings.Contains(got, want) {
		t.Errorf("%s = %q, want to contain %q", name, got, want)
	}
}
