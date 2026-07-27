// Package candidatejson owns the canonical candidate/v2 structured-output
// contract shared by all JSON-producing providers.
package candidatejson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/Vedaant-Rajoo/clai/internal/capability"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
)

const SchemaVersion = "candidate/v2"

const maxRequirements = 8

var requirementNamePattern = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)

type wire struct {
	Command      string          `json:"command"`
	Explanation  string          `json:"explanation"`
	Requirements requirementList `json:"requirements,omitempty"`
}

type requirementList []requirementWire

func (requirements *requirementList) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("requirements must be an array, not null")
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	decoded := make([]requirementWire, 0, len(raw))
	for _, item := range raw {
		decoder := json.NewDecoder(bytes.NewReader(item))
		decoder.DisallowUnknownFields()
		var requirement requirementWire
		if err := decoder.Decode(&requirement); err != nil {
			return err
		}
		decoded = append(decoded, requirement)
	}
	*requirements = decoded
	return nil
}

type optionalVersion struct {
	present bool
	value   string
}

func (version *optionalVersion) UnmarshalJSON(data []byte) error {
	version.present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("min_version must be a string, not null")
	}
	return json.Unmarshal(data, &version.value)
}

type requirementWire struct {
	Kind       string          `json:"kind"`
	Name       string          `json:"name"`
	MinVersion optionalVersion `json:"min_version,omitempty"`
}

// Decode strictly decodes exactly one candidate/v2 object. Bounds and grammar
// are enforced locally because the provider-facing structured-output schema
// intentionally uses only JSON Schema features supported by Anthropic.
func Decode(data []byte) (provider.Candidate, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var candidate wire
	if err := decoder.Decode(&candidate); err != nil {
		return provider.Candidate{}, fmt.Errorf("candidatejson: decode %s: %w", SchemaVersion, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return provider.Candidate{}, fmt.Errorf("candidatejson: unexpected trailing content after %s object", SchemaVersion)
		}
		return provider.Candidate{}, fmt.Errorf("candidatejson: unexpected trailing content after %s object: %w", SchemaVersion, err)
	}
	if candidate.Command == "" {
		return provider.Candidate{}, errors.New("candidatejson: candidate object has an empty or missing command")
	}
	if candidate.Explanation == "" {
		return provider.Candidate{}, errors.New("candidatejson: candidate object has an empty or missing explanation")
	}
	if len(candidate.Requirements) > maxRequirements {
		return provider.Candidate{}, fmt.Errorf("candidatejson: candidate has %d requirements, maximum is %d", len(candidate.Requirements), maxRequirements)
	}

	requirements := make([]capability.Requirement, 0, len(candidate.Requirements))
	for index, declared := range candidate.Requirements {
		kind := capability.RequirementKind(declared.Kind)
		if !kind.Valid() {
			return provider.Candidate{}, fmt.Errorf("candidatejson: requirement %d has invalid kind", index)
		}
		if !requirementNamePattern.MatchString(declared.Name) {
			return provider.Candidate{}, fmt.Errorf("candidatejson: requirement %d has invalid name", index)
		}

		requirement := capability.Requirement{Kind: kind, Name: declared.Name}
		if declared.MinVersion.present {
			if kind != capability.RequirementTool {
				return provider.Candidate{}, fmt.Errorf("candidatejson: requirement %d uses min_version for non-tool kind", index)
			}
			version, ok := capability.ParseVersion(declared.MinVersion.value)
			if !ok {
				return provider.Candidate{}, fmt.Errorf("candidatejson: requirement %d has invalid min_version", index)
			}
			requirement.MinVersion = version
		}
		requirements = append(requirements, requirement)
	}

	return provider.Candidate{
		Command:      candidate.Command,
		Explanation:  candidate.Explanation,
		Requirements: requirements,
	}, nil
}

// Schema returns a fresh Anthropic-compatible JSON Schema for candidate/v2.
// Unsupported constraints such as minLength, maxLength, pattern, and maxItems
// are deliberately absent; Decode is the authoritative local gate.
func Schema() map[string]any {
	stringProperty := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	requirementObject := func(kinds []any, includeVersion bool) map[string]any {
		properties := map[string]any{
			"kind": map[string]any{"type": "string", "enum": kinds},
			"name": stringProperty("1-64 ASCII bytes matching ^[a-z0-9._-]{1,64}$"),
		}
		if includeVersion {
			properties["min_version"] = stringProperty("Optional 1-32 byte numeric dotted version matching ^[0-9]+(?:\\.[0-9]+)*$")
		}
		return map[string]any{
			"type":                 "object",
			"properties":           properties,
			"required":             []any{"kind", "name"},
			"additionalProperties": false,
		}
	}

	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command":     stringProperty("Required non-empty shell command"),
			"explanation": stringProperty("Required non-empty one-sentence explanation"),
			"requirements": map[string]any{
				"type":        "array",
				"description": "Optional array containing at most eight actual command dependencies",
				"items": map[string]any{
					"anyOf": []any{
						requirementObject([]any{"tool"}, true),
						requirementObject([]any{"shell", "os"}, false),
					},
				},
			},
		},
		"required":             []any{"command", "explanation"},
		"additionalProperties": false,
	}
}
