package provider

import (
	"context"

	machinecontext "codeberg.org/newedia/clai/internal/context"
)

type Request struct {
	Intent  string
	Context machinecontext.Context
}

type Candidate struct {
	Command     string
	Explanation string
}

// Provider compiles a natural-language intent into shell command candidates.
//
// Semantics:
//   - An empty candidate slice with a nil error means "no suggestion"; the
//     caller is responsible for the fallback UX.
//   - The first candidate is the primary suggestion; additional candidates
//     are alternates that the current UI ignores.
//   - A non-nil error means compilation failed; no candidates are usable.
type Provider interface {
	Compile(context.Context, Request) ([]Candidate, error)
}
