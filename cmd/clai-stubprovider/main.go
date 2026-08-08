// Command clai-stubprovider is a development-only server that speaks the
// Anthropic Messages streaming and OpenRouter chat-completions wire formats.
// It lets `clai --dev-endpoint` exercise the complete production request path —
// credential header, context selection, request serialization, receipt
// computation, transport, streaming accumulation, strict candidate/v2 decoding,
// applicability, review, and widget export — without contacting a real provider
// (REQ-DEVENDPOINT-007).
//
// It binds a loopback address only, is never imported by the clai binary, and
// holds no credentials. The scenario is selected by the incoming intent text so
// a single running server covers every case.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// scenario is one scripted provider behavior.
type scenario struct {
	// name is the keyword matched inside the intent.
	name string
	// summary documents the behavior in --list output.
	summary string
	// candidate is the raw candidate/v2 JSON the model "returns". Ignored when
	// the scenario short-circuits before producing text.
	candidate string
	// stopReason overrides the terminating stop_reason (default end_turn).
	stopReason string
	// status, when non-zero, short-circuits with that HTTP status.
	status int
	// redirect issues a 302 to this location instead of answering.
	redirect string
	// hang holds the request open so the caller can prove cancellation.
	hang bool
}

var scenarios = []scenario{
	{
		name:      "normal",
		summary:   "one valid candidate with no declared requirements",
		candidate: `{"command":"ls -la","explanation":"lists files in long format"}`,
	},
	{
		name:      "requires-rg",
		summary:   "candidate declaring tool rg (soft-marks when rg is absent)",
		candidate: `{"command":"rg TODO","explanation":"searches with ripgrep","requirements":[{"kind":"tool","name":"rg"}]}`,
	},
	{
		name:    "hard-reject",
		summary: "candidate declaring an OS that cannot match this machine (hard rejection, non-exportable)",
		// plan9 is a valid normalized OS name, so this decodes cleanly and is
		// rejected by the applicability gate rather than by the decoder.
		candidate: `{"command":"echo hello","explanation":"only for plan9","requirements":[{"kind":"os","name":"plan9"}]}`,
	},
	{
		name:      "malformed",
		summary:   "payload the strict candidate/v2 decoder must reject (unknown field)",
		candidate: `{"command":"ls","explanation":"lists","unexpected":"field"}`,
	},
	{
		name:       "truncated",
		summary:    "max_tokens termination: provider failure, no candidate",
		candidate:  `{"command":"ls -la","explanation":"lists fi`,
		stopReason: "max_tokens",
	},
	{
		name:       "refusal",
		summary:    "model refusal: provider failure, no candidate",
		candidate:  `{"command":"","explanation":""}`,
		stopReason: "refusal",
	},
	{
		name:    "server-error",
		summary: "HTTP 500: transient upstream failure",
		status:  http.StatusInternalServerError,
	},
	{
		name:     "redirect",
		summary:  "302 redirect: provider must refuse to follow it",
		redirect: "https://example.invalid/redirected",
	},
	{
		name:    "hang",
		summary: "never responds, so Escape/timeout proves cancellation",
		hang:    true,
	},
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8747", "loopback address to bind")
	list := flag.Bool("list", false, "list the scripted scenarios and exit")
	flag.Parse()

	if *list {
		for _, s := range scenarios {
			fmt.Printf("%-14s %s\n", s.name, s.summary)
		}
		return
	}

	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clai-stubprovider: invalid --addr %q: %v\n", *addr, err)
		os.Exit(2)
	}
	// Refuse to serve on anything but loopback: this server answers without
	// authenticating and must never be reachable off-host.
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		fmt.Fprintf(os.Stderr, "clai-stubprovider: --addr must be a loopback IP, got %q\n", host)
		os.Exit(2)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", handleAnthropic)
	mux.HandleFunc("/v1/chat/completions", handleOpenRouter)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "clai-stubprovider: unknown path "+r.URL.Path, http.StatusNotFound)
	})

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clai-stubprovider: listen: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("clai-stubprovider listening on http://%s\n", listener.Addr())
	fmt.Println("run: clai --provider anthropic --dev-endpoint http://" + listener.Addr().String())
	fmt.Println("intents select the scenario; use --list to see them")
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := server.Serve(listener); err != nil {
		fmt.Fprintf(os.Stderr, "clai-stubprovider: serve: %v\n", err)
		os.Exit(1)
	}
}

