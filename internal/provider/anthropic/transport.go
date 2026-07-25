package anthropic

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"

	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
)

// receiptTransport wraps a base RoundTripper. On the single request per compile
// it captures the exact final SDK-produced body bytes, computes the request
// receipt and its SHA-256 over those bytes, emits the receipt once immediately
// before network transport, then forwards the byte-identical body to the base
// transport. It reads only the body — headers and credentials are never
// observed, so the receipt carries none by construction.
type receiptTransport struct {
	base      http.RoundTripper
	model     string
	class     machinecontext.EndpointClass
	proxyMode string
	selection machinecontext.Selection
	sink      func(provider.RequestReceipt)
}

// RoundTrip captures and restores the request body, emits the receipt, and
// forwards to the base transport. If the body cannot be captured or bounded it
// fails closed: no receipt is emitted and no network request is made.
func (t *receiptTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := captureBody(req)
	if err != nil {
		return nil, err
	}
	receipt := makeReceipt(t.model, req.URL.String(), t.class, t.proxyMode, t.selection, body)
	if t.sink != nil {
		t.sink(receipt)
	}
	return t.base.RoundTrip(req)
}

// captureBody reads the request body up to a hard bound, restores an identical
// reader (and GetBody) so the base transport transmits the exact same bytes,
// and returns the captured bytes. It returns an error — before any network
// activity — if the body exceeds the bound or cannot be read.
func captureBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	captured, err := io.ReadAll(io.LimitReader(req.Body, maxRequestBytes+1))
	closeErr := req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("anthropic: capture request body: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("anthropic: capture request body: %w", closeErr)
	}
	if len(captured) > maxRequestBytes {
		return nil, errors.New("anthropic: request body exceeds size limit")
	}
	req.Body = io.NopCloser(bytes.NewReader(captured))
	req.ContentLength = int64(len(captured))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(captured)), nil
	}
	return captured, nil
}

// makeReceipt builds the provider-neutral receipt over the exact body bytes.
// Its shape and hashing mirror the OpenRouter provider so both emit the same
// request-receipt/v1 record.
func makeReceipt(model, endpoint string, class machinecontext.EndpointClass, proxyMode string, selection machinecontext.Selection, body []byte) provider.RequestReceipt {
	sum := sha256.Sum256(body)
	receipt := provider.RequestReceipt{
		Version: "request-receipt/v1", Provider: "anthropic", Model: model,
		EffectiveEndpoint: endpoint, EndpointClassification: class, ProxyMode: proxyMode,
		ContextPolicy: selection.Policy, SelectorVersion: machinecontext.SelectorVersion,
		RequestBody:     append([]byte(nil), body...),
		RequestBodyHash: provider.BodyHash{Algorithm: "sha256", Value: hex.EncodeToString(sum[:])},
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
