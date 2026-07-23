package machinecontext

import (
	"reflect"
	"testing"
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
		t.Run(endpoint, func(t *testing.T) {
			if _, err := ClassifyEndpoint(endpoint); err == nil || err.Error() != "provider endpoint must not contain userinfo" {
				t.Fatalf("err = %v, want userinfo rejection", err)
			}
		})
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

func TestSelectRemoteMinimalMetadataAndOrdering(t *testing.T) {
	selection, err := Select(Context{
		OS: "DARWIN", Shell: "/opt/homebrew/bin/fish", ShellProvenance: "widget-declared-shell", WorkingDirectory: "/secret/project",
		GitRepository: true, GitRoot: "/secret/project", GitBranch: "private-branch",
	}, PolicyRemoteMinimal, nil)
	if err != nil {
		t.Fatal(err)
	}

	wantNames := []string{FieldOSFamily, FieldShellFamily, FieldProjectKind, FieldWorkingDirectory, FieldGitRoot, FieldGitBranch}
	gotNames := make([]string, 0, len(selection.Capsule.Fields))
	for _, field := range selection.Capsule.Fields {
		gotNames = append(gotNames, field.Name)
		if field.SchemaVersion != CapsuleVersion || field.Provenance == "" || field.Sensitivity == "" || field.Freshness == "" || field.Reason == "" {
			t.Errorf("incomplete metadata: %+v", field)
		}
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("field order = %v, want %v", gotNames, wantNames)
	}
	for _, field := range selection.Capsule.Fields[:3] {
		if field.Sharing != SharingSelected || field.Value == "" || field.Reason != "selected by remote-minimal baseline" {
			t.Errorf("minimal field metadata mismatch: %+v", field)
		}
	}
	if got := selection.Capsule.Fields[1].Provenance; got != "widget-declared-shell" {
		t.Fatalf("shell provenance = %q, want widget-declared-shell", got)
	}
	for _, field := range selection.Capsule.Fields[3:] {
		if field.Sharing != SharingRedacted || field.Value != "" || field.Transformation != TransformationRedacted || field.Reason != "not included by remote-minimal policy" {
			t.Errorf("sensitive field redaction mismatch: %+v", field)
		}
	}
}

func TestSelectManualContextUsesProviderRequestShellProvenance(t *testing.T) {
	selection, err := Select(Context{OS: "linux", Shell: "/bin/bash"}, PolicyRemoteMinimal, nil)
	if err != nil {
		t.Fatal(err)
	}
	field := selection.Capsule.Fields[1]
	if field.Name != FieldShellFamily || field.Provenance != "provider-request" || field.Value != "bash" {
		t.Fatalf("manual shell metadata = %+v, want provider-request provenance", field)
	}
}

func TestSelectRemoteExplicitDedupeAndUnavailable(t *testing.T) {
	fields, err := NormalizeExplicitFields([]string{FieldGitBranch, FieldWorkingDirectory, FieldGitBranch})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{FieldGitBranch, FieldWorkingDirectory}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("fields = %v, want %v", fields, want)
	}

	selection, err := Select(Context{
		OS: "linux", Shell: "bash", ShellProvenance: "SHELL", WorkingDirectory: "/work",
		GitRepository: true, GitRoot: "/work",
	}, PolicyRemoteExplicit, fields)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Field{}
	for _, field := range selection.Capsule.Fields {
		byName[field.Name] = field
	}
	if field := byName[FieldWorkingDirectory]; field.Sharing != SharingSelected || field.Reason != "approved by invocation-scoped remote-explicit allowlist" {
		t.Fatalf("working directory = %+v", field)
	}
	if field := byName[FieldShellFamily]; field.Provenance != "SHELL" || field.Reason != "selected by remote-explicit baseline" {
		t.Fatalf("shell family = %+v", field)
	}
	if field := byName[FieldGitRoot]; field.Sharing != SharingRedacted || field.Transformation != TransformationRedacted || field.Reason != "not approved by invocation-scoped remote-explicit allowlist" {
		t.Fatalf("git root = %+v", field)
	}
	if field := byName[FieldGitBranch]; field.Sharing != SharingOmitted || field.Transformation != TransformationOmitted || field.Reason != "approved field unavailable from filesystem-git-HEAD" {
		t.Fatalf("git branch = %+v", field)
	}
}

func TestSelectLocalOnlyReasonsDistinguishRedactedAndOmitted(t *testing.T) {
	selection, err := Select(Context{OS: "linux", Shell: "bash", ShellProvenance: "SHELL", WorkingDirectory: "/work"}, PolicyLocalOnly, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Field{}
	for _, field := range selection.Capsule.Fields {
		byName[field.Name] = field
	}
	if field := byName[FieldWorkingDirectory]; field.Sharing != SharingRedacted || field.Reason != "local-only policy prohibits context sharing" {
		t.Fatalf("working directory = %+v", field)
	}
	if field := byName[FieldGitBranch]; field.Sharing != SharingOmitted || field.Reason != "field unavailable from filesystem-git-HEAD" {
		t.Fatalf("git branch = %+v", field)
	}
}

func TestSelectRejectsInvalidSharingCombinations(t *testing.T) {
	if _, err := Select(Context{}, PolicyRemoteExplicit, nil); err == nil {
		t.Fatal("remote-explicit without fields accepted")
	}
	if _, err := Select(Context{}, PolicyRemoteMinimal, []string{FieldGitBranch}); err == nil {
		t.Fatal("remote-minimal with fields accepted")
	}
	if _, err := NormalizeExplicitFields([]string{"hostname"}); err == nil {
		t.Fatal("unknown shared field accepted")
	}
}
