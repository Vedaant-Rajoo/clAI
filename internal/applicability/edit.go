package applicability

import (
	"fmt"

	"github.com/Vedaant-Rajoo/clai/internal/shellsyntax"
)

// EvaluateEdited derives presence-only applicability assertions from the exact
// edited command bytes. Parser uncertainty makes no applicability assertion;
// structural validation remains authoritative for eligibility.
func EvaluateEdited(command string, inventory Inventory) Result {
	parsed := shellsyntax.Parse(command)
	if !parsed.Supported() {
		return Result{Decision: Applicable}
	}

	seen := make(map[string]bool)
	var tools []string
	var missing []string
	for _, pipeline := range parsed.List.Pipelines {
		for _, simple := range pipeline.Commands {
			resolved := shellsyntax.ResolveExecutable(simple)
			if !resolved.Found || resolved.Base == "" || seen[resolved.Base] {
				continue
			}
			seen[resolved.Base] = true
			tools = append(tools, resolved.Base)
			fact, known := inventory.LookupTool(resolved.Base)
			if known && !fact.Present {
				missing = append(missing, resolved.Base)
			}
		}
	}

	if len(missing) == 0 {
		return Result{Decision: Applicable, Tools: tools}
	}
	reasons := make([]string, len(missing))
	for index, name := range missing {
		reasons[index] = fmt.Sprintf("tool %s: may not work (not installed)", name)
	}
	return Result{Decision: Marked, Reasons: reasons, Tools: tools}
}
