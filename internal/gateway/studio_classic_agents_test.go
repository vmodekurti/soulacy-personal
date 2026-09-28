package gateway

import (
	"net/http"
	"testing"
)

func TestStudioListIncludesClassicAgentAsManageOnly(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", `{
		"id":"classic-agent","name":"Classic Agent","enabled":true,
		"trigger":"channel","system_prompt":"Do the work.",
		"llm":{"provider":"openai","model":"gpt-4o-mini"},
		"memory":{"read_scopes":["session"],"write_scopes":["session"],"max_tokens":20},
		"max_turns":5
	}`)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d", status)
	}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/studio/agents", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list status = %d body=%v", status, body)
	}
	agents, _ := body["agents"].([]any)
	var got map[string]any
	for _, item := range agents {
		candidate, _ := item.(map[string]any)
		if candidate["id"] == "classic-agent" {
			got = candidate
			break
		}
	}
	if got == nil || got["editable"] != false {
		t.Fatalf("classic summary = %#v", got)
	}

	status, _ = gatewayJSON(t, s, http.MethodGet, "/api/v1/studio/agents/classic-agent", "secret", "")
	if status != http.StatusBadRequest {
		t.Fatalf("classic load status = %d, want 400", status)
	}
}

func TestStudioLoadsAutoReasoningAgent(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", `{
		"id":"auto-agent","name":"Auto Agent","enabled":true,
		"trigger":"channel","system_prompt":"Use tools.",
		"reasoning":{"strategy":"auto"},
		"llm":{"provider":"openai","model":"gpt-4o-mini"},
		"memory":{"read_scopes":["session"],"write_scopes":["session"],"max_tokens":20},
		"max_turns":5
	}`)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d", status)
	}
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/studio/agents/auto-agent", "secret", "")
	if status != http.StatusOK || body["workflow"] == nil {
		t.Fatalf("load status=%d body=%v", status, body)
	}
}
