package capability

import (
	"reflect"
	"testing"
)

func TestVersionComparison(t *testing.T) {
	t.Parallel()

	tests := []struct {
		left  string
		right string
		want  int
	}{
		{left: "1.2", right: "1.2.0", want: 0},
		{left: "1.2.1", right: "1.2", want: 1},
		{left: "2.0", right: "10.0", want: -1},
		{left: "0002.000", right: "2", want: 0},
		{left: "99999999999999999999999999999999", right: "9", want: 1},
	}

	for _, test := range tests {
		t.Run(test.left+"_vs_"+test.right, func(t *testing.T) {
			left, leftOK := ParseVersion(test.left)
			right, rightOK := ParseVersion(test.right)
			if !leftOK || !rightOK {
				t.Fatalf("ParseVersion(%q, %q) = %v, %v", test.left, test.right, leftOK, rightOK)
			}
			if got := left.Compare(right); got != test.want {
				t.Fatalf("Version(%q).Compare(%q) = %d, want %d", test.left, test.right, got, test.want)
			}
		})
	}
}

func TestVersionValidationAndRequirementKinds(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "v1.2", "1.2-beta", "1.", ".1", "1..2", "123456789012345678901234567890123"} {
		if version, ok := ParseVersion(value); ok {
			t.Errorf("ParseVersion(%q) = %q, true; want unknown", value, version)
		}
	}
	for _, kind := range []RequirementKind{RequirementTool, RequirementShell, RequirementOS} {
		if !kind.Valid() {
			t.Errorf("RequirementKind(%q).Valid() = false", kind)
		}
	}
	if RequirementKind("other").Valid() {
		t.Fatal("unknown requirement kind is valid")
	}
}

func TestImmutableReturns(t *testing.T) {
	t.Parallel()

	names := ToolNames()
	names[0] = "changed"
	if got := ToolNames()[0]; got != "git" {
		t.Fatalf("ToolNames()[0] = %q after caller mutation, want git", got)
	}

	original := Inventory{
		version: InventoryVersion,
		tools: []ToolFact{
			{Name: "git", Present: true, Path: "/bin/git", Version: "2.42"},
			{Name: "rg"},
		},
	}
	first := original.Tools()
	first[0].Name = "changed"
	second := original.Tools()
	if !reflect.DeepEqual(second, []ToolFact{{Name: "git", Present: true, Path: "/bin/git", Version: "2.42"}, {Name: "rg"}}) {
		t.Fatalf("Tools() exposed mutable storage: %#v", second)
	}

	fact, ok := original.LookupTool("git")
	if !ok || fact.Name != "git" {
		t.Fatalf("LookupTool(git) = %#v, %v", fact, ok)
	}
	fact.Name = "changed"
	fact, _ = original.LookupTool("git")
	if fact.Name != "git" {
		t.Fatalf("LookupTool exposed mutable storage: %#v", fact)
	}
}
