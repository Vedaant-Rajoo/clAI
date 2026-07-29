package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestDevEndpointEndToEnd drives the real clai binary against a loopback stub
// server so the complete production request path is exercised without
// contacting a provider (REQ-DEVENDPOINT-001/004/007, REQ-ACCEPT-DEVENDPOINT-004).
//
// The widget mode is used because it is non-interactive at the transport
// boundary: it accepts a candidate through the TUI, then re-runs validation,
// safety, result-file identity, and applicability before writing. That makes the
// hard-rejection case observable end to end — a candidate that is structurally
// valid and safety-allowed, yet non-exportable purely on applicability, which no
// local rules candidate can produce.
func TestDevEndpointEndToEnd(t *testing.T) {
	binary := buildCLAI(t)
	server := startStubProvider(t)

	tests := []struct {
		name        string
		intent      string
		wantExit    int
		wantResult  string
		wantStderr  string
		description string
	}{
		{
			name:        "normal candidate exports",
			intent:      "normal listing",
			wantExit:    0,
			wantResult:  "ls -la",
			description: "a valid candidate with no requirements reaches the result file",
		},
		{
			// The review screen refuses acceptance outright, so Enter does
			// nothing and the trailing Escape cancels (exit 3). The command is
			// structurally valid and safety-allowed, so applicability alone is
			// what keeps it from ever reaching the transport boundary; the
			// widget-boundary rerun is covered by the cmd/clai unit tests.
			name:        "hard rejected candidate cannot be accepted",
			intent:      "hard-reject please",
			wantExit:    3,
			wantResult:  "",
			description: "declared os:plan9 hard-rejects in review and writes nothing",
		},
		{
			name:        "malformed payload is a provider failure",
			intent:      "malformed payload",
			wantExit:    3,
			wantResult:  "",
			description: "the strict candidate/v2 decoder rejects it and no command is exported",
		},
		{
			name:        "refusal is a provider failure",
			intent:      "refusal case",
			wantExit:    3,
			wantResult:  "",
			description: "a refusal never becomes a candidate",
		},
		{
			name:        "server error is a provider failure",
			intent:      "server-error case",
			wantExit:    3,
			wantResult:  "",
			description: "HTTP 500 surfaces as a failure with no command",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resultFile := filepath.Join(t.TempDir(), "result")
			if err := os.WriteFile(resultFile, nil, 0o600); err != nil {
				t.Fatal(err)
			}

			exitCode, stderr := runWidget(t, binary, server.URL, resultFile, tt.intent)
			if exitCode != tt.wantExit {
				t.Fatalf("%s: exit = %d, want %d (stderr: %s)", tt.description, exitCode, tt.wantExit, stderr)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr, tt.wantStderr) {
				t.Fatalf("stderr = %q, want it to mention %q", stderr, tt.wantStderr)
			}

			data, err := os.ReadFile(resultFile)
			switch {
			case tt.wantResult == "":
				// A non-exporting outcome either removes the transport file or
				// leaves it empty; both prove the shell buffer stays unchanged.
				if err == nil && len(data) != 0 {
					t.Fatalf("%s: result file contains %q, want no exported command", tt.description, data)
				}
			default:
				if err != nil {
					t.Fatalf("read result: %v", err)
				}
				if string(data) != tt.wantResult {
					t.Fatalf("result = %q, want %q", data, tt.wantResult)
				}
			}
		})
	}
}

// TestDevEndpointRejectsNonLoopbackBinary proves the shipped binary refuses a
// remote development endpoint before any network activity (REQ-DEVENDPOINT-002).
func TestDevEndpointRejectsNonLoopbackBinary(t *testing.T) {
	binary := buildCLAI(t)
	for _, endpoint := range []string{
		"https://api.anthropic.com",
		"http://example.com",
		"http://localhost.example.com",
	} {
		cmd := exec.Command(binary, "widget", "--shell", "bash", "--result-file", "/tmp/does-not-matter",
			"--provider", "anthropic", "--api-key", "stub", "--dev-endpoint", endpoint)
		output, err := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
			t.Fatalf("endpoint %q: err = %v, want usage exit 2 (output: %s)", endpoint, err, output)
		}
		if !strings.Contains(string(output), "loopback") {
			t.Fatalf("endpoint %q: output = %s, want a loopback rejection", endpoint, output)
		}
	}
}

