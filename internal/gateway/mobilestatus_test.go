package gateway

import (
	"net/http"
	"sync"
	"testing"
)

func TestMobileStatusReportsCompanionReadiness(t *testing.T) {
	t.Setenv("SOULACY_WORKSPACE", t.TempDir())
	pushOnce = sync.Once{}
	pushSvc = nil
	pushErr = nil

	srv := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, srv, http.MethodGet, "/api/v1/mobile/status", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("mobile status = %d body=%v", status, body)
	}
	if _, ok := body["score"].(float64); !ok {
		t.Fatalf("expected score in response: %v", body)
	}
	checks, _ := body["checks"].([]any)
	if len(checks) == 0 {
		t.Fatalf("expected companion checks: %v", body)
	}
	if _, ok := body["push_subscriptions"].(float64); !ok {
		t.Fatalf("expected push subscription count: %v", body)
	}
	if _, ok := body["installable"].(bool); !ok {
		t.Fatalf("expected installable flag: %v", body)
	}
	if _, ok := body["durable_run_ledger"].(bool); !ok {
		t.Fatalf("expected durable run ledger flag: %v", body)
	}
	if _, ok := body["recent_runs"].(float64); !ok {
		t.Fatalf("expected recent run count: %v", body)
	}
	keys := map[string]bool{}
	for _, raw := range checks {
		check, _ := raw.(map[string]any)
		keys[check["key"].(string)] = true
	}
	for _, key := range []string{"pwa_install", "run_review"} {
		if !keys[key] {
			t.Fatalf("expected companion check %q in %v", key, checks)
		}
	}
}

func TestReadinessIncludesMobileCompanionParity(t *testing.T) {
	t.Setenv("SOULACY_WORKSPACE", t.TempDir())
	pushOnce = sync.Once{}
	pushSvc = nil
	pushErr = nil

	srv := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, srv, http.MethodGet, "/api/v1/readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("readiness = %d body=%v", status, body)
	}
	if _, ok := body["mobile"].(map[string]any); !ok {
		t.Fatalf("expected mobile readiness in payload: %v", body)
	}
	parity := body["parity"].(map[string]any)
	areas := parity["areas"].([]any)
	found := false
	for _, raw := range areas {
		area := raw.(map[string]any)
		if area["key"] == "mobile_companion" {
			found = true
			if _, ok := area["score"].(float64); !ok {
				t.Fatalf("mobile parity should include score: %v", area)
			}
		}
	}
	if !found {
		t.Fatalf("mobile companion parity area missing: %v", areas)
	}
}
