package provider

import machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"

// RequestReceipt is the provider-neutral, in-memory record of exactly what a
// provider was about to transmit for one compile: the effective endpoint and its
// classification, the context policy and the fields it selected, redacted, and
// omitted, and the exact request body bytes together with their hash.
//
// It exists so a provider can prove, without a live network, precisely which
// bytes and which context fields would leave the machine. It carries no
// credentials, authorization values, or headers by construction. The type is
// provider-neutral so both the OpenRouter and direct Anthropic providers emit the
// same receipt shape.
type RequestReceipt struct {
	Version                string
	Provider               string
	Model                  string
	EffectiveEndpoint      string
	EndpointClassification machinecontext.EndpointClass
	ProxyMode              string
	ContextPolicy          machinecontext.Policy
	SelectorVersion        string
	SelectedFields         []machinecontext.Field
	RedactedFields         []machinecontext.Field
	OmittedFields          []machinecontext.Field
	RequestBody            []byte
	RequestBodyHash        BodyHash
}

// BodyHash is a named digest of a request body. Algorithm is the hash name (for
// example "sha256") and Value is its lowercase hex encoding.
type BodyHash struct {
	Algorithm string
	Value     string
}
