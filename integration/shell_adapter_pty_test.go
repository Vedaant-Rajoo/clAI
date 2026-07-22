package integration_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codeberg.org/newedia/clai/internal/shellinit"
	"github.com/creack/pty"
)

const ptyTimeout = 5 * time.Second

type shellCase struct {
	name string
	args []string
}

type outcome struct {
	name string
	mode string
}

func TestGeneratedShellAdaptersPTY(t *testing.T) {
	shells := []shellCase{
		{name: "fish", args: []string{"--no-config"}},
		{name: "bash", args: []string{"--noprofile", "--norc"}},
		{name: "zsh", args: []string{"-f"}},
	}
	outcomes := []outcome{
		{name: "accepted", mode: "accept"},
		{name: "cancelled", mode: "cancel"},
		{name: "error", mode: "error"},
	}

	for _, shell := range shells {
		shell := shell
		shellPath, err := exec.LookPath(shell.name)
		if err != nil {
			t.Run(shell.name, func(t *testing.T) {
				t.Skipf("%s is unavailable: %v", shell.name, err)
			})
			continue
		}

		for _, result := range outcomes {
			result := result
			t.Run(shell.name+"/"+result.name, func(t *testing.T) {
				testShellAdapter(t, shell, shellPath, result)
			})
		}
	}
}

func testShellAdapter(t *testing.T, shell shellCase, shellPath string, result outcome) {
	t.Helper()

	dir := t.TempDir()
	adapterPath := filepath.Join(dir, "clai."+shell.name)
	helperPath := filepath.Join(dir, "fake-clai")
	argsPath := filepath.Join(dir, "helper-args")
	readyPath := filepath.Join(dir, "ready")
	statePath := filepath.Join(dir, "state")
	cursorPath := filepath.Join(dir, "cursor")
	donePath := filepath.Join(dir, "done")
	executedPath := filepath.Join(dir, "executed marker")

	adapter, err := shellinit.Script(shell.name)
	if err != nil {
		t.Fatalf("generate %s adapter: %v", shell.name, err)
	}
	if err := os.WriteFile(adapterPath, []byte(adapter), 0o600); err != nil {
		t.Fatalf("write generated adapter: %v", err)
	}
	if err := os.WriteFile(helperPath, []byte(fakeCLAI), 0o700); err != nil {
		t.Fatalf("write fake clai helper: %v", err)
	}

	originalText := "original ? [x]"
	acceptedText := "accepted * $HOME"
	if shell.name != "bash" {
		originalText += " 日本語"
		acceptedText += " 日本語"
	}
	original := "printf '" + originalText + "' > " + shellQuote(executedPath)
	accepted := "printf '" + acceptedText + "' > " + shellQuote(executedPath)

	cmd := exec.Command(shellPath, shell.args...)
	cmd.Dir = dir
	cmd.Env = withEnv(os.Environ(), map[string]string{
		"CLAI_COMMAND":    helperPath,
		"CLAI_TEST_ARGS":  argsPath,
		"CLAI_TEST_MODE":  result.mode,
		"CLAI_TEST_VALUE": accepted,
		"TMPDIR":          dir,
		"TERM":            "dumb",
	})

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 4096})
	if err != nil {
		t.Fatalf("start clean interactive %s: %v", shell.name, err)
	}
	var output lockedBuffer
	drainDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&output, ptmx)
		close(drainDone)
	}()
	cleanup := func() {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		select {
		case <-drainDone:
		case <-time.After(time.Second):
		}
	}
	defer cleanup()

	setup := inspectorSetup(shell.name, adapterPath, statePath, cursorPath, donePath, readyPath)
	writePTY(t, ptmx, setup)
	waitForFile(t, readyPath, ptyTimeout, shell.name+" setup", &output)

	writePTY(t, ptmx, original)
	writePTY(t, ptmx, string([]byte{0x18, 0x01})) // Ctrl-X Ctrl-A: generated clai widget.
	waitForFile(t, argsPath, ptyTimeout, shell.name+" fake clai invocation", &output)
	resultPath := assertHelperInvocation(t, argsPath, shell.name)
	waitForMissingFile(t, resultPath, ptyTimeout, shell.name+" result-file cleanup", &output)

	wantBuffer := original
	if result.mode == "accept" {
		// The adapters insert the accepted command into the existing buffer
		// rather than replacing it, preserving any text the user typed before
		// invoking the widget.
		wantBuffer = original + accepted
	}

	if shell.name == "bash" {
		// Bash 3.2 does not populate READLINE_LINE/READLINE_POINT on entry to a
		// bind -x inspector. Append a unique byte after invoking the adapter;
		// Readline processes it only after the adapter macro finishes. Seeing
		// the exact expected buffer followed by that byte in a 4096-column dumb
		// terminal proves both whole-buffer contents and end-of-buffer cursor
		// placement without submitting the command.
		writePTY(t, ptmx, "Z")
		writePTY(t, ptmx, string([]byte{0x18, 0x02}))
		waitForFile(t, donePath, ptyTimeout, shell.name+" inspector", &output)
		waitForOutput(t, &output, wantBuffer+"Z", ptyTimeout, shell.name+" buffer probe")
	} else {
		writePTY(t, ptmx, string([]byte{0x18, 0x02})) // Ctrl-X Ctrl-B: deterministic inspector.
		waitForFile(t, donePath, ptyTimeout, shell.name+" inspector", &output)

		gotBuffer, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatalf("read inspected buffer: %v\nterminal output:\n%s", err, output.String())
		}
		gotCursor, err := os.ReadFile(cursorPath)
		if err != nil {
			t.Fatalf("read inspected cursor: %v\nterminal output:\n%s", err, output.String())
		}
		if string(gotBuffer) != wantBuffer {
			t.Fatalf("buffer mismatch\n got: %q\nwant: %q\nterminal output:\n%s", gotBuffer, wantBuffer, output.String())
		}
		cursorParts := strings.Split(string(gotCursor), ":")
		if len(cursorParts) != 2 || cursorParts[0] == "" || cursorParts[0] != cursorParts[1] {
			t.Fatalf("cursor is not at buffer end: got %q\nterminal output:\n%s", gotCursor, output.String())
		}
	}
	if _, err := os.Stat(executedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("adapter executed the edited buffer; marker stat error: %v", err)
	}
}

