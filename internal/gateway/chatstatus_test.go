package gateway

import (
	"net/http"
	"testing"
)

func TestChatStatusReportsExperienceChecks(t *testing.T) {
	// Production wires RBAC after constructing the gateway. Keep it active here
	// so this test catches drift between the route's chat:read requirement and
	// the static role policy.
	s := newTestGatewayWithRBAC(t)

	status, resp := gatewayJSON(t, s, http.MethodGet, "/api/v1/chat/status", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, resp)
	}
	if _, ok := resp["score"].(float64); !ok {
		t.Fatalf("missing score in response: %v", resp)
	}
	checks, ok := resp["checks"].([]any)
	if !ok || len(checks) == 0 {
		t.Fatalf("checks = %v, want non-empty list", resp["checks"])
	}
	foundAgents := false
	for _, raw := range checks {
		m, _ := raw.(map[string]any)
		if m["key"] == "agents" {
			foundAgents = true
			break
		}
	}
	if !foundAgents {
		t.Fatalf("agents check missing from %v", checks)
	}
}

func TestReadinessIncludesChatExperience(t *testing.T) {
	s := newTestGateway(t, "secret")

	status, resp := gatewayJSON(t, s, http.MethodGet, "/api/v1/readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, resp)
	}
	chat, ok := resp["chat"].(map[string]any)
	if !ok {
		t.Fatalf("chat readiness missing from response: %v", resp)
	}
	if _, ok := chat["checks"].([]any); !ok {
		t.Fatalf("chat checks missing: %v", chat)
	}
	parity, _ := resp["parity"].(map[string]any)
	areas, _ := parity["areas"].([]any)
	found := false
	for _, raw := range areas {
		area, _ := raw.(map[string]any)
		if area["key"] == "chat" {
			found = true
			if _, ok := area["score"].(float64); !ok {
				t.Fatalf("chat parity score missing: %v", area)
			}
			break
		}
	}
	if !found {
		t.Fatalf("chat parity area missing: %v", areas)
	}
}
