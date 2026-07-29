package machinecontext

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/Vedaant-Rajoo/clai/internal/capability"
)

const (
	CapsuleVersion  = "context-capsule/v2"
	SelectorVersion = "context-selector/v2"
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

const TransformationNormalized Transformation = "normalized"

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
	FieldPlatformArch     = "platform_arch"
	FieldWorkingDirectory = "working_directory"
	FieldGitRoot          = "git_root"
	FieldGitBranch        = "git_branch"
)

var explicitFieldOrder = [...]string{
	FieldWorkingDirectory,
	FieldGitRoot,
	FieldGitBranch,
}

type Field struct {
	Name           string          `json:"name"`
	Value          string          `json:"value,omitempty"`
	SchemaVersion  string          `json:"schema_version"`
	Provenance     []string        `json:"provenance"`
	Sensitivity    string          `json:"sensitivity"`
	Freshness      string          `json:"freshness"`
	Transformation Transformation  `json:"transformation"`
	Sharing        SharingDecision `json:"sharing"`
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

// ProviderContext is the deterministic provider wire representation of selected
// context. Field order is contract-bearing because encoding/json preserves Go
// struct field order.
type ProviderContext struct {
	OSFamily         string         `json:"os_family"`
	ShellFamily      string         `json:"shell_family"`
	ProjectKind      string         `json:"project_kind"`
	PlatformArch     string         `json:"platform_arch"`
	Tools            []ProviderTool `json:"tools"`
	WorkingDirectory string         `json:"working_directory,omitempty"`
	GitRoot          string         `json:"git_root,omitempty"`
	GitBranch        string         `json:"git_branch,omitempty"`
}

type ProviderTool struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type ProviderPayload struct {
	Intent  string           `json:"intent"`
	Context *ProviderContext `json:"context,omitempty"`
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

func IsExplicitField(name string) bool {
	for _, candidate := range explicitFieldOrder {
		if name == candidate {
			return true
		}
	}
	return false
}

func NormalizeExplicitFields(fields []string) ([]string, error) {
	selected := make(map[string]bool, len(fields))
	for _, name := range fields {
		if !IsExplicitField(name) {
			return nil, fmt.Errorf("invalid shared context field %q (want working_directory, git_root, or git_branch)", name)
		}
		selected[name] = true
	}
	result := make([]string, 0, len(selected))
	for _, name := range explicitFieldOrder {
		if selected[name] {
			result = append(result, name)
		}
	}
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

// Select applies context-selector/v2 to one collected machine context and the
// immutable capability inventory for this process invocation.
func Select(c Context, inventory capability.Inventory, policy Policy, shared []string) (Selection, error) {
	if _, err := ParsePolicy(string(policy)); err != nil {
		return Selection{}, err
	}
	shared, err := NormalizeExplicitFields(shared)
	if err != nil {
		return Selection{}, err
	}
	if policy == PolicyRemoteExplicit && len(shared) == 0 {
		return Selection{}, errors.New("remote-explicit requires at least one shared context field")
	}
	if policy != PolicyRemoteExplicit && len(shared) != 0 {
		return Selection{}, errors.New("shared context fields require remote-explicit")
	}

	selectedExplicit := make(map[string]bool, len(shared))
	for _, name := range shared {
		selectedExplicit[name] = true
	}

	fields := make([]Field, 0, 4+len(capability.ToolNames())+len(explicitFieldOrder))
	baselineSelected := policy != PolicyLocalOnly
	shell := inventory.Shell()
	shellFamily := string(shell.Family)
	if shellFamily == "" {
		shellFamily = string(capability.ShellUnknown)
	}
	shellProvenance := string(shell.Provenance)
	if shellProvenance == "" {
		shellProvenance = string(capability.ShellFromEnv)
	}
	osFamily := inventory.OSFamily()
	if osFamily == "" {
		osFamily = "unknown"
	}
	arch := inventory.Arch()
	if arch == "" {
		arch = "unknown"
	}
	fields = append(fields,
		newField(FieldOSFamily, osFamily, []string{"runtime.GOOS"}, baselineSelected, baselineReason(policy)),
		newField(FieldShellFamily, shellFamily, []string{shellProvenance}, baselineSelected, baselineReason(policy)),
		newField(FieldProjectKind, projectKind(c), []string{"filesystem"}, baselineSelected, baselineReason(policy)),
		newField(FieldPlatformArch, arch, []string{"runtime.GOARCH"}, baselineSelected, baselineReason(policy)),
	)

	for _, name := range capability.ToolNames() {
		fact, found := inventory.LookupTool(name)
		status := "absent"
		provenance := []string{"exec.LookPath:" + name}
		if found && fact.Present {
			status = "present"
			if fact.Version.Known() {
				status += ":" + fact.Version.String()
				provenance = append(provenance, "probe:"+name)
			}
		}
		fields = append(fields, newField("tool_"+name, status, provenance, baselineSelected, baselineReason(policy)))
	}

	explicitValues := map[string]struct {
		value      string
		provenance []string
	}{
		FieldWorkingDirectory: {c.WorkingDirectory, []string{"filesystem"}},
		FieldGitRoot:          {c.GitRoot, []string{"filesystem"}},
		FieldGitBranch:        {c.GitBranch, []string{"filesystem"}},
	}
	for _, name := range explicitFieldOrder {
		value := explicitValues[name]
		selected := policy == PolicyRemoteExplicit && selectedExplicit[name] && value.value != ""
		reason := "not selected by context policy"
		if policy == PolicyRemoteExplicit && selectedExplicit[name] {
			if value.value == "" {
				reason = "selected field unavailable"
			} else {
				reason = "approved by invocation-scoped remote-explicit allowlist"
			}
		}
		fields = append(fields, newField(name, value.value, value.provenance, selected, reason))
	}

	return Selection{Policy: policy, Capsule: Capsule{Version: CapsuleVersion, Fields: fields}}, nil
}

func newField(name, value string, provenance []string, selected bool, reason string) Field {
	sharing := SharingOmitted
	if selected {
		sharing = SharingSelected
	} else {
		value = ""
	}
	return Field{
		Name:           name,
		Value:          value,
		SchemaVersion:  CapsuleVersion,
		Provenance:     append([]string(nil), provenance...),
		Sensitivity:    "low",
		Freshness:      "invocation",
		Transformation: TransformationNormalized,
		Sharing:        sharing,
		Reason:         reason,
	}
}

func baselineReason(policy Policy) string {
	switch policy {
	case PolicyLocalOnly:
		return "local-only policy omits provider context"
	case PolicyRemoteExplicit:
		return "selected by remote-explicit baseline"
	default:
		return "selected by remote-minimal baseline"
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

// ProviderPayloadFor converts a selection into the shared deterministic prompt
// payload used by remote providers. local-only leaves Context nil so the JSON
// member is omitted entirely.
func ProviderPayloadFor(intent string, selection Selection) ProviderPayload {
	payload := ProviderPayload{Intent: intent}
	if selection.Policy == PolicyLocalOnly {
		return payload
	}

	wire := &ProviderContext{Tools: make([]ProviderTool, 0, len(capability.ToolNames()))}
	for _, field := range selection.Capsule.Fields {
		if field.Sharing != SharingSelected {
			continue
		}
		switch field.Name {
		case FieldOSFamily:
			wire.OSFamily = field.Value
		case FieldShellFamily:
			wire.ShellFamily = field.Value
		case FieldProjectKind:
			wire.ProjectKind = field.Value
		case FieldPlatformArch:
			wire.PlatformArch = field.Value
		case FieldWorkingDirectory:
			wire.WorkingDirectory = field.Value
		case FieldGitRoot:
			wire.GitRoot = field.Value
		case FieldGitBranch:
			wire.GitBranch = field.Value
		default:
			if name, ok := strings.CutPrefix(field.Name, "tool_"); ok {
				wire.Tools = append(wire.Tools, ProviderTool{Name: name, Status: field.Value})
			}
		}
	}
	payload.Context = wire
	return payload
}
