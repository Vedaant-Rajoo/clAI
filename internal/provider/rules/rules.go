package rules

import (
	"codeberg.org/newedia/clai/internal/compiler"
	"codeberg.org/newedia/clai/internal/provider"
)

type Provider struct{}

func (Provider) Compile(request provider.Request) ([]provider.Candidate, error) {
	result := compiler.Compile(compiler.Request{
		Intent:  request.Intent,
		Context: request.Context,
	})

	return []provider.Candidate{
		{
			Command:     result.Command,
			Explanation: result.Explanation,
		},
	}, nil
}
