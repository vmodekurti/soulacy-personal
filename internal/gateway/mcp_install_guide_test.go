package gateway

import (
	"net/http"
	"testing"
)

func TestMCPInstallGuideRejectsNonHTTPSSourceBeforeFetching(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, response := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp/install-guide", "secret", `{"source_url":"http://127.0.0.1/server"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; response=%v", status, http.StatusBadRequest, response)
	}
	if response["error"] == "" {
		t.Fatalf("expected actionable validation error: %v", response)
	}
}
