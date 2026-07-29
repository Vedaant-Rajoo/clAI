package candidatejson_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Vedaant-Rajoo/clai/internal/capability"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
	"github.com/Vedaant-Rajoo/clai/internal/provider/candidatejson"
)

func TestCandidateV2AcceptsV1Shape(t *testing.T) {
	candidate, err := candidatejson.Decode([]byte(`{"command":"ls -la","explanation":"lists files"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := provider.Candidate{Command: "ls -la", Explanation: "lists files", Requirements: []capability.Requirement{}}
	if !reflect.DeepEqual(candidate, want) {
		t.Fatalf("candidate = %#v, want %#v", candidate, want)
	}
}

func TestCandidateV2StrictDecodeAndBounds(t *testing.T) {
	eight := make([]string, 8)
	for index := range eight {
		eight[index] = `{"kind":"tool","name":"tool-` + string(rune('a'+index)) + `"}`
	}

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"zero requirements", `{"command":"pwd","explanation":"prints cwd","requirements":[]}`, false},
		{"all kinds", `{"command":"x","explanation":"x","requirements":[{"kind":"tool","name":"git","min_version":"2.40"},{"kind":"shell","name":"bash"},{"kind":"os","name":"darwin"}]}`, false},
		{"eight requirements", `{"command":"x","explanation":"x","requirements":[` + strings.Join(eight, ",") + `]}`, false},
		{"ninth requirement", `{"command":"x","explanation":"x","requirements":[` + strings.Join(append(eight, `{"kind":"tool","name":"ninth"}`), ",") + `]}`, true},
		{"unknown top field", `{"command":"x","explanation":"x","extra":true}`, true},
		{"unknown requirement field", `{"command":"x","explanation":"x","requirements":[{"kind":"tool","name":"git","extra":true}]}`, true},
		{"missing command", `{"explanation":"x"}`, true},
		{"missing explanation", `{"command":"x"}`, true},
		{"missing kind", `{"command":"x","explanation":"x","requirements":[{"name":"git"}]}`, true},
		{"missing name", `{"command":"x","explanation":"x","requirements":[{"kind":"tool"}]}`, true},
		{"unknown kind", `{"command":"x","explanation":"x","requirements":[{"kind":"package","name":"git"}]}`, true},
		{"uppercase name", `{"command":"x","explanation":"x","requirements":[{"kind":"tool","name":"Git"}]}`, true},
		{"unicode name", `{"command":"x","explanation":"x","requirements":[{"kind":"tool","name":"gít"}]}`, true},
		{"whitespace name", `{"command":"x","explanation":"x","requirements":[{"kind":"tool","name":"git status"}]}`, true},
		{"path separator name", `{"command":"x","explanation":"x","requirements":[{"kind":"tool","name":"bin/git"}]}`, true},
		{"empty name", `{"command":"x","explanation":"x","requirements":[{"kind":"tool","name":""}]}`, true},
		{"name too long", `{"command":"x","explanation":"x","requirements":[{"kind":"tool","name":"` + strings.Repeat("a", 65) + `"}]}`, true},
		{"valid punctuation", `{"command":"x","explanation":"x","requirements":[{"kind":"tool","name":"a.b_c-d0"}]}`, false},
		{"valid 64 byte name", `{"command":"x","explanation":"x","requirements":[{"kind":"tool","name":"` + strings.Repeat("a", 64) + `"}]}`, false},
		{"null requirements", `{"command":"x","explanation":"x","requirements":null}`, true},
		{"null min version", `{"command":"x","explanation":"x","requirements":[{"kind":"tool","name":"git","min_version":null}]}`, true},
		{"empty min version", `{"command":"x","explanation":"x","requirements":[{"kind":"tool","name":"git","min_version":""}]}`, true},
		{"leading v version", `{"command":"x","explanation":"x","requirements":[{"kind":"tool","name":"git","min_version":"v2.40"}]}`, true},
		{"version suffix", `{"command":"x","explanation":"x","requirements":[{"kind":"tool","name":"git","min_version":"2.40-beta"}]}`, true},
		{"version too long", `{"command":"x","explanation":"x","requirements":[{"kind":"tool","name":"git","min_version":"` + strings.Repeat("1", 33) + `"}]}`, true},
		{"shell min version", `{"command":"x","explanation":"x","requirements":[{"kind":"shell","name":"bash","min_version":"5"}]}`, true},
		{"os min version", `{"command":"x","explanation":"x","requirements":[{"kind":"os","name":"darwin","min_version":"1"}]}`, true},
		{"trailing object", `{"command":"x","explanation":"x"}{}`, true},
		{"trailing garbage", `{"command":"x","explanation":"x"} garbage`, true},
		{"markdown fence", "```json\n{\"command\":\"x\",\"explanation\":\"x\"}\n```", true},
	}
	for count := 0; count <= 8; count++ {
		tests = append(tests, struct {
			name    string
			input   string
			wantErr bool
		}{
			name:  fmt.Sprintf("valid requirement cardinality %d", count),
			input: `{"command":"x","explanation":"x","requirements":[` + strings.Join(eight[:count], ",") + `]}`,
		})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate, err := candidatejson.Decode([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Decode(%s) = %#v, want error", tt.input, candidate)
				}
				if !reflect.DeepEqual(candidate, provider.Candidate{}) {
					t.Fatalf("candidate on error = %#v", candidate)
				}
				return
			}
			if err != nil {
				t.Fatalf("Decode(%s): %v", tt.input, err)
			}
		})
	}
}

func TestSchemaMirrorsStrictContract(t *testing.T) {
	schema := candidatejson.Schema()
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("root schema = %#v", schema)
	}
	required := schema["required"].([]any)
	if !reflect.DeepEqual(required, []any{"command", "explanation"}) {
		t.Fatalf("required = %v", required)
	}
	encodedBytes, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(encodedBytes)
	for _, unsupported := range []string{"minLength", "maxLength", "pattern", "maxItems"} {
		if strings.Contains(encoded, unsupported) {
			t.Fatalf("schema contains unsupported constraint %q: %s", unsupported, encoded)
		}
	}
	if strings.Count(encoded, `"additionalProperties":false`) < 3 {
		t.Fatalf("every object schema must reject additional properties: %s", encoded)
	}

	schema["type"] = "mutated"
	if candidatejson.Schema()["type"] != "object" {
		t.Fatal("Schema must return an independent value")
	}
}
