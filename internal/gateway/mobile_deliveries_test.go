package gateway

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	mobilechan "github.com/soulacy/soulacy/internal/channels/mobile"
)

func TestMobileDeliveryAPIRegistersScopesAndRetainsGenie(t *testing.T) {
	store, err := mobilechan.Open(filepath.Join(t.TempDir(), "mobile.db"))
	if err != nil {
		t.Fatal(err)
	}
	mobilechan.SetDefaultStore(store)
	t.Cleanup(func() {
		mobilechan.SetDefaultStore(nil)
		_ = store.Close()
	})
	s := newTestGateway(t, "secret")

	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/mobile/devices", "secret",
		`{"id":"phone","name":"Owner's iPhone","push_token":"bad","notifications_enabled":true}`)
	if status != http.StatusBadRequest {
		t.Fatalf("invalid push token status = %d, want 400", status)
	}
	status, _ = gatewayJSON(t, s, http.MethodPost, "/api/v1/mobile/devices", "secret",
		`{"id":"phone","bundle_id":"com.example.untrusted"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("untrusted bundle id status = %d, want 400", status)
	}
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mobile/devices", "secret",
		`{"id":"phone","name":"Owner's iPhone","notifications_enabled":false}`)
	if status != http.StatusCreated || body["registered"] != true {
		t.Fatalf("register status=%d body=%v", status, body)
	}

	ctx := context.Background()
	for _, delivery := range []mobilechan.Delivery{
		{ID: "regular", Destination: "all", AgentID: "reporter", Body: "report"},
		{ID: "genie", Destination: "all", AgentID: "genie", Body: "orchestrated result"},
		{ID: "system", Destination: "all", AgentID: "system", Body: "internal output"},
	} {
		if err := store.Add(ctx, "personal", delivery); err != nil {
			t.Fatal(err)
		}
	}

	status, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/mobile/deliveries?device_id=phone", "secret", "")
	if status != http.StatusOK || body["unread_count"] != float64(2) {
		t.Fatalf("list status=%d body=%v", status, body)
	}
	items, ok := body["deliveries"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("visible deliveries = %#v, want regular and Genie only", body["deliveries"])
	}
	seen := map[string]bool{}
	for _, item := range items {
		if row, ok := item.(map[string]any); ok {
			seen[row["agent_id"].(string)] = true
		}
	}
	if !seen["reporter"] || !seen["genie"] || seen["system"] {
		t.Fatalf("visible agents = %v", seen)
	}

	status, _ = gatewayJSON(t, s, http.MethodGet, "/api/v1/mobile/deliveries/system?device_id=phone", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("system delivery status = %d, want 404", status)
	}
	status, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/mobile/deliveries/genie?device_id=phone", "secret", "")
	if status != http.StatusOK || body["agent_id"] != "genie" {
		t.Fatalf("Genie delivery status=%d body=%v", status, body)
	}

	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/mobile/deliveries/regular/read", "secret", `{"device_id":"phone"}`)
	if status != http.StatusOK || body["read"] != true {
		t.Fatalf("read status=%d body=%v", status, body)
	}
	status, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/mobile/deliveries?device_id=phone", "secret", "")
	if status != http.StatusOK || body["unread_count"] != float64(1) {
		t.Fatalf("unread status=%d body=%v", status, body)
	}

	status, _ = gatewayJSON(t, s, http.MethodGet, "/api/v1/mobile/deliveries?device_id=someone-elses-phone", "secret", "")
	if status != http.StatusForbidden {
		t.Fatalf("unregistered device status = %d, want 403", status)
	}
}
