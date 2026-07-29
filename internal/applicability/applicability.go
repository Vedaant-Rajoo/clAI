// Package applicability evaluates candidate requirements against a bounded
// machine capability inventory. It is independent from validation and safety.
package applicability

import (
	"fmt"

	"github.com/Vedaant-Rajoo/clai/internal/capability"
)

// Decision is the result of the independent applicability gate.
type Decision string

const (
	Applicable Decision = "applicable"
	Marked     Decision = "soft-marked"
	Rejected   Decision = "hard-rejected"
)

// Result records the gate decision and deterministic, render-time-sanitized
// display reasons. Reasons never contain executable paths or probe output.
type Result struct {
	Decision Decision
	Reasons  []string
}

// Inventory is the read-only capability surface needed by the gate.
// capability.Inventory satisfies this interface.
type Inventory interface {
	OSFamily() string
	Shell() capability.ShellIdentity
	LookupTool(string) (capability.ToolFact, bool)
}

// Evaluate applies same-kind OR and different-kind AND to requirements.
func Evaluate(requirements []capability.Requirement, inventory Inventory) Result {
	if len(requirements) == 0 {
		return Result{Decision: Applicable}
	}

	groups := make(map[capability.RequirementKind][]capability.Requirement, 3)
	order := make([]capability.RequirementKind, 0, 3)
	for _, requirement := range requirements {
		if _, seen := groups[requirement.Kind]; !seen {
			order = append(order, requirement.Kind)
		}
		groups[requirement.Kind] = append(groups[requirement.Kind], requirement)
	}

	decision := Applicable
	var reasons []string
	for _, kind := range order {
		matched, kindReasons := evaluateKind(kind, groups[kind], inventory)
		if matched {
			continue
		}
		reasons = append(reasons, kindReasons...)
		if kind == capability.RequirementShell || kind == capability.RequirementOS {
			decision = Rejected
		} else if decision != Rejected {
			decision = Marked
		}
	}
	return Result{Decision: decision, Reasons: reasons}
}

func evaluateKind(kind capability.RequirementKind, requirements []capability.Requirement, inventory Inventory) (bool, []string) {
	switch kind {
	case capability.RequirementTool:
		return evaluateTools(requirements, inventory)
	case capability.RequirementShell:
		// A normalized machine value of unknown is always a hard rejection
		// (REQ-APPLICABILITY-012), even for a requirement literally named
		// "unknown"; only fish, bash, or zsh can satisfy a shell requirement.
		actual := string(inventory.Shell().Family)
		if actual != string(capability.ShellUnknown) {
			for _, requirement := range requirements {
				if requirement.Name == actual {
					return true, nil
				}
			}
		}
		reasons := make([]string, 0, len(requirements))
		for _, requirement := range requirements {
			reasons = append(reasons, fmt.Sprintf("shell %s: not for this shell/OS (active shell is %s)", requirement.Name, actual))
		}
		return false, reasons
	case capability.RequirementOS:
		actual := inventory.OSFamily()
		if actual != "unknown" {
			for _, requirement := range requirements {
				if requirement.Name == actual {
					return true, nil
				}
			}
		}
		reasons := make([]string, 0, len(requirements))
		for _, requirement := range requirements {
			reasons = append(reasons, fmt.Sprintf("os %s: not for this shell/OS (active OS is %s)", requirement.Name, actual))
		}
		return false, reasons
	default:
		return false, []string{fmt.Sprintf("%s requirement: may not work", kind)}
	}
}

func evaluateTools(requirements []capability.Requirement, inventory Inventory) (bool, []string) {
	for _, requirement := range requirements {
		fact, known := inventory.LookupTool(requirement.Name)
		if !known || !fact.Present {
			continue
		}
		if !requirement.MinVersion.Known() || fact.Version.Known() && fact.Version.Compare(requirement.MinVersion) >= 0 {
			return true, nil
		}
	}

	reasons := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		fact, known := inventory.LookupTool(requirement.Name)
		switch {
		case !known:
			reasons = append(reasons, fmt.Sprintf("tool %s: may not work (outside capability inventory)", requirement.Name))
		case !fact.Present:
			reasons = append(reasons, fmt.Sprintf("tool %s: may not work (not installed)", requirement.Name))
		case requirement.MinVersion.Known() && !fact.Version.Known():
			reasons = append(reasons, fmt.Sprintf("tool %s: may not work (version unknown; need at least %s)", requirement.Name, requirement.MinVersion))
		case requirement.MinVersion.Known() && fact.Version.Compare(requirement.MinVersion) < 0:
			reasons = append(reasons, fmt.Sprintf("tool %s: may not work (version %s is below %s)", requirement.Name, fact.Version, requirement.MinVersion))
		default:
			reasons = append(reasons, fmt.Sprintf("tool %s: may not work", requirement.Name))
		}
	}
	return false, reasons
}