// pick selects the scenario named in the intent, defaulting to the first.
func pick(intent string) scenario {
	lowered := strings.ToLower(intent)
	for _, s := range scenarios {
		if containsWord(lowered, s.name) {
			return s
		}
	}
	return scenarios[0]
}

func containsWord(text, word string) bool {
	for offset := 0; offset <= len(text)-len(word); {
		index := strings.Index(text[offset:], word)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(word)
		leftBoundary := start == 0
		if !leftBoundary {
			left, _ := utf8.DecodeLastRuneInString(text[:start])
			leftBoundary = !scenarioWordRune(left)
		}
		rightBoundary := end == len(text)
		if !rightBoundary {
			right, _ := utf8.DecodeRuneInString(text[end:])
			rightBoundary = !scenarioWordRune(right)
		}
		if leftBoundary && rightBoundary {
			return true
		}
		offset = start + 1
	}
	return false
}

func scenarioWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// intentFromBody extracts the intent out of whatever request shape arrived. The
// body is the JSON payload clai builds, embedded as message content.
func intentFromBody(body []byte) string {
	var anthropicShape struct {
		Messages []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &anthropicShape); err == nil {
		for _, m := range anthropicShape.Messages {
			for _, c := range m.Content {
				if intent := intentFromPayload(c.Text); intent != "" {
					return intent
				}
			}
		}
	}
	var openRouterShape struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &openRouterShape); err == nil {
		for _, m := range openRouterShape.Messages {
			if intent := intentFromPayload(m.Content); intent != "" {
				return intent
			}
		}
	}
	return ""
}

func intentFromPayload(payload string) string {
	var envelope struct {
		Intent string `json:"intent"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err == nil {
		return envelope.Intent
	}
	return ""
}

// prelude handles the scenarios that answer before producing any model text.
// It returns true when the request is finished.
func prelude(w http.ResponseWriter, r *http.Request, s scenario) bool {
	if s.hang {
		<-r.Context().Done()
		return true
	}
	if s.redirect != "" {
		http.Redirect(w, r, s.redirect, http.StatusFound)
		return true
	}
	if s.status != 0 {
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"stub scenario"}}`, s.status)
		return true
	}
	return false
}

func handleAnthropic(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s := pick(intentFromBody(body))
	logRequest(r, s)
	if prelude(w, r, s) {
		return
	}

	stop := s.stopReason
	if stop == "" {
		stop = "end_turn"
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
	chunk, _ := json.Marshal(s.candidate)
	emit("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%s}}`, chunk))
	emit("content_block_stop", `{"type":"content_block_stop","index":0}`)
	emit("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":1}}`, stop))
	emit("message_stop", `{"type":"message_stop"}`)
}

func handleOpenRouter(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s := pick(intentFromBody(body))
	logRequest(r, s)
	if prelude(w, r, s) {
		return
	}

	finish := "stop"
	if s.stopReason == "max_tokens" {
		finish = "length"
	}
	response := map[string]any{
		"id":      "chatcmpl-stub",
		"object":  "chat.completion",
		"model":   "stub",
		"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": map[string]any{"role": "assistant", "content": s.candidate}}},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// logRequest reports the scenario and whether a credential header was present,
// without ever printing the credential itself.
func logRequest(r *http.Request, s scenario) {
	credential := "absent"
	if r.Header.Get("x-api-key") != "" || r.Header.Get("Authorization") != "" {
		credential = "present (value not logged)"
	}
	fmt.Printf("%s %s -> scenario %q, credential header %s\n", r.Method, r.URL.Path, s.name, credential)
}
