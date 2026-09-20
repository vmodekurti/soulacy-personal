package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// anonymousMCP is an MCP server that answers initialize and tools/list for
// anyone — the shape (#162) that made "Test connection" vouch for a key
// nobody had looked at.
func anonymousMCP(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusOK)
			return
		}
		result := `{"protocolVersion":"2024-11-05","capabilities":{}}`
		if req.Method == "tools/list" {
			result = `{"tools":[{"name":"GetLatestClosingPrices","inputSchema":{"type":"object"}},{"name":"GetNews","inputSchema":{"type":"object"}}]}`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result)
	}))
}

func TestGatewayHandleTestMCPServer_SaysWhenCredentialWasNotVerified(t *testing.T) {
	srv := anonymousMCP(t)
	defer srv.Close()
	s := newTestGateway(t, "secret")

	body := fmt.Sprintf(`{"transport":"http","url":%q,"auth":{"type":"bearer","header":"api_key","scheme":"Bearer","secret_ref":"mcp.quotes.credential"},"auth_secret":"eq_test"}`, srv.URL)
	status, res := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp/test", "secret", body)
	if status != http.StatusOK || res["ok"] != true {
		t.Fatalf("status=%d body=%v", status, res)
	}
	if res["credential_verified"] != false {
		t.Fatalf("credential_verified = %v, want false (server accepts anonymous handshake): %v", res["credential_verified"], res)
	}
	msg, _ := res["message"].(string)
	if !strings.Contains(msg, "could not be verified") || !strings.Contains(msg, "2 tools") {
		t.Fatalf("message = %q", msg)
	}
}

func TestGatewayHandleTestMCPServer_NoAuthConfiguredClaimsNothing(t *testing.T) {
	srv := anonymousMCP(t)
	defer srv.Close()
	s := newTestGateway(t, "secret")

	status, res := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp/test", "secret", fmt.Sprintf(`{"transport":"http","url":%q}`, srv.URL))
	if status != http.StatusOK || res["ok"] != true {
		t.Fatalf("status=%d body=%v", status, res)
	}
	if _, present := res["credential_verified"]; present {
		t.Fatalf("no credential configured, yet credential_verified is reported: %v", res)
	}
	if msg, _ := res["message"].(string); !strings.Contains(msg, "2 tools") {
		t.Fatalf("message = %q", msg)
	}
}
