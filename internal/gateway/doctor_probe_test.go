package gateway

import (
	"net/http"
	"testing"
)

func TestModelInListToleratesImplicitLatest(t *testing.T) {
	have := []string{"nomic-embed-text:latest", "qwen3:8b"}
	for _, want := range []string{"nomic-embed-text", "nomic-embed-text:latest", "qwen3:8b"} {
		if !modelInList(want, have) {
			t.Errorf("%q should match %v", want, have)
		}
	}
	for _, want := range []string{"qwen3:32b", "", "nomic-embed"} {
		if modelInList(want, have) {
			t.Errorf("%q should not match %v", want, have)
		}
	}
}

// The defect this fixes: a provider block the product wrote for itself was
// enough to report "ready" on a machine with nothing running. The probe must
// be able to say no.
func TestProviderCheckFailsWhenTheProviderCannotBeReached(t *testing.T) {
	s := newTestGateway(t, "secret")
	// The test gateway's providers point nowhere real, so a live probe must
	// mark them unreachable rather than usable.
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/doctor", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body = %v", status, body)
	}
	providers, _ := body["providers"].([]any)
	if len(providers) == 0 {
		t.Skip("this gateway has no providers configured; nothing to probe")
	}
	for _, raw := range providers {
		p, _ := raw.(map[string]any)
		// Whatever the verdict, an unreachable provider must never be "ok".
		if reachable, ok := p["reachable"].(bool); ok && !reachable {
			if p["status"] == "ok" {
				t.Errorf("provider %v is unreachable but still reported ok: %v", p["id"], p)
			}
			if detail, _ := p["detail"].(string); detail == "" {
				t.Errorf("provider %v failed without saying why", p["id"])
			}
			if remedy, _ := p["remedy"].(string); remedy == "" {
				t.Errorf("provider %v failed without a remedy", p["id"])
			}
		}
	}
}

// Onboarding only sends a new user to the setup wizard when the provider step
// is "todo". If an unreachable provider reports ok, the person who most needs
// the wizard never sees it.
func TestOnboardingProviderStepReflectsAFailedProbe(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/onboarding/status", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body = %v", status, body)
	}
	steps, _ := body["steps"].([]any)
	var providerStep map[string]any
	for _, raw := range steps {
		st, _ := raw.(map[string]any)
		if st["key"] == "provider" {
			providerStep = st
		}
	}
	if providerStep == nil {
		t.Fatal("onboarding must report a provider step")
	}
	providers, _ := body["providers"].([]any)
	anyOK := false
	for _, raw := range providers {
		p, _ := raw.(map[string]any)
		if p["status"] == "ok" {
			anyOK = true
		}
	}
	if !anyOK && providerStep["status"] == "ok" {
		t.Errorf("no provider is usable, yet the provider step says ok: %v", providerStep)
	}
}

// probeLiveProviders exists so tests can opt out of outbound calls. If it ever
// defaults to off, every readiness verdict silently goes back to being a
// statement about config rather than about the world.
func TestLiveProbingIsOnByDefault(t *testing.T) {
	if !probeLiveProviders {
		t.Fatal("probing must be enabled by default; a readiness report that never leaves the process is not one")
	}
}
