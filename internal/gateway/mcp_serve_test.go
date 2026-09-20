package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/soulacy/soulacy/internal/httptestutil"
)

func mcpRequest(t *testing.T, s *Server, method, apiKey, body, protocolVersion string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, "/mcp", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("new MCP request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if protocolVersion != "" {
		req.Header.Set("MCP-Protocol-Version", protocolVersion)
	}
	resp, err := s.app.Test(httptestutil.WithHost(req), -1)
	if err != nil {
		t.Fatalf("MCP app.Test: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read MCP response: %v", err)
	}
	return resp.StatusCode, resp.Header, raw
}

func decodeMCPResponse(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode MCP response %q: %v", string(raw), err)
	}
	return out
}

func TestRemoteMCPRequiresGatewayAuthentication(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, _, _ := mcpRequest(t, s, http.MethodPost, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

func TestRemoteMCPInitializeAndNotification(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, headers, raw := mcpRequest(t, s, http.MethodPost, "secret", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`, "")
	if status != http.StatusOK {
		t.Fatalf("initialize status = %d body=%s", status, raw)
	}
	if got := headers.Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	resp := decodeMCPResponse(t, raw)
	result := resp["result"].(map[string]any)
	if result["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocol version = %v", result["protocolVersion"])
	}

	status, _, raw = mcpRequest(t, s, http.MethodPost, "secret", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "2025-06-18")
	if status != http.StatusAccepted || len(raw) != 0 {
		t.Fatalf("notification status = %d body=%q, want 202 with no body", status, raw)
	}
}

func TestRemoteMCPListsAgentsAndCallsChat(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	create := `{"id":"mcp-agent","name":"MCP Agent","description":"Answers remote MCP calls.","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"fake-model"},"system_prompt":"Be helpful.","enabled":true}`
	if status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", create); status != http.StatusCreated {
		t.Fatalf("create status = %d body=%v", status, body)
	}

	status, _, raw := mcpRequest(t, s, http.MethodPost, "secret", `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, "2025-11-25")
	if status != http.StatusOK {
		t.Fatalf("tools/list status = %d body=%s", status, raw)
	}
	resp := decodeMCPResponse(t, raw)
	tools := resp["result"].(map[string]any)["tools"].([]any)
	foundAgent := false
	for _, item := range tools {
		tool := item.(map[string]any)
		if name, _ := tool["name"].(string); len(name) > 0 && name != "soulacy_chat" {
			if description, _ := tool["description"].(string); description == "Call the Soulacy agent MCP Agent. Answers remote MCP calls." {
				foundAgent = true
			}
		}
	}
	if !foundAgent {
		t.Fatalf("MCP agent tool missing from %#v", tools)
	}

	call := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"soulacy_chat","arguments":{"agent_id":"mcp-agent","text":"hello from MCP","session_id":"remote-mcp-test"}}}`
	status, _, raw = mcpRequest(t, s, http.MethodPost, "secret", call, "2025-11-25")
	if status != http.StatusOK {
		t.Fatalf("tools/call status = %d body=%s", status, raw)
	}
	resp = decodeMCPResponse(t, raw)
	if rpcErr := resp["error"]; rpcErr != nil {
		t.Fatalf("tools/call error = %#v", rpcErr)
	}
	content := resp["result"].(map[string]any)["content"].([]any)
	if got := content[0].(map[string]any)["text"]; got != "sync reply" {
		t.Fatalf("chat reply = %v", got)
	}
}

func TestRemoteMCPRejectsUnsupportedTransportOperationsAndVersion(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, headers, _ := mcpRequest(t, s, http.MethodGet, "secret", "", "")
	if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodPost {
		t.Fatalf("GET status = %d allow=%q", status, headers.Get("Allow"))
	}
	status, _, raw := mcpRequest(t, s, http.MethodPost, "secret", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "2099-01-01")
	if status != http.StatusBadRequest {
		t.Fatalf("unsupported version status = %d body=%s", status, raw)
	}
}

func TestRemoteMCPRejectsUnknownBrowserOrigin(t *testing.T) {
	s := newTestGateway(t, "secret")
	req, err := http.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Origin", "https://attacker.example")
	resp, err := s.app.Test(httptestutil.WithHost(req), -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}
