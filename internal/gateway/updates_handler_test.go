package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/soulacy/soulacy/internal/updates"
)

func TestUpdatesStatusEndpointReturnsSaneDefaults(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/system/updates/status", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("updates status = %d body=%v", status, body)
	}

	if _, ok := body["checking"].(bool); !ok {
		t.Fatalf("missing checking field: %#v", body)
	}
	if _, ok := body["update_available"].(bool); !ok {
		t.Fatalf("missing update_available field: %#v", body)
	}
}

func TestTriggerUpdatesCheckMocked(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")

	// Set a mock manifest URL
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		manifest := updates.UpdateManifest{
			Product: "soulacy",
			Version: "3.5.0",
			Artifacts: []updates.UpdateArtifact{
				{
					Name:   "soulacy_3.5.0_darwin_arm64.tar.gz",
					OS:     "darwin",
					Arch:   "arm64",
					SHA256: "ea8465d64808698fde09e256b82edf068dd68fff86a07df2bd3c990b16f399ef",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer ts.Close()
	oldClient := updates.HTTPClient
	updates.HTTPClient = ts.Client()
	t.Cleanup(func() { updates.HTTPClient = oldClient })

	s.cfg.Updates.ManifestURL = ts.URL

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/system/updates/check", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("trigger updates check = %d body=%v", status, body)
	}

	if latest, ok := body["latest_version"].(string); !ok || latest != "3.5.0" {
		t.Errorf("expected latest_version 3.5.0, got %#v", body)
	}
}
