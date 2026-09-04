package gateway

import (
	"net/http"
	"strings"
	"testing"
)

func TestGatewayChatVoiceModeIsPrivateAndBounded(t *testing.T) {
	s, provider := newTestGatewayWithLLM(t, "secret")
	provider.content = `<spoken_response>Micron looks strong, but watch the low volume.</spoken_response><display_response>## Full analysis

| Metric | Value |
| --- | --- |
| Forward P/E | 6.2 |</display_response>`
	createBody := `{
		"id":"voice-agent","name":"Voice Agent","trigger":"channel",
		"channels":["http"],"llm":{"provider":"test","model":"base-model"},
		"system_prompt":"Answer accurately.","enabled":true
	}`
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d body=%v", status, body)
	}

	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/chat", "secret", `{
		"agent_id":"voice-agent","session_id":"voice-session",
		"text":"How is Micron performing?","response_mode":"voice"
	}`)
	if status != http.StatusOK {
		t.Fatalf("chat status = %d body=%v", status, body)
	}
	req := provider.lastRequest()
	if len(req.Messages) < 2 {
		t.Fatalf("messages = %#v", req.Messages)
	}
	if !strings.Contains(req.Messages[0].Content, "## Voice Response Mode") {
		t.Fatalf("system prompt missing voice contract: %q", req.Messages[0].Content)
	}
	if got := req.Messages[len(req.Messages)-1].Content; got != "How is Micron performing?" {
		t.Fatalf("visible user message was altered: %q", got)
	}
	if got := body["spoken_reply"]; got != "Micron looks strong, but watch the low volume." {
		t.Fatalf("spoken_reply = %#v", got)
	}
	if got, _ := body["reply"].(string); !strings.Contains(got, "## Full analysis") || strings.Contains(got, "spoken_response") {
		t.Fatalf("display reply = %q", got)
	}
}

func TestGatewayChatRejectsUnknownResponseMode(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/chat", "secret",
		`{"agent_id":"anything","text":"hello","response_mode":"telepathy"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if !strings.Contains(body["error"].(string), "response_mode") {
		t.Fatalf("body = %v", body)
	}
}
