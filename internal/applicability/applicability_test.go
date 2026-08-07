package applicability

import (
	"reflect"
	"testing"

	"github.com/Vedaant-Rajoo/clai/internal/capability"
)

type fixtureInventory struct {
	os    string
	shell capability.ShellFamily
	tools map[string]capability.ToolFact
}

func (inventory fixtureInventory) OSFamily() string { return inventory.os }
func (inventory fixtureInventory) Shell() capability.ShellIdentity {
	return capability.ShellIdentity{Family: inventory.shell}
}
func (inventory fixtureInventory) LookupTool(name string) (capability.ToolFact, bool) {
	fact, ok := inventory.tools[name]
	return fact, ok
}

func requirement(kind capability.RequirementKind, name, minimum string) capability.Requirement {
	version, _ := capability.ParseVersion(minimum)
	return capability.Requirement{Kind: kind, Name: name, MinVersion: version}
}

func TestApplicabilityTruthTable(t *testing.T) {
	inventory := fixtureInventory{
		os:    "darwin",
		shell: capability.ShellFish,
		tools: map[string]capability.ToolFact{
			"rg":   {Name: "rg", Present: true, Version: "14.1.0"},
			"git":  {Name: "git", Present: true},
			"grep": {Name: "grep", Present: false},
		},
	}
	tests := []struct {
		name         string
		requirements []capability.Requirement
		want         Decision
		wantReasons  int
	}{
		{name: "empty", want: Applicable},
		{name: "same kind OR", requirements: []capability.Requirement{
			requirement(capability.RequirementTool, "grep", ""),
			requirement(capability.RequirementTool, "rg", ""),
		}, want: Applicable},
		{name: "different kind AND hard reject", requirements: []capability.Requirement{
			requirement(capability.RequirementOS, "linux", ""),
			requirement(capability.RequirementTool, "rg", ""),
		}, want: Rejected, wantReasons: 1},
		{name: "shell mismatch", requirements: []capability.Requirement{
			requirement(capability.RequirementShell, "bash", ""),
		}, want: Rejected, wantReasons: 1},
		{name: "missing tool", requirements: []capability.Requirement{
			requirement(capability.RequirementTool, "grep", ""),
		}, want: Marked, wantReasons: 1},
		{name: "outside inventory", requirements: []capability.Requirement{
			requirement(capability.RequirementTool, "python", ""),
		}, want: Applicable},
		{name: "unknown version", requirements: []capability.Requirement{
			requirement(capability.RequirementTool, "git", "2.40"),
		}, want: Marked, wantReasons: 1},
		{name: "version below minimum", requirements: []capability.Requirement{
			requirement(capability.RequirementTool, "rg", "15"),
		}, want: Marked, wantReasons: 1},
		{name: "numeric version satisfied", requirements: []capability.Requirement{
			requirement(capability.RequirementTool, "rg", "14.1"),
		}, want: Applicable},
		{name: "shell same kind OR", requirements: []capability.Requirement{
			requirement(capability.RequirementShell, "bash", ""),
			requirement(capability.RequirementShell, "fish", ""),
		}, want: Applicable},
		{name: "os same kind OR", requirements: []capability.Requirement{
			requirement(capability.RequirementOS, "linux", ""),
			requirement(capability.RequirementOS, "darwin", ""),
		}, want: Applicable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Evaluate(test.requirements, inventory)
			if got.Decision != test.want || len(got.Reasons) != test.wantReasons {
				t.Fatalf("Evaluate() = %+v, want decision %q and %d reasons", got, test.want, test.wantReasons)
			}
		})
	}

	requirements := []capability.Requirement{
		requirement(capability.RequirementTool, "grep", ""),
		requirement(capability.RequirementTool, "python", ""),
		requirement(capability.RequirementOS, "linux", ""),
	}
	first := Evaluate(requirements, inventory)
	second := Evaluate(requirements, inventory)
	if !reflect.DeepEqual(first.Reasons, second.Reasons) {
		t.Fatalf("reason order is not deterministic: %v then %v", first.Reasons, second.Reasons)
	}
}

