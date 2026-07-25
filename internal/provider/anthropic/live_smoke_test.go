package anthropic

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Vedaant-Rajoo/clai/internal/auth"
	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
)

// TestLiveAnthropicSmoke exercises the REAL Anthropic Messages API end to end
// through the exact provider construction the CLI uses (auth.Resolve ->
// anthropic.Provider{remote-minimal} -> streaming SDK -> api.anthropic.com). It
// is skipped unless CLAI_LIVE=1 so it never runs in CI or `make check`.
//
// Constraints honored: the resolved credential is never printed; fallback is not
// involved (the direct provider is constructed without the fallback wrapper); the
// stream is consumed transport-only and only one complete candidate is returned;
// and the generated command is never executed.
func TestLiveAnthropicSmoke(t *testing.T) {
	if os.Getenv("CLAI_LIVE") != "1" {
		t.Skip("live smoke test; set CLAI_LIVE=1 to run against the real Anthropic API")
	}

	key, err := auth.Resolve("anthropic", "")
	if err != nil {
		t.Fatalf("resolve anthropic credential: %v", err)
	}
	if key == "" {
		t.Fatal("no anthropic credential resolved")
	}

	// Mirror the app exactly: collect runtime context; the provider's
	// remote-minimal policy decides which fields actually leave the machine.
	collected := machinecontext.CollectWithShell("")

	var receipts []provider.RequestReceipt
	p := Provider{
		APIKey:       key,
		Model:        DefaultModel,
		Policy:       machinecontext.PolicyRemoteMinimal,
		SharedFields: nil,
		receiptSink:  func(r provider.RequestReceipt) { receipts = append(receipts, r) },
	}

	// The app's outer compile deadline is 120s; nest inside it.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	const intent = "list all files including hidden ones in long format"
	candidates, err := p.Compile(ctx, provider.Request{Intent: intent, Context: collected})
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected exactly one complete candidate, got %d", len(candidates))
	}
	c := candidates[0]
	if strings.TrimSpace(c.Command) == "" {
		t.Fatal("candidate command is empty")
	}
	t.Logf("LIVE candidate command:     %s", c.Command)
	t.Logf("LIVE candidate explanation: %s", c.Explanation)

	// Exactly one receipt, over the real endpoint, classified remote.
	if len(receipts) != 1 {
		t.Fatalf("expected exactly one receipt, got %d", len(receipts))
	}
	r := receipts[0]
	if want := defaultEndpoint + "/v1/messages"; r.EffectiveEndpoint != want {
		t.Fatalf("receipt endpoint = %q, want %q", r.EffectiveEndpoint, want)
	}
	if r.EndpointClassification != machinecontext.EndpointRemote {
		t.Fatalf("receipt classification = %v, want remote", r.EndpointClassification)
	}

	// Credential non-disclosure: the resolved key must never appear in the
	// captured request body, the hash input, or the candidate text.
	if strings.Contains(string(r.RequestBody), key) {
		t.Fatal("credential leaked into request body")
	}
	if strings.Contains(c.Command, key) || strings.Contains(c.Explanation, key) {
		t.Fatal("credential leaked into candidate text")
	}

	t.Logf("LIVE receipt: endpoint=%s class=%s policy=%s bodyHash=%s:%s bytes=%d selected=%d redacted=%d omitted=%d",
		r.EffectiveEndpoint, r.EndpointClassification, r.ContextPolicy,
		r.RequestBodyHash.Algorithm, r.RequestBodyHash.Value, len(r.RequestBody),
		len(r.SelectedFields), len(r.RedactedFields), len(r.OmittedFields))
}
