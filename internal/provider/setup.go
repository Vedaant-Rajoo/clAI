package provider

import (
	"errors"
	"fmt"

	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
)

// CompileSetup carries the per-provider inputs for ResolveCompileSetup. Name
// prefixes every error; KeyHint completes the no-API-key message with the
// provider's login command and environment variable.
type CompileSetup struct {
	Name            string
	TestEndpoint    string
	DevEndpoint     string
	DefaultEndpoint string
	Policy          machinecontext.Policy
	SharedFields    []string
	APIKey          string
	KeyHint         string
}

// ResolveCompileSetup runs the compile prologue shared by the remote
// providers: resolve the effective endpoint (test seam > loopback dev override
// > pinned default), classify it lexically, default the context policy on that
// classification, fail closed when a local-only policy meets a remote
// endpoint, select the context to share, and require an API key. Keeping this
// in one place means the privacy enforcement order cannot drift between
// providers.
func ResolveCompileSetup(setup CompileSetup, request Request) (string, machinecontext.EndpointClass, machinecontext.Selection, error) {
	endpoint := setup.TestEndpoint
	if endpoint == "" {
		endpoint = setup.DevEndpoint
	}
	if endpoint == "" {
		endpoint = setup.DefaultEndpoint
	}
	class, err := machinecontext.ClassifyEndpoint(endpoint)
	if err != nil {
		return "", class, machinecontext.Selection{}, fmt.Errorf("%s: %w", setup.Name, err)
	}
	policy := setup.Policy
	if policy == "" {
		policy = machinecontext.DefaultPolicy(false, class)
	}
	if policy == machinecontext.PolicyLocalOnly && class == machinecontext.EndpointRemote {
		return "", class, machinecontext.Selection{}, errors.New(setup.Name + ": local-only context policy prohibits a remote endpoint")
	}
	selection, err := machinecontext.Select(request.Context, request.Capabilities, policy, setup.SharedFields)
	if err != nil {
		return "", class, machinecontext.Selection{}, fmt.Errorf("%s: context policy: %w", setup.Name, err)
	}
	if setup.APIKey == "" {
		return "", class, machinecontext.Selection{}, errors.New(setup.Name + ": no API key (" + setup.KeyHint + ")")
	}
	return endpoint, class, selection, nil
}
