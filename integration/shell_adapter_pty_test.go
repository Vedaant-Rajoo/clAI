package integration_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// TestGeneratedShellAdaptersMidCursorPTY proves REQ-WIDGET-002 (text on BOTH
// sides of the cursor is preserved) and the concrete REQ-EDGE-WIDGET-001
// example. TestGeneratedShellAdaptersPTY only ever inserts at end-of-buffer, so
// a regression that appended accepted text (for example zsh BUFFER+= or bash
// READLINE_LINE+=) instead of splicing at the cursor would still pass it. This
// test seeds a cursor in the MIDDLE of existing text ("git sta| --short"),
// accepts "tus", and requires the result to be "git status| --short" with the
// cursor at column 10 (before the space) rather than at end-of-buffer.
func TestGeneratedShellAdaptersMidCursorPTY(t *testing.T) {
	shells := []shellCase{
		{name: "fish", args: []string{"--no-config"}},
		{name: "bash", args: []string{"--noprofile", "--norc"}},
		{name: "zsh", args: []string{"-f"}},
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
		t.Run(shell.name, func(t *testing.T) {
			testShellAdapterMidCursor(t, shell, shellPath)
		})
	}
}

func testShellAdapterMidCursor(t *testing.T, shell shellCase, shellPath string) {
	t.Helper()

	if shell.name == "bash" {
		out, err := exec.Command(shellPath, "-c", "printf %s \"${BASH_VERSINFO[0]}\"").Output()
		if err != nil {
			t.Fatalf("determine bash major version: %v", err)
		}
		major, convErr := strconv.Atoi(strings.TrimSpace(string(out)))
		if convErr != nil {
			t.Fatalf("parse bash major version %q: %v", out, convErr)
		}
		if major < 4 {
			// Bash 3.x drives the widget through a Readline kill-ring macro (the
			// same compatibility technique fzf uses) because READLINE_POINT is
			// not writable; that macro yanks accepted text at end-of-line, so
			// middle-of-buffer cursor insertion is a documented limitation and is
			// not exercised on this version.
			t.Skipf("bash %s uses the Readline kill-ring macro; mid-buffer cursor insertion is a documented limitation", strings.TrimSpace(string(out)))
		}
	}

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

	// The exact REQ-EDGE-WIDGET-001 example: "git sta| --short" + accept "tus".
	const seedBuffer = "git sta --short"
	const seedCursor = 7 // len("git sta"): cursor sits just before the space.
	const accepted = "tus"
	const wantBuffer = "git status --short"
	const wantCursor = 10 // len("git status").
	bufferLen := len([]rune(seedBuffer)) + len([]rune(accepted))

	cmd := exec.Command(shellPath, shell.args...)
	cmd.Dir = dir
	cmd.Env = withEnv(os.Environ(), map[string]string{
		"CLAI_COMMAND":    helperPath,
		"CLAI_TEST_ARGS":  argsPath,
		"CLAI_TEST_MODE":  "accept",
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

	setup := midCursorSetup(shell.name, adapterPath, statePath, cursorPath, donePath, readyPath, seedBuffer, seedCursor)
	writePTY(t, ptmx, setup)
	waitForFile(t, readyPath, ptyTimeout, shell.name+" setup", &output)

	writePTY(t, ptmx, string([]byte{0x18, 0x0e})) // Ctrl-X Ctrl-N: seed a mid-line cursor.
	writePTY(t, ptmx, string([]byte{0x18, 0x01})) // Ctrl-X Ctrl-A: generated clai widget.
	waitForFile(t, argsPath, ptyTimeout, shell.name+" fake clai invocation", &output)
	resultPath := assertHelperInvocation(t, argsPath, shell.name)
	waitForMissingFile(t, resultPath, ptyTimeout, shell.name+" result-file cleanup", &output)

	writePTY(t, ptmx, string([]byte{0x18, 0x02})) // Ctrl-X Ctrl-B: deterministic inspector.
	waitForFile(t, donePath, ptyTimeout, shell.name+" inspector", &output)

	gotBuffer, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read inspected buffer: %v\nterminal output:\n%s", err, output.String())
	}
	if string(gotBuffer) != wantBuffer {
		t.Fatalf("mid-cursor buffer mismatch\n got: %q\nwant: %q\nterminal output:\n%s", gotBuffer, wantBuffer, output.String())
	}
	gotCursor, err := os.ReadFile(cursorPath)
	if err != nil {
		t.Fatalf("read inspected cursor: %v\nterminal output:\n%s", err, output.String())
	}
	cursorParts := strings.Split(string(gotCursor), ":")
	if len(cursorParts) != 2 {
		t.Fatalf("cursor record is malformed: got %q\nterminal output:\n%s", gotCursor, output.String())
	}
	if cursorParts[1] != strconv.Itoa(bufferLen) {
		t.Fatalf("buffer length mismatch: cursor %q, want length %d\nterminal output:\n%s", gotCursor, bufferLen, output.String())
	}
	if cursorParts[0] != strconv.Itoa(wantCursor) {
		t.Fatalf("accepted text was not spliced at the cursor: cursor %q, want position %d\nterminal output:\n%s", gotCursor, wantCursor, output.String())
	}
	if cursorParts[0] == cursorParts[1] {
		t.Fatalf("cursor is at end-of-buffer, so text after the cursor was not preserved: got %q\nterminal output:\n%s", gotCursor, output.String())
	}
	if _, err := os.Stat(executedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("adapter executed the edited buffer; marker stat error: %v", err)
	}
}

// midCursorSetup sources the adapter, binds a deterministic inspector to
// Ctrl-X Ctrl-B, and binds a seed function to Ctrl-X Ctrl-N that installs a
// buffer with the cursor positioned mid-string. The inspector records
// "<cursor>:<buffer length>" so callers can prove both the spliced contents and
// that the cursor landed at the splice point rather than end-of-buffer.
func midCursorSetup(shell, adapterPath, statePath, cursorPath, donePath, readyPath, seedBuffer string, seedCursor int) string {
	adapter := shellQuote(adapterPath)
	state := shellQuote(statePath)
	cursor := shellQuote(cursorPath)
	done := shellQuote(donePath)
	ready := shellQuote(readyPath)
	seed := shellQuote(seedBuffer)

	switch shell {
	case "fish":
		return fmt.Sprintf("source %s\nfunction __clai_inspect; printf '%%s' (commandline) > %s; printf '%%s:%%s' (commandline --cursor) (string length -- (commandline)) > %s; command touch %s; end\nfunction __clai_seed; commandline -r -- %s; commandline -C %d; end\nbind \\cx\\cb __clai_inspect\nbind \\cx\\cn __clai_seed\ncommand touch %s\n", adapter, state, cursor, done, seed, seedCursor, ready)
	case "bash":
		return fmt.Sprintf("source %s\n__clai_inspect() { printf '%%s' \"$READLINE_LINE\" > %s; printf '%%s:%%s' \"$READLINE_POINT\" \"${#READLINE_LINE}\" > %s; command touch %s; }\n__clai_seed() { READLINE_LINE=%s; READLINE_POINT=%d; }\nbind -x '\"\\C-x\\C-b\":__clai_inspect'\nbind -x '\"\\C-x\\C-n\":__clai_seed'\ncommand touch %s\n", adapter, state, cursor, done, seed, seedCursor, ready)
	case "zsh":
		return fmt.Sprintf("source %s\nfunction __clai_inspect() { print -rn -- \"$BUFFER\" > %s; print -rn -- \"$CURSOR:${#BUFFER}\" > %s; command touch %s; }\nfunction __clai_seed() { BUFFER=%s; CURSOR=%d; }\nzle -N clai-inspect __clai_inspect\nzle -N clai-seed __clai_seed\nbindkey '^X^B' clai-inspect\nbindkey '^X^N' clai-seed\ncommand touch %s\n", adapter, state, cursor, done, seed, seedCursor, ready)
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