func TestUnknownMachineValueHardRejects(t *testing.T) {
	unknownInventory := fixtureInventory{os: "unknown", shell: capability.ShellUnknown, tools: map[string]capability.ToolFact{}}
	tests := []struct {
		name         string
		requirements []capability.Requirement
	}{
		{name: "shell unknown requirement vs unknown inventory", requirements: []capability.Requirement{
			requirement(capability.RequirementShell, "unknown", ""),
		}},
		{name: "os unknown requirement vs unknown inventory", requirements: []capability.Requirement{
			requirement(capability.RequirementOS, "unknown", ""),
		}},
		{name: "shell unknown among alternatives", requirements: []capability.Requirement{
			requirement(capability.RequirementShell, "fish", ""),
			requirement(capability.RequirementShell, "unknown", ""),
		}},
		{name: "os unknown among alternatives", requirements: []capability.Requirement{
			requirement(capability.RequirementOS, "darwin", ""),
			requirement(capability.RequirementOS, "unknown", ""),
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Evaluate(test.requirements, unknownInventory)
			if got.Decision != Rejected || len(got.Reasons) != len(test.requirements) {
				t.Fatalf("Evaluate() = %+v, want hard rejection with %d reasons", got, len(test.requirements))
			}
		})
	}

	// Known machine values still match normally alongside the unknown guard.
	known := fixtureInventory{os: "darwin", shell: capability.ShellZsh, tools: map[string]capability.ToolFact{}}
	if got := Evaluate([]capability.Requirement{requirement(capability.RequirementShell, "unknown", "")}, known); got.Decision != Rejected {
		t.Fatalf("shell unknown vs zsh inventory = %+v, want hard rejection", got)
	}
	if got := Evaluate([]capability.Requirement{requirement(capability.RequirementOS, "unknown", "")}, known); got.Decision != Rejected {
		t.Fatalf("os unknown vs darwin inventory = %+v, want hard rejection", got)
	}
}

func TestEditReviewDerivesExecutablesFromBytes(t *testing.T) {
	inventory := fixtureInventory{tools: map[string]capability.ToolFact{
		"rg":      {Name: "rg", Present: true},
		"missing": {Name: "missing", Present: false},
	}}
	result := EvaluateEdited("env FOO=bar /usr/bin/rg TODO | command missing --flag", inventory)
	if result.Decision != Marked {
		t.Fatalf("decision = %q, want marked", result.Decision)
	}
	want := []string{"tool missing: may not work (not installed)"}
	if !reflect.DeepEqual(result.Reasons, want) {
		t.Fatalf("reasons = %v, want %v", result.Reasons, want)
	}
}

// TestEditedBraceCommandsDeriveExecutables covers the consequence of literal
// {} joining the supported subset: a braced command now parses, so edit
// evaluation derives its executables instead of skipping on parse uncertainty.
// The marks stay soft, so such a command remains exportable after acceptance.
func TestEditedBraceCommandsDeriveExecutables(t *testing.T) {
	inventory := fixtureInventory{tools: map[string]capability.ToolFact{
		"du":    {Name: "du", Present: true},
		"xargs": {Name: "xargs", Present: false},
	}}

	result := EvaluateEdited("xargs -I{} du -h {}", inventory)
	if result.Decision != Marked {
		t.Fatalf("decision = %q, want a soft mark for the absent xargs", result.Decision)
	}
	want := []string{"tool xargs: may not work (not installed)"}
	if !reflect.DeepEqual(result.Reasons, want) {
		t.Fatalf("reasons = %v, want %v", result.Reasons, want)
	}

	// Every derived executable present: applicable with no reasons.
	present := fixtureInventory{tools: map[string]capability.ToolFact{
		"find": {Name: "find", Present: true},
		"du":   {Name: "du", Present: true},
	}}
	if got := EvaluateEdited(`find . -exec du -h {} \;`, present); got.Decision != Applicable || len(got.Reasons) != 0 {
		t.Fatalf("EvaluateEdited(find -exec) = %+v, want applicable with no reasons", got)
	}

	// A brace pair in command position is still parse uncertainty, so no
	// applicability assertion is made and structural validation decides.
	if got := EvaluateEdited("{}/echo", inventory); got.Decision != Applicable || len(got.Reasons) != 0 {
		t.Fatalf("EvaluateEdited({}/echo) = %+v, want no applicability assertion", got)
	}
}

func TestUnprobedToolsDoNotProduceApplicabilityWarnings(t *testing.T) {
	inventory := fixtureInventory{tools: map[string]capability.ToolFact{}}

	modelResult := Evaluate([]capability.Requirement{
		requirement(capability.RequirementTool, "ls", ""),
	}, inventory)
	if modelResult.Decision != Applicable || len(modelResult.Reasons) != 0 {
		t.Fatalf("Evaluate(unprobed ls) = %+v, want applicable without a false warning", modelResult)
	}

	editedResult := EvaluateEdited("ls -la", inventory)
	if editedResult.Decision != Applicable || len(editedResult.Reasons) != 0 {
		t.Fatalf("EvaluateEdited(unprobed ls) = %+v, want applicable without a false warning", editedResult)
	}
	if !reflect.DeepEqual(editedResult.Tools, []string{"ls"}) {
		t.Fatalf("EvaluateEdited(unprobed ls).Tools = %v, want the unprobed fact retained for rendering", editedResult.Tools)
	}
}

func TestEditedParseUncertaintyDefersToValidation(t *testing.T) {
	inventory := fixtureInventory{tools: map[string]capability.ToolFact{}}
	for _, command := range []string{"echo $(missing)", "echo 'unterminated"} {
		if result := EvaluateEdited(command, inventory); result.Decision != Applicable || len(result.Reasons) != 0 {
			t.Fatalf("EvaluateEdited(%q) = %+v, want no applicability assertion", command, result)
		}
	}
}
