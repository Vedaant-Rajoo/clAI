package machinecontext

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Vedaant-Rajoo/clai/internal/capability"
)

func TestClassifyEndpointLexically(t *testing.T) {
	tests := []struct {
		endpoint string
		want     EndpointClass
	}{
		{"http://localhost:8080/v1", EndpointLoopback},
		{"http://LOCALHOST./v1", EndpointLoopback},
		{"http://api.localhost:8080/v1", EndpointLoopback},
		{"http://127.0.0.1/v1", EndpointLoopback},
		{"http://127.99.2.3/v1", EndpointLoopback},
		{"http://[::1]:8080/v1", EndpointLoopback},
		{"http://loopback.example/v1", EndpointRemote},
		{"http://localhost.localdomain/v1", EndpointRemote},
		{"http://192.168.1.4/v1", EndpointRemote},
		{"https://openrouter.ai/api/v1/chat/completions", EndpointRemote},
	}
	for _, tt := range tests {
		t.Run(tt.endpoint, func(t *testing.T) {
			got, err := ClassifyEndpoint(tt.endpoint)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("ClassifyEndpoint() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyEndpointRejectsUserinfo(t *testing.T) {
	for _, endpoint := range []string{
		"https://username@openrouter.ai/v1",
		"https://username:password@openrouter.ai/v1",
		"http://username@localhost:8080/v1",
	} {
		if _, err := ClassifyEndpoint(endpoint); err == nil || err.Error() != "provider endpoint must not contain userinfo" {
			t.Fatalf("ClassifyEndpoint(%q) error = %v", endpoint, err)
		}
	}
}

func TestPolicyDefaults(t *testing.T) {
	if got := DefaultPolicy(true, EndpointRemote); got != PolicyLocalOnly {
		t.Fatalf("local provider default = %q", got)
	}
	if got := DefaultPolicy(false, EndpointLoopback); got != PolicyLocalOnly {
		t.Fatalf("loopback default = %q", got)
	}
	if got := DefaultPolicy(false, EndpointRemote); got != PolicyRemoteMinimal {
		t.Fatalf("remote default = %q", got)
	}
}

func TestContextV2CanonicalFieldsAndMetadata(t *testing.T) {
	inventory := fixtureInventory(map[string]capability.ToolFact{
		"rg":   {Name: "rg", Present: true, Path: "/fixture/bin/rg", Version: "14.1.0"},
		"grep": {Name: "grep", Present: true, Path: "/fixture/bin/grep"},
	})
	selection, err := Select(Context{
		WorkingDirectory: "/private/work",
		GitRepository:    true,
		GitRoot:          "/private/work",
		GitBranch:        "private-branch",
	}, inventory, PolicyRemoteMinimal, nil)
	if err != nil {
		t.Fatal(err)
	}

	wantNames := []string{FieldOSFamily, FieldShellFamily, FieldProjectKind, FieldPlatformArch}
	for _, name := range capability.ToolNames() {
		wantNames = append(wantNames, "tool_"+name)
	}
	wantNames = append(wantNames, FieldWorkingDirectory, FieldGitRoot, FieldGitBranch)

	gotNames := make([]string, 0, len(selection.Capsule.Fields))
	for _, field := range selection.Capsule.Fields {
		gotNames = append(gotNames, field.Name)
		if field.SchemaVersion != CapsuleVersion || field.Sensitivity != "low" || field.Freshness != "invocation" || field.Transformation != TransformationNormalized {
			t.Errorf("metadata mismatch for %s: %+v", field.Name, field)
		}
		if len(field.Provenance) == 0 || field.Reason == "" {
			t.Errorf("missing provenance/reason for %s: %+v", field.Name, field)
		}
		if field.Sharing == SharingRedacted {
			t.Errorf("Phase B must not redact fields: %+v", field)
		}
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("field order = %v, want %v", gotNames, wantNames)
	}
	byName := fieldsByName(selection)
	if byName[FieldOSFamily].Value != "linux" || !reflect.DeepEqual(byName[FieldOSFamily].Provenance, []string{"runtime.GOOS"}) {
		t.Fatalf("os field = %+v", byName[FieldOSFamily])
	}
	if byName[FieldShellFamily].Value != "bash" || !reflect.DeepEqual(byName[FieldShellFamily].Provenance, []string{"SHELL"}) {
		t.Fatalf("shell field = %+v", byName[FieldShellFamily])
	}
	if byName[FieldPlatformArch].Value != "arm64" {
		t.Fatalf("arch field = %+v", byName[FieldPlatformArch])
	}
	if byName["tool_rg"].Value != "present:14.1.0" || !reflect.DeepEqual(byName["tool_rg"].Provenance, []string{"exec.LookPath:rg", "probe:rg"}) {
		t.Fatalf("rg field = %+v", byName["tool_rg"])
	}
	if byName["tool_grep"].Value != "present" || !reflect.DeepEqual(byName["tool_grep"].Provenance, []string{"exec.LookPath:grep"}) {
		t.Fatalf("grep field = %+v", byName["tool_grep"])
	}
	if byName["tool_git"].Value != "absent" {
		t.Fatalf("git field = %+v", byName["tool_git"])
	}
	for _, field := range selection.Capsule.Fields {
		if strings.Contains(field.Value, "/fixture/bin") {
			t.Fatalf("field %s leaked an executable path: %+v", field.Name, field)
		}
	}
	for _, name := range explicitFieldOrder {
		if byName[name].Sharing != SharingOmitted || byName[name].Value != "" {
			t.Fatalf("minimal explicit field %s = %+v", name, byName[name])
		}
	}
}

func TestContextPolicyWireAllPolicies(t *testing.T) {
	inventory := fixtureInventory(map[string]capability.ToolFact{
		"rg": {Name: "rg", Present: true, Path: "/fixture/bin/rg", Version: "14.1.0"},
	})
	context := Context{
		WorkingDirectory: "/work",
		GitRepository:    true,
		GitRoot:          "/work",
		GitBranch:        "dev",
	}

	local, err := Select(context, inventory, PolicyLocalOnly, nil)
	if err != nil {
		t.Fatal(err)
	}
	minimal, err := Select(context, inventory, PolicyRemoteMinimal, nil)
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := Select(context, inventory, PolicyRemoteExplicit, []string{FieldGitBranch, FieldWorkingDirectory})
	if err != nil {
		t.Fatal(err)
	}

	localJSON := marshalPayload(t, ProviderPayloadFor("intent", local))
	if string(localJSON) != `{"intent":"intent"}` {
		t.Fatalf("local-only payload = %s", localJSON)
	}

	contextJSON := `{"os_family":"linux","shell_family":"bash","project_kind":"git","platform_arch":"arm64","tools":` + expectedToolsJSON(map[string]string{"rg": "present:14.1.0"})
	minimalJSON := marshalPayload(t, ProviderPayloadFor("intent", minimal))
	wantMinimal := `{"intent":"intent","context":` + contextJSON + `}}`
	if string(minimalJSON) != wantMinimal {
		t.Fatalf("minimal body = %s, want %s", minimalJSON, wantMinimal)
	}
	assertToolWireOrder(t, minimalJSON)

	explicitJSON := marshalPayload(t, ProviderPayloadFor("intent", explicit))
	wantExplicit := `{"intent":"intent","context":` + contextJSON + `,"working_directory":"/work","git_branch":"dev"}}`
	if string(explicitJSON) != wantExplicit {
		t.Fatalf("explicit body = %s, want %s", explicitJSON, wantExplicit)
	}
}

func TestNormalizeExplicitFieldsCanonicalOrder(t *testing.T) {
	fields, err := NormalizeExplicitFields([]string{FieldGitBranch, FieldWorkingDirectory, FieldGitRoot, FieldGitBranch})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{FieldWorkingDirectory, FieldGitRoot, FieldGitBranch}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("fields = %v, want %v", fields, want)
	}
	if _, err := NormalizeExplicitFields([]string{"hostname"}); err == nil {
		t.Fatal("unknown explicit field accepted")
	}
}

func TestSelectRejectsInvalidSharingCombinations(t *testing.T) {
	inventory := fixtureInventory(nil)
	if _, err := Select(Context{}, inventory, PolicyRemoteExplicit, nil); err == nil {
		t.Fatal("remote-explicit without fields accepted")
	}
	if _, err := Select(Context{}, inventory, PolicyRemoteMinimal, []string{FieldGitBranch}); err == nil {
		t.Fatal("remote-minimal with fields accepted")
	}
}

func fieldsByName(selection Selection) map[string]Field {
	result := make(map[string]Field, len(selection.Capsule.Fields))
	for _, field := range selection.Capsule.Fields {
		result[field.Name] = field
	}
	return result
}

func marshalPayload(t *testing.T, payload ProviderPayload) []byte {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func expectedToolsJSON(present map[string]string) string {
	var result strings.Builder
	result.WriteByte('[')
	for index, name := range capability.ToolNames() {
		if index > 0 {
			result.WriteByte(',')
		}
		status := present[name]
		if status == "" {
			status = "absent"
		}
		result.WriteString(`{"name":"` + name + `","status":"` + status + `"}`)
	}
	result.WriteByte(']')
	return result.String()
}

func assertToolWireOrder(t *testing.T, data []byte) {
	t.Helper()
	previous := -1
	for _, name := range capability.ToolNames() {
		index := strings.Index(string(data), `{"name":"`+name+`","status":"`)
		if index <= previous {
			t.Fatalf("tool %q out of order in %s", name, data)
		}
		previous = index
	}
}

// fixtureInventory builds a deterministic inventory through the capability
// test seam so selection semantics are asserted without real subprocess
// probes; probe execution itself is covered by internal/capability tests and
// the controlled integration fixture.
func fixtureInventory(present map[string]capability.ToolFact) capability.Inventory {
	tools := make([]capability.ToolFact, 0, len(capability.ToolNames()))
	for _, name := range capability.ToolNames() {
		if fact, ok := present[name]; ok {
			tools = append(tools, fact)
			continue
		}
		tools = append(tools, capability.ToolFact{Name: name})
	}
	shell := capability.ShellIdentity{Family: capability.ShellBash, Path: "/bin/bash", Provenance: capability.ShellFromEnv}
	return capability.NewFixtureInventory("linux", "arm64", shell, tools)
}
