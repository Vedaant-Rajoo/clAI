package rules

import (
	"context"

	"codeberg.org/newedia/clai/internal/compiler"
	"codeberg.org/newedia/clai/internal/provider"
)

type Provider struct{}

func (Provider) Compile(ctx context.Context, request provider.Request) ([]provider.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	result := compiler.Compile(compiler.Request{
		Intent:  request.Intent,
		Context: request.Context,
	})

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if result.Command == "" {
		return nil, nil
	}

	return []provider.Candidate{{
		Command:     result.Command,
		Explanation: result.Explanation,
	}}, nil
}