// runWidget runs the widget non-interactively by feeding the TUI a scripted key
// sequence: the intent, Enter to submit, then Enter to accept.
func runWidget(t *testing.T, binary, endpoint, resultFile, intent string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, "widget",
		"--shell", "bash",
		"--result-file", resultFile,
		"--provider", "anthropic",
		"--api-key", "stub-key-not-real",
		"--dev-endpoint", endpoint,
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	// Widget mode drives the TUI on a controlling terminal, so it needs a PTY.
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 32, Cols: 120})
	if err != nil {
		t.Skipf("PTY unavailable: %v", err)
	}
	defer ptmx.Close()

	// Drain continuously so the child never blocks, answer the terminal
	// capability queries Bubble Tea sends at startup (OSC 11 background color and
	// DSR 6 cursor position, without which the TUI never finishes initializing),
	// and accumulate the screen so the driver can synchronize on what is actually
	// rendered instead of on fixed sleeps.
	var mu sync.Mutex
	var screen strings.Builder
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				mu.Lock()
				screen.WriteString(chunk)
				mu.Unlock()
				if strings.Contains(chunk, "]11;?") {
					_, _ = ptmx.WriteString("\x1b]11;rgb:0000/0000/0000\x07")
				}
				if strings.Contains(chunk, "[6n") {
					_, _ = ptmx.WriteString("\x1b[1;1R")
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// waitFor polls the accumulated screen for a rendered marker. Observable
	// synchronization keeps the driver reliable when the machine is loaded and
	// the whole suite runs in parallel; a fixed sleep would race the TUI.
	waitFor := func(what string, markers ...string) bool {
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			current := screen.String()
			mu.Unlock()
			for _, marker := range markers {
				if strings.Contains(current, marker) {
					return true
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Logf("timed out waiting for %s; screen so far:\n%s", what, screen.String())
		return false
	}

	// Type the intent once the prompt is up, submit, then attempt acceptance once
	// the request has resolved into review, a failure, or the no-suggestion state.
	if !waitFor("the intent prompt", "What do you want to do?") {
		t.Fatal("TUI never rendered the intent prompt")
	}
	_, _ = ptmx.WriteString(intent + "\r")
	if !waitFor("a resolved request", "enter accept", "enter inapplicable", "Error:", "No suggestion") {
		t.Fatal("TUI never left the loading state")
	}
	_, _ = ptmx.WriteString("\r")
	// Escape guarantees the process exits even from a non-accepting state. Give
	// an accepted command a moment to reach the transport first.
	time.Sleep(300 * time.Millisecond)
	_, _ = ptmx.WriteString("\x1b")

	err = cmd.Wait()
	exitCode := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("wait: %v (stderr: %s)", err, stderr.String())
	}
	return exitCode, stderr.String()
}

// buildCLAI builds the real binary under test once per run.
func buildCLAI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "clai")
	build := exec.Command("go", "build", "-o", binary, "../cmd/clai")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build clai: %v\n%s", err, output)
	}
	return binary
}

// startStubProvider runs the scripted Anthropic-shaped server in-process on
// loopback, mirroring cmd/clai-stubprovider's scenarios.
func startStubProvider(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		intent := strings.ToLower(stubIntent(body))

		switch {
		case strings.Contains(intent, "server-error"):
			http.Error(w, `{"type":"error","error":{"type":"api_error","message":"stub"}}`, http.StatusInternalServerError)
			return
		case strings.Contains(intent, "hang"):
			<-r.Context().Done()
			return
		}

		candidate := `{"command":"ls -la","explanation":"lists files in long format"}`
		stop := "end_turn"
		switch {
		case strings.Contains(intent, "hard-reject"):
			candidate = `{"command":"echo hello","explanation":"only for plan9","requirements":[{"kind":"os","name":"plan9"}]}`
		case strings.Contains(intent, "malformed"):
			candidate = `{"command":"ls","explanation":"lists","unexpected":"field"}`
		case strings.Contains(intent, "refusal"):
			stop = "refusal"
		case strings.Contains(intent, "truncated"):
			stop = "max_tokens"
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		emit := func(event, data string) {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
			if flusher != nil {
				flusher.Flush()
			}
		}
		emit("message_start", `{"type":"message_start","message":{"id":"msg_stub","type":"message","role":"assistant","model":"stub","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`)
		emit("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		chunk, _ := json.Marshal(candidate)
		emit("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%s}}`, chunk))
		emit("content_block_stop", `{"type":"content_block_stop","index":0}`)
		emit("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":1}}`, stop))
		emit("message_stop", `{"type":"message_stop"}`)
	}))
	t.Cleanup(server.Close)

	host, _, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("stub server is not on loopback: %s", server.URL)
	}
	return server
}

func stubIntent(body []byte) string {
	var envelope struct {
		Messages []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	for _, m := range envelope.Messages {
		for _, c := range m.Content {
			var payload struct {
				Intent string `json:"intent"`
			}
			if err := json.Unmarshal([]byte(c.Text), &payload); err == nil && payload.Intent != "" {
				return payload.Intent
			}
		}
	}
	return ""
}
