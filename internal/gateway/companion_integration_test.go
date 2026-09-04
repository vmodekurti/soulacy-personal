package gateway

import (
	"net/http"
	"testing"

	"github.com/soulacy/soulacy/internal/runtime"
)

// TestApprovalsEndpoints exercises the full approve-from-anywhere path: a
// pending approval registered on the broker is listed via GET /approvals and
// resolved via POST /approvals/:id/approve.
func TestApprovalsEndpoints(t *testing.T) {
	srv := newTestGateway(t, "secret")

	// Register a pending approval as the engine would during a run.
	ch := srv.engine.Broker().RegisterRequest(
		runtime.ConfirmRequest{CallID: "call-1", Tool: "shell_exec", Reason: "risky"},
		"agent-a", "sess-1",
	)

	status, body := gatewayJSON(t, srv, http.MethodGet, "/api/v1/approvals", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list status = %d", status)
	}
	list, _ := body["approvals"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected 1 pending approval, got %v", body["approvals"])
	}
	first := list[0].(map[string]any)
	if first["call_id"] != "call-1" || first["tool"] != "shell_exec" || first["agent_id"] != "agent-a" {
		t.Fatalf("approval metadata wrong: %v", first)
	}

	status, body = gatewayJSON(t, srv, http.MethodPost, "/api/v1/approvals/call-1/approve", "secret", "")
	if status != http.StatusOK || body["approved"] != true {
		t.Fatalf("approve failed: %d %v", status, body)
	}
	// The engine's blocked goroutine would receive true.
	if got := <-ch; got != true {
		t.Fatalf("expected approval delivered to broker channel")
	}

	// Now empty; a second resolve is a 404.
	_, body = gatewayJSON(t, srv, http.MethodGet, "/api/v1/approvals", "secret", "")
	if list, _ := body["approvals"].([]any); len(list) != 0 {
		t.Fatalf("approvals should be empty after resolve")
	}
	status, _ = gatewayJSON(t, srv, http.MethodPost, "/api/v1/approvals/call-1/deny", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("resolving an unknown call should 404, got %d", status)
	}
}

// TestApprovalsDenyDelivers verifies deny is delivered as false.
func TestApprovalsDenyDelivers(t *testing.T) {
	srv := newTestGateway(t, "secret")
	ch := srv.engine.Broker().RegisterRequest(runtime.ConfirmRequest{CallID: "c2", Tool: "write_file"}, "a", "s")
	status, _ := gatewayJSON(t, srv, http.MethodPost, "/api/v1/approvals/c2/deny", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("deny status = %d", status)
	}
	if got := <-ch; got != false {
		t.Fatalf("expected deny=false delivered")
	}
}

// TestPairingRoundTrip mints a pairing token and redeems it once.
func TestPairingRoundTrip(t *testing.T) {
	t.Setenv("SOULACY_WORKSPACE", t.TempDir())
	srv := newTestGateway(t, "secret")

	status, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/pairing/tokens", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("create token status = %d", status)
	}
	code, _ := body["code"].(string)
	if code == "" {
		t.Fatalf("no pairing code returned: %v", body)
	}
	if url, _ := body["pair_url"].(string); url == "" {
		t.Fatalf("expected a pair_url")
	}

	// Redeem succeeds once.
	status, body = gatewayJSON(t, srv, http.MethodPost, "/api/v1/pairing/redeem", "secret", `{"code":"`+code+`"}`)
	if status != http.StatusOK || body["paired"] != true {
		t.Fatalf("redeem failed: %d %v", status, body)
	}
	// Second redeem is rejected (single use).
	status, _ = gatewayJSON(t, srv, http.MethodPost, "/api/v1/pairing/redeem", "secret", `{"code":"`+code+`"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("second redeem should be 401, got %d", status)
	}
}

// TestPushEndpoints checks the VAPID public key is served and a subscription is
// accepted.
func TestPushEndpoints(t *testing.T) {
	t.Setenv("SOULACY_WORKSPACE", t.TempDir())
	srv := newTestGateway(t, "secret")

	status, body := gatewayJSON(t, srv, http.MethodGet, "/api/v1/push/public-key", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("public-key status = %d body=%v", status, body)
	}
	if key, _ := body["public_key"].(string); key == "" {
		t.Fatalf("expected a VAPID public key")
	}

	sub := `{"endpoint":"https://push.example.com/abc","keys":{"p256dh":"BExample","auth":"authsecret"}}`
	status, body = gatewayJSON(t, srv, http.MethodPost, "/api/v1/push/subscribe", "secret", sub)
	if status != http.StatusOK || body["ok"] != true {
		t.Fatalf("subscribe failed: %d %v", status, body)
	}
}
