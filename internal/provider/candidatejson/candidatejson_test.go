package candidatejson_test

import (
	"testing"

	"github.com/Vedaant-Rajoo/clai/internal/provider"
	"github.com/Vedaant-Rajoo/clai/internal/provider/candidatejson"
)

func TestDecode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    provider.Candidate
		wantErr bool
	}{
		{
			name:  "both fields present",
			input: `{"command":"ls -la","explanation":"lists files in long format"}`,
			want:  provider.Candidate{Command: "ls -la", Explanation: "lists files in long format"},
		},
		{
			name:  "trailing whitespace tolerated",
			input: "{\"command\":\"ls\",\"explanation\":\"lists files\"}\n   \t\n",
			want:  provider.Candidate{Command: "ls", Explanation: "lists files"},
		},
		{
			name:  "field order does not matter",
			input: `{"explanation":"prints the date","command":"date"}`,
			want:  provider.Candidate{Command: "date", Explanation: "prints the date"},
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: true,
		},
		{
			name:    "invalid json",
			input:   "{",
			wantErr: true,
		},
		{
			name:    "not an object",
			input:   `"just a string"`,
			wantErr: true,
		},
		{
			name:    "missing explanation",
			input:   `{"command":"ls"}`,
			wantErr: true,
		},
		{
			name:    "missing command",
			input:   `{"explanation":"lists files"}`,
			wantErr: true,
		},
		{
			name:    "empty command",
			input:   `{"command":"","explanation":"lists files"}`,
			wantErr: true,
		},
		{
			name:    "empty explanation",
			input:   `{"command":"ls","explanation":""}`,
			wantErr: true,
		},
		{
			name:    "unknown field rejected",
			input:   `{"command":"ls","explanation":"lists files","danger":true}`,
			wantErr: true,
		},
		{
			name:    "trailing json rejected",
			input:   `{"command":"ls","explanation":"lists files"}{}`,
			wantErr: true,
		},
		{
			name:    "trailing non-whitespace rejected",
			input:   `{"command":"ls","explanation":"lists files"} garbage`,
			wantErr: true,
		},
		{
			name:  "fences are not tolerated by the strict decoder",
			input: "```json\n{\"command\":\"ls\",\"explanation\":\"lists\"}\n```",
			// The strict contract deliberately rejects fenced output; callers
			// that must accept fences normalize before calling Decode.
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := candidatejson.Decode([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Decode(%q) = %+v, want error", tt.input, got)
				}
				if got != (provider.Candidate{}) {
					t.Fatalf("Decode(%q) returned candidate %+v on error, want zero value", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Decode(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("Decode(%q) = %+v, want %+v", tt.input, got, tt.want)
			}
		})
	}
}

func TestSchemaMirrorsStrictContract(t *testing.T) {
	t.Parallel()

	schema := candidatejson.Schema()

	if schema["type"] != "object" {
		t.Fatalf("schema type = %v, want object", schema["type"])
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("schema additionalProperties = %v, want false (unknown fields must be rejected)", schema["additionalProperties"])
	}

	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("schema required = %T, want []any", schema["required"])
	}
	if len(required) != 2 || required[0] != "command" || required[1] != "explanation" {
		t.Fatalf("schema required = %v, want [command explanation]", required)
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %T, want map[string]any", schema["properties"])
	}
	for _, field := range []string{"command", "explanation"} {
		property, present := props[field].(map[string]any)
		if !present {
			t.Fatalf("schema property %q = %T, want object", field, props[field])
		}
		if property["type"] != "string" {
			t.Fatalf("schema property %q type = %v, want string", field, property["type"])
		}
		if _, unsupported := property["minLength"]; unsupported {
			t.Fatalf("schema property %q uses unsupported minLength", field)
		}
	}

	// A fresh map per call so callers may mutate without corrupting the canonical
	// contract.
	schema["type"] = "mutated"
	if candidatejson.Schema()["type"] != "object" {
		t.Fatal("Schema() must return an independent map on each call")
	}
}
