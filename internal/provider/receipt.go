package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
)

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

// NewReceipt builds a request-receipt/v1 record over the exact body bytes for
// the named provider. Both remote providers share it so the receipt shape and
// hashing cannot drift between them.
func NewReceipt(providerName, model, endpoint string, class machinecontext.EndpointClass, proxyMode string, selection machinecontext.Selection, body []byte) RequestReceipt {
	sum := sha256.Sum256(body)
	receipt := RequestReceipt{
		Version: "request-receipt/v1", Provider: providerName, Model: model,
		EffectiveEndpoint: endpoint, EndpointClassification: class, ProxyMode: proxyMode,
		ContextPolicy: selection.Policy, SelectorVersion: machinecontext.SelectorVersion,
		RequestBody:     append([]byte(nil), body...),
		RequestBodyHash: BodyHash{Algorithm: "sha256", Value: hex.EncodeToString(sum[:])},
	}
	for _, field := range selection.Capsule.Fields {
		switch field.Sharing {
		case machinecontext.SharingSelected:
			receipt.SelectedFields = append(receipt.SelectedFields, field)
		case machinecontext.SharingRedacted:
			receipt.RedactedFields = append(receipt.RedactedFields, field)
		case machinecontext.SharingOmitted:
			receipt.OmittedFields = append(receipt.OmittedFields, field)
		}
	}
	return receipt
}

// ProxyMode reports "direct", "configured-proxy", or "unknown" for a transport
// and request. It never reveals a proxy URL or credentials. A nil transport is
// classified as the default transport.
func ProxyMode(transport http.RoundTripper, request *http.Request) string {
	if transport == nil {
		transport = http.DefaultTransport
	}
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		return "unknown"
	}
	if httpTransport.Proxy == nil {
		return "direct"
	}
	proxyURL, err := httpTransport.Proxy(request)
	if err != nil {
		return "unknown"
	}
	if proxyURL == nil {
		return "direct"
	}
	return "configured-proxy"
}
