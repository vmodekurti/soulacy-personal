package gateway

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestBrowserTraceIncludesAgentNetworkPolicy(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.loader.Register(&agent.Definition{
		ID:      "browser-agent",
		Name:    "Browser Agent",
		Enabled: true,
		Policy: agent.ToolPolicyConfig{
			Enabled:      true,
			Network:      "prompt",
			AllowDomains: []string{"example.com", "docs.internal"},
			DenyDomains:  []string{"ads.example.com"},
		},
	})
	s.actions = &fakeTailBackend{events: []message.Event{{
		Type:      "tool.call",
		AgentID:   "browser-agent",
		SessionID: "sess-browser",
		Timestamp: time.Now().UTC(),
		Payload: message.ToolCall{
			Name:      "mcp__playwright__browser_navigate",
			Arguments: map[string]any{"url": "https://example.com/docs"},
		},
	}}}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/browser/trace?agent_id=browser-agent&session_id=sess-browser", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	policy, ok := body["policy"].(map[string]any)
	if !ok {
		t.Fatalf("missing policy summary: %#v", body)
	}
	if policy["browser_action"] != "prompt" || policy["requires_approval"] != true {
		t.Fatalf("unexpected policy summary: %#v", policy)
	}
	allow, _ := policy["allow_domains"].([]any)
	if len(allow) != 2 || allow[0] != "example.com" {
		t.Fatalf("allow domains not preserved: %#v", policy)
	}
	trace, _ := body["trace"].(map[string]any)
	if trace["last_url"] != "https://example.com/docs" {
		t.Fatalf("trace missing browser event: %#v", trace)
	}
}

func TestBrowserTraceServesLocalScreenshotArtifact(t *testing.T) {
	s := newTestGateway(t, "secret")
	dir := t.TempDir()
	shot := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(shot, []byte("fake-png"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.actions = &fakeTailBackend{events: []message.Event{{
		Type:      "tool.call",
		AgentID:   "browser-agent",
		SessionID: "sess-browser",
		Timestamp: time.Now().UTC(),
		Payload: message.ToolCall{
			Name:      "mcp__playwright__browser_screenshot",
			Arguments: map[string]any{"path": shot},
		},
	}}}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/browser/trace?agent_id=browser-agent&session_id=sess-browser", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	trace := body["trace"].(map[string]any)
	steps := trace["steps"].([]any)
	step := steps[0].(map[string]any)
	if got, _ := step["screenshot_url"].(string); !strings.Contains(got, "/api/v1/browser/artifact?") {
		t.Fatalf("screenshot_url = %q, want browser artifact endpoint", got)
	}

	path := "/api/v1/browser/artifact?agent_id=browser-agent&session_id=sess-browser&path=" + url.QueryEscape(shot)
	dstatus, raw := gatewayRaw(t, s, http.MethodGet, path, "secret", "")
	if dstatus != http.StatusOK || raw != "fake-png" {
		t.Fatalf("artifact status=%d body=%q", dstatus, raw)
	}
	bad := "/api/v1/browser/artifact?agent_id=browser-agent&session_id=sess-browser&path=" + url.QueryEscape(filepath.Join(dir, "other.png"))
	if st, _ := gatewayRaw(t, s, http.MethodGet, bad, "secret", ""); st != http.StatusNotFound {
		t.Fatalf("unreferenced screenshot status = %d, want 404", st)
	}
}
