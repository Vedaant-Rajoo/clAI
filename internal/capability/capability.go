// Package capability collects a bounded, immutable inventory of local machine
// capabilities without invoking a shell.
package capability

import (
	"math/big"
	"regexp"
	"strings"
)

const InventoryVersion = "capability-inventory/v1"

type ShellFamily string

const (
	ShellFish    ShellFamily = "fish"
	ShellBash    ShellFamily = "bash"
	ShellZsh     ShellFamily = "zsh"
	ShellUnknown ShellFamily = "unknown"
)

type ShellProvenance string

const (
	ShellFromWidget ShellProvenance = "widget-declared-shell"
	ShellFromEnv    ShellProvenance = "SHELL"
)

type ShellIdentity struct {
	Family     ShellFamily
	Path       string
	Provenance ShellProvenance
}

// Version is a validated numeric dotted version. Its zero value is unknown.
type Version string

var numericDottedVersion = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*$`)

func ParseVersion(value string) (Version, bool) {
	if len(value) == 0 || len(value) > 32 || !numericDottedVersion.MatchString(value) {
		return "", false
	}
	return Version(value), true
}

func (v Version) String() string { return string(v) }

func (v Version) Known() bool {
	_, ok := ParseVersion(string(v))
	return ok
}

// Compare compares numeric components, treating missing trailing components as
// zero. It returns -1, 0, or 1. An unknown version sorts before a known one.
func (v Version) Compare(other Version) int {
	vKnown := v.Known()
	otherKnown := other.Known()
	if !vKnown || !otherKnown {
		switch {
		case vKnown:
			return 1
		case otherKnown:
			return -1
		default:
			return 0
		}
	}

	left := strings.Split(string(v), ".")
	right := strings.Split(string(other), ".")
	count := max(len(left), len(right))
	for i := range count {
		var leftPart, rightPart string
		if i < len(left) {
			leftPart = left[i]
		}
		if i < len(right) {
			rightPart = right[i]
		}

		var leftInt, rightInt big.Int
		leftInt.SetString(zeroIfEmpty(leftPart), 10)
		rightInt.SetString(zeroIfEmpty(rightPart), 10)
		if comparison := leftInt.Cmp(&rightInt); comparison != 0 {
			return comparison
		}
	}
	return 0
}

func zeroIfEmpty(value string) string {
	if value == "" {
		return "0"
	}
	return value
}

type ToolFact struct {
	Name    string
	Present bool
	Path    string
	Version Version
}

type Inventory struct {
	version  string
	osFamily string
	arch     string
	shell    ShellIdentity
	tools    []ToolFact
}

// NewFixtureInventory builds an Inventory from explicit facts. It is a test
// seam for deterministic fixtures: production collection must go through
// Collect or Cached so normalization, probe bounds, and once-per-process reuse
// apply. The tool slice is copied so callers cannot mutate the inventory.
func NewFixtureInventory(osFamily, arch string, shell ShellIdentity, tools []ToolFact) Inventory {
	return Inventory{
		version:  InventoryVersion,
		osFamily: osFamily,
		arch:     arch,
		shell:    shell,
		tools:    append([]ToolFact(nil), tools...),
	}
}

func (i Inventory) Version() string { return i.version }

func (i Inventory) OSFamily() string { return i.osFamily }

func (i Inventory) Arch() string { return i.arch }

func (i Inventory) Shell() ShellIdentity { return i.shell }

func (i Inventory) Tools() []ToolFact {
	return append([]ToolFact(nil), i.tools...)
}

func (i Inventory) LookupTool(name string) (ToolFact, bool) {
	for _, tool := range i.tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return ToolFact{}, false
}

type RequirementKind string

const (
	RequirementTool  RequirementKind = "tool"
	RequirementShell RequirementKind = "shell"
	RequirementOS    RequirementKind = "os"
)

type Requirement struct {
	Kind       RequirementKind
	Name       string
	MinVersion Version
}

func (kind RequirementKind) Valid() bool {
	switch kind {
	case RequirementTool, RequirementShell, RequirementOS:
		return true
	default:
		return false
	}
}
