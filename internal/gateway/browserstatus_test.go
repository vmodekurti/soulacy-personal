package gateway

import (
	"net/http"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

func TestBrowserStatusEndpointReturnsChecks(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/browser/status", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("browser status = %d body=%v", status, body)
	}
	checks, ok := body["checks"].([]any)
	if !ok || len(checks) == 0 {
		t.Fatalf("missing checks: %#v", body)
	}
	if _, ok := body["score"].(float64); !ok {
		t.Fatalf("missing score: %#v", body)
	}
}

func TestReadinessIncludesBrowserAutomationPosture(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("readiness status = %d body=%v", status, body)
	}
	if _, ok := body["browser"].(map[string]any); !ok {
		t.Fatalf("missing browser readiness: %#v", body)
	}
	parity := body["parity"].(map[string]any)
	areas := parity["areas"].([]any)
	for _, raw := range areas {
		area := raw.(map[string]any)
		if area["key"] == "browser" {
			if got, _ := area["score"].(float64); got <= 0 {
				t.Fatalf("browser score = %v", area["score"])
			}
			return
		}
	}
	t.Fatalf("browser parity area missing: %#v", areas)
}

func TestParityBrowserAutomationScoresReadyPosture(t *testing.T) {
	area := parityBrowserAutomation(browserAutomationReadiness{Status: "ok", Score: 100})
	if area.Status != "ok" || area.Score < 82 {
		t.Fatalf("browser parity = %#v, want ok >= 82", area)
	}

	area = parityBrowserAutomation(browserAutomationReadiness{
		Status: "warn",
		Score:  40,
		NextActions: []executorAction{{
			Detail: "Add Browser headless from MCP.",
		}},
	})
	if area.Status != "fail" || area.Next == "" {
		t.Fatalf("browser parity = %#v, want fail with next action", area)
	}
}

func TestBrowserPolicyPostureWarnsForUnmanagedBrowserAgents(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	tools := []string{"mcp__browser__navigate"}
	s.loader.Register(&agent.Definition{ID: "browser-bot", Name: "Browser Bot", MCPTools: &tools})

	posture := s.browserPolicyPosture(nil)
	if posture.Status != "warn" || posture.UnmanagedAgents != 1 {
		t.Fatalf("policy posture = %#v, want warn with one unmanaged agent", posture)
	}
	if len(posture.UnmanagedIDs) != 1 || posture.UnmanagedIDs[0] != "browser-bot" {
		t.Fatalf("unmanaged ids = %#v", posture.UnmanagedIDs)
	}
}

func TestBrowserPolicyPostureAcceptsExplicitDomainPolicy(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	tools := []string{"mcp__browser__navigate"}
	s.loader.Register(&agent.Definition{
		ID:       "browser-bot",
		Name:     "Browser Bot",
		MCPTools: &tools,
		Policy: agent.ToolPolicyConfig{
			Enabled:      true,
			Network:      "prompt",
			AllowDomains: []string{"example.com"},
		},
	})

	posture := s.browserPolicyPosture(nil)
	if posture.Status != "ok" || posture.ManagedAgents != 1 || posture.UnmanagedAgents != 0 {
		t.Fatalf("policy posture = %#v, want ok with one managed agent", posture)
	}
}
