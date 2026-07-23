package machinecontext

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	CapsuleVersion  = "context-capsule/v1"
	SelectorVersion = "context-selector/v1"
)

type Policy string

const (
	PolicyLocalOnly      Policy = "local-only"
	PolicyRemoteMinimal  Policy = "remote-minimal"
	PolicyRemoteExplicit Policy = "remote-explicit"
)

type EndpointClass string

const (
	EndpointLoopback EndpointClass = "loopback"
	EndpointRemote   EndpointClass = "remote"
)

type Transformation string

const (
	TransformationRaw        Transformation = "raw"
	TransformationNormalized Transformation = "normalized"
	TransformationRedacted   Transformation = "redacted"
	TransformationInferred   Transformation = "inferred"
	TransformationOmitted    Transformation = "omitted"
)

type SharingDecision string

const (
	SharingSelected SharingDecision = "selected"
	SharingRedacted SharingDecision = "redacted"
	SharingOmitted  SharingDecision = "omitted"
)

const (
	FieldOSFamily         = "os_family"
	FieldShellFamily      = "shell_family"
	FieldProjectKind      = "project_kind"
	FieldWorkingDirectory = "working_directory"
	FieldGitRoot          = "git_root"
	FieldGitBranch        = "git_branch"
)

var explicitFields = map[string]bool{
	FieldWorkingDirectory: true,
	FieldGitRoot:          true,
	FieldGitBranch:        true,
}

type Field struct {
	Name           string          `json:"name"`
	SchemaVersion  string          `json:"schema_version"`
	Value          string          `json:"value,omitempty"`
	Provenance     string          `json:"provenance"`
	Sensitivity    string          `json:"sensitivity"`
	Freshness      string          `json:"freshness,omitempty"`
	Transformation Transformation  `json:"transformation"`
	Sharing        SharingDecision `json:"sharing_decision"`
	Reason         string          `json:"reason"`
}

type Capsule struct {
	Version string  `json:"version"`
	Fields  []Field `json:"fields"`
}

type Selection struct {
	Policy  Policy
	Capsule Capsule
}

func ParsePolicy(value string) (Policy, error) {
	policy := Policy(value)
	switch policy {
	case PolicyLocalOnly, PolicyRemoteMinimal, PolicyRemoteExplicit:
		return policy, nil
	default:
		return "", fmt.Errorf("invalid context policy %q (want local-only, remote-minimal, or remote-explicit)", value)
	}
}

func IsExplicitField(name string) bool { return explicitFields[name] }

func NormalizeExplicitFields(fields []string) ([]string, error) {
	seen := make(map[string]bool, len(fields))
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if !IsExplicitField(field) {
			return nil, fmt.Errorf("invalid shared context field %q (want working_directory, git_root, or git_branch)", field)
		}
		if !seen[field] {
			seen[field] = true
			result = append(result, field)
		}
	}
	sort.Strings(result)
	return result, nil
}

func ClassifyEndpoint(endpoint string) (EndpointClass, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid provider endpoint %q", endpoint)
	}
	if parsed.User != nil {
		return "", errors.New("provider endpoint must not contain userinfo")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return EndpointLoopback, nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return EndpointLoopback, nil
	}
	return EndpointRemote, nil
}

func DefaultPolicy(localProvider bool, class EndpointClass) Policy {
	if localProvider || class == EndpointLoopback {
		return PolicyLocalOnly
	}
	return PolicyRemoteMinimal
}

func Select(c Context, policy Policy, shared []string) (Selection, error) {
	if _, err := ParsePolicy(string(policy)); err != nil {
		return Selection{}, err
	}
	shared, err := NormalizeExplicitFields(shared)
	if err != nil {
		return Selection{}, err
	}
	if policy == PolicyRemoteExplicit && len(shared) == 0 {
		return Selection{}, fmt.Errorf("remote-explicit requires at least one shared context field")
	}
	if policy != PolicyRemoteExplicit && len(shared) != 0 {
		return Selection{}, fmt.Errorf("shared context fields require remote-explicit")
	}

	allowed := make(map[string]bool, len(shared)+3)
	if policy != PolicyLocalOnly {
		allowed[FieldOSFamily] = true
		allowed[FieldShellFamily] = true
		allowed[FieldProjectKind] = true
	}
	for _, name := range shared {
		allowed[name] = true
	}

	values := []Field{
		field(FieldOSFamily, normalizeOS(c.OS), "runtime.GOOS", "low", TransformationNormalized),
		field(FieldShellFamily, normalizeShell(c.Shell), shellProvenance(c), "low", TransformationNormalized),
		field(FieldProjectKind, projectKind(c), "filesystem-git-discovery", "low", TransformationInferred),
		field(FieldWorkingDirectory, c.WorkingDirectory, "os.Getwd", "sensitive", TransformationRaw),
		field(FieldGitRoot, c.GitRoot, "filesystem-git-root", "sensitive", TransformationRaw),
		field(FieldGitBranch, c.GitBranch, "filesystem-git-HEAD", "sensitive", TransformationRaw),
	}

	for i := range values {
		f := &values[i]
		if f.Value == "" {
			f.Transformation = TransformationOmitted
			f.Sharing = SharingOmitted
			switch {
			case policy == PolicyRemoteExplicit && allowed[f.Name] && IsExplicitField(f.Name):
				f.Reason = "approved field unavailable from " + f.Provenance
			case allowed[f.Name]:
				f.Reason = "policy-selected field unavailable from " + f.Provenance
			default:
				f.Reason = "field unavailable from " + f.Provenance
			}
			continue
		}
		if allowed[f.Name] {
			f.Sharing = SharingSelected
			switch {
			case IsExplicitField(f.Name):
				f.Reason = "approved by invocation-scoped remote-explicit allowlist"
			case policy == PolicyRemoteExplicit:
				f.Reason = "selected by remote-explicit baseline"
			default:
				f.Reason = "selected by remote-minimal baseline"
			}
			continue
		}
		f.Value = ""
		f.Transformation = TransformationRedacted
		f.Sharing = SharingRedacted
		switch policy {
		case PolicyLocalOnly:
			f.Reason = "local-only policy prohibits context sharing"
		case PolicyRemoteMinimal:
			f.Reason = "not included by remote-minimal policy"
		case PolicyRemoteExplicit:
			f.Reason = "not approved by invocation-scoped remote-explicit allowlist"
		}
	}

	return Selection{Policy: policy, Capsule: Capsule{Version: CapsuleVersion, Fields: values}}, nil
}

func field(name, value, provenance, sensitivity string, transformation Transformation) Field {
	return Field{
		Name: name, SchemaVersion: CapsuleVersion, Value: value,
		Provenance: provenance, Sensitivity: sensitivity, Freshness: "request-time",
		Transformation: transformation,
	}
}

func normalizeOS(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = runtime.GOOS
	}
	switch value {
	case "darwin", "linux", "windows", "freebsd", "openbsd", "netbsd", "dragonfly", "solaris", "aix", "plan9":
		return value
	default:
		return "unknown"
	}
}

func shellProvenance(c Context) string {
	switch c.ShellProvenance {
	case "widget-declared-shell", "SHELL":
		return c.ShellProvenance
	default:
		return "provider-request"
	}
}

func normalizeShell(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	base := strings.ToLower(filepath.Base(value))
	base = strings.TrimSuffix(base, ".exe")
	switch base {
	case "fish", "bash", "zsh":
		return base
	default:
		return "unknown"
	}
}

func projectKind(c Context) string {
	if c.GitRepository {
		return "git"
	}
	if c.WorkingDirectory != "" {
		return "non-git"
	}
	return "unknown"
}
