// Package candidatejson owns the canonical candidate/v1 structured-output
// contract that direct providers use to turn a model's JSON response into a
// provider.Candidate.
//
// The contract is deliberately strict: exactly one JSON object, both the command
// and explanation present and non-empty, and no unknown fields or trailing
// content. Strictness is a safety property, not a convenience one — clai never
// auto-executes a candidate, so a partial, ambiguous, or unexpectedly-shaped
// model response must fail closed rather than yield a half-formed command. This
// package performs no markdown-fence tolerance; a provider that must accept
// fenced output normalizes it before calling Decode, keeping fence tolerance out
// of the strict contract.
package candidatejson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/Vedaant-Rajoo/clai/internal/provider"
)

// SchemaVersion identifies the canonical structured-output contract this package
// enforces.
const SchemaVersion = "candidate/v1"

// wire is the exact candidate/v1 shape: a JSON object carrying only a command
// and an explanation.
type wire struct {
	Command     string `json:"command"`
	Explanation string `json:"explanation"`
}

// Decode strictly decodes exactly one candidate/v1 JSON object from data.
//
// It requires both fields, rejects unknown fields and any trailing JSON, and
// rejects an empty command or explanation. The returned command and explanation
// are untrusted model output: callers remain responsible for local validation
// and display sanitization before use. Decode performs no markdown-fence
// tolerance; callers normalize fenced output before calling.
func Decode(data []byte) (provider.Candidate, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var w wire
	if err := dec.Decode(&w); err != nil {
		return provider.Candidate{}, fmt.Errorf("candidatejson: decode %s: %w", SchemaVersion, err)
	}
	// Reject anything after the first JSON value. Once one value is consumed,
	// dec.Token returns io.EOF only when nothing but whitespace remains; any
	// further token (e.g. a second object) is trailing content and fails closed.
	if _, err := dec.Token(); err != io.EOF {
		return provider.Candidate{}, fmt.Errorf("candidatejson: unexpected trailing content after %s object", SchemaVersion)
	}
	if w.Command == "" {
		return provider.Candidate{}, errors.New("candidatejson: candidate object has an empty command")
	}
	if w.Explanation == "" {
		return provider.Candidate{}, errors.New("candidatejson: candidate object has an empty explanation")
	}
	return provider.Candidate{Command: w.Command, Explanation: w.Explanation}, nil
}

// Schema returns the JSON Schema for candidate/v1, suitable for a provider's
// native structured-output request. It mirrors what Decode enforces so the model
// is steered toward a response Decode will accept, but Decode — not the schema —
// remains the authoritative gate. A fresh map is returned on each call so callers
// may mutate it freely.
func Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command":     map[string]any{"type": "string"},
			"explanation": map[string]any{"type": "string"},
		},
		"required":             []any{"command", "explanation"},
		"additionalProperties": false,
	}
}
