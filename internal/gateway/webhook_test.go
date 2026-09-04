package gateway

import (
	"net/http"
	"strings"
	"testing"
)

func TestGenericWebhookRequiresAuth(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/webhooks/agent-a", "", `{"text":"hi"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

func TestGenericWebhookUsesAgentMapping(t *testing.T) {
	s, provider := newTestGatewayWithLLM(t, "secret")
	createBody := `{
		"id": "github-webhook",
		"name": "GitHub Webhook",
		"trigger": "webhook",
		"llm": {"provider": "test", "model": "fake-model"},
		"system_prompt": "Summarize the inbound event.",
		"webhook": {
			"text_path": "issue.title",
			"user_id_path": "sender.login",
			"thread_id_path": "repository.full_name",
			"session_id_path": "repository.full_name"
		},
		"enabled": true
	}`
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d body=%v", status, body)
	}

	payload := `{"issue":{"title":"Fix provider auth regression"},"sender":{"login":"octocat"},"repository":{"full_name":"acme/soulacy"}}`
	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/webhooks/github-webhook", "secret", payload)
	if status != http.StatusOK {
		t.Fatalf("webhook status = %d body=%v", status, body)
	}
	if body["reply"] != "sync reply" {
		t.Fatalf("reply body = %#v", body)
	}
	if body["user_id"] != "octocat" || body["thread_id"] != "acme/soulacy" {
		t.Fatalf("mapped ids body = %#v", body)
	}
	req := provider.lastRequest()
	// Cohort F S1: webhook messages get an untrusted-content annotation
	// prefix from annotateInboundForTrust so the model treats the
	// sender identity conservatively. The original user text is still
	// there — just preceded by the trust boundary header.
	if len(req.Messages) == 0 || !strings.Contains(req.Messages[len(req.Messages)-1].Content, "Fix provider auth regression") {
		t.Fatalf("messages = %#v", req.Messages)
	}
}

func TestGenericWebhookDefaultTextFallback(t *testing.T) {
	s, provider := newTestGatewayWithLLM(t, "secret")
	createBody := `{
		"id": "default-webhook",
		"name": "Default Webhook",
		"trigger": "webhook",
		"llm": {"provider": "test", "model": "fake-model"},
		"system_prompt": "Handle webhook.",
		"enabled": true
	}`
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d body=%v", status, body)
	}

	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/webhooks/default-webhook", "secret", `{"message":"hello from external system"}`)
	if status != http.StatusOK {
		t.Fatalf("webhook status = %d body=%v", status, body)
	}
	req := provider.lastRequest()
	if len(req.Messages) == 0 || !strings.Contains(req.Messages[len(req.Messages)-1].Content, "hello from external system") {
		t.Fatalf("messages = %#v", req.Messages)
	}
}