func inspectorSetup(shell, adapterPath, statePath, cursorPath, donePath, readyPath string) string {
	adapter := shellQuote(adapterPath)
	state := shellQuote(statePath)
	cursor := shellQuote(cursorPath)
	done := shellQuote(donePath)
	ready := shellQuote(readyPath)

	switch shell {
	case "fish":
		return fmt.Sprintf("source %s\nfunction __clai_inspect; printf '%%s' (commandline) > %s; printf '%%s:%%s' (commandline --cursor) (string length -- (commandline)) > %s; command touch %s; end\nbind \\cx\\cb __clai_inspect\ncommand touch %s\n", adapter, state, cursor, done, ready)
	case "bash":
		return fmt.Sprintf("source %s\n__clai_inspect() { printf '%%s' \"$READLINE_LINE\" > %s; printf '%%s' \"$READLINE_POINT\" > %s; command touch %s; }\nbind -x '\"\\C-x\\C-b\":__clai_inspect'\ncommand touch %s\n", adapter, state, cursor, done, ready)
	case "zsh":
		return fmt.Sprintf("source %s\nfunction __clai_inspect() { print -rn -- \"$BUFFER\" > %s; print -rn -- \"$CURSOR:${#BUFFER}\" > %s; command touch %s; }\nzle -N clai-inspect __clai_inspect\nbindkey '^X^B' clai-inspect\ncommand touch %s\n", adapter, state, cursor, done, ready)
	default:
		panic("unsupported shell " + shell)
	}
}

func assertHelperInvocation(t *testing.T, argsPath, shell string) string {
	t.Helper()
	contents, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("fake clai was not invoked through CLAI_COMMAND: %v", err)
	}
	args := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n")
	if len(args) != 5 {
		t.Fatalf("fake clai arguments: got %q, want five arguments", args)
	}
	if args[0] != "widget" || args[1] != "--shell" || args[2] != shell || args[3] != "--result-file" {
		t.Fatalf("fake clai arguments: got %q, want widget --shell %s --result-file <path>", args, shell)
	}
	if args[4] == "" || filepath.Dir(args[4]) != "/tmp" {
		t.Fatalf("result file path %q is not a temporary file under /tmp", args[4])
	}
	return args[4]
}

func writePTY(t *testing.T, ptmx *os.File, text string) {
	t.Helper()
	if _, err := io.WriteString(ptmx, text); err != nil {
		t.Fatalf("write PTY input: %v", err)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration, action string, output *lockedBuffer) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()

	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("wait for %s: %v", action, err)
		}

		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s\nterminal output:\n%s", action, output.String())
		}
	}
}

func waitForOutput(t *testing.T, output *lockedBuffer, text string, timeout time.Duration, action string) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()

	for {
		if strings.Contains(output.String(), text) {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s containing %q\nterminal output:\n%s", action, text, output.String())
		}
	}
}

func waitForMissingFile(t *testing.T, path string, timeout time.Duration, action string, output *lockedBuffer) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()

	for {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return
		} else if err != nil {
			t.Fatalf("wait for %s: %v", action, err)
		}

		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s\nterminal output:\n%s", action, output.String())
		}
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func withEnv(base []string, replacements map[string]string) []string {
	env := make([]string, 0, len(base)+len(replacements))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, replace := replacements[key]; !replace {
			env = append(env, entry)
		}
	}
	for key, value := range replacements {
		env = append(env, key+"="+value)
	}
	return env
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

const fakeCLAI = `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$CLAI_TEST_ARGS.tmp"
mv "$CLAI_TEST_ARGS.tmp" "$CLAI_TEST_ARGS"
if [ "$#" -ne 5 ] || [ "$1" != widget ] || [ "$2" != --shell ] || [ "$4" != --result-file ]; then
    exit 64
fi
case "$CLAI_TEST_MODE" in
    accept)
        printf '%s' "$CLAI_TEST_VALUE" > "$5"
        exit 0
        ;;
    cancel)
        exit 3
        ;;
    error)
        exit 7
        ;;
    *)
        exit 64
        ;;
esac
`
