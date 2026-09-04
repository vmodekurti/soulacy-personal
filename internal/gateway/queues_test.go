package gateway

import (
	"net/http"
	"testing"
)

func TestGatewayQueuesRoundTrip(t *testing.T) {
	s := newTestGateway(t, "secret")

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/queues", "secret", `{"queue":"pending_resources"}`)
	if status != http.StatusOK {
		t.Fatalf("create queue status = %d body=%v", status, body)
	}
	if body["queue"] != "pending_resources" || body["created"] != true {
		t.Fatalf("create queue body = %v", body)
	}

	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/queues/items", "secret", `{"queue":"pending_resources","item":{"url":"https://example.com/a"},"ttl_seconds":60}`)
	if status != http.StatusCreated {
		t.Fatalf("put item status = %d body=%v", status, body)
	}
	if body["queue"] != "pending_resources" || body["id"] == "" {
		t.Fatalf("put item body = %v", body)
	}

	status, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/queues", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("queue names status = %d body=%v", status, body)
	}
	queues, _ := body["queues"].([]any)
	if len(queues) != 1 {
		t.Fatalf("queues = %v", body)
	}

	status, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/queues/items?queue=pending_resources", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list items status = %d body=%v", status, body)
	}
	if body["count"].(float64) != 1 {
		t.Fatalf("list items body = %v", body)
	}

	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/queues/take?queue=pending_resources", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("take item status = %d body=%v", status, body)
	}
	item := body["item"].(map[string]any)
	if item["url"] != "https://example.com/a" {
		t.Fatalf("take item body = %v", body)
	}

	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/queues/take?queue=pending_resources", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("take empty status = %d body=%v", status, body)
	}
	if body["empty"] != true {
		t.Fatalf("take empty body = %v", body)
	}
}

func TestGatewayQueuesRejectBadInput(t *testing.T) {
	s := newTestGateway(t, "secret")

	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/queues", "secret", `{"queue":"../bad"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("bad queue status = %d, want 400", status)
	}

	status, _ = gatewayJSON(t, s, http.MethodPost, "/api/v1/queues/items", "secret", `{"queue":"safe"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("missing item status = %d, want 400", status)
	}
}
