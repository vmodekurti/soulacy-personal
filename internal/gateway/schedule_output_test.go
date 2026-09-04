package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/pkg/message"
)

type scheduleOutputTestAdapter struct {
	mu   sync.Mutex
	sent []message.Message
}

type channelDeliveryTestAdapter struct {
	id      string
	sendErr error
	mu      sync.Mutex
	sent    []message.Message
}

func (a *channelDeliveryTestAdapter) ID() string                                          { return a.id }
func (a *channelDeliveryTestAdapter) Name() string                                        { return "Channel Delivery Test" }
func (a *channelDeliveryTestAdapter) Start(context.Context, chan<- message.Message) error { return nil }
func (a *channelDeliveryTestAdapter) Send(_ context.Context, msg message.Message) error {
	if a.sendErr != nil {
		return a.sendErr
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, msg)
	return nil
}
func (a *channelDeliveryTestAdapter) Stop() error { return nil }
func (a *channelDeliveryTestAdapter) Status() channels.AdapterStatus {
	return channels.AdapterStatus{Connected: true, Detail: "test"}
}
func (a *channelDeliveryTestAdapter) last() (message.Message, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.sent) == 0 {
		return message.Message{}, false
	}
	return a.sent[len(a.sent)-1], true
}

func (a *scheduleOutputTestAdapter) ID() string   { return "test-output" }
func (a *scheduleOutputTestAdapter) Name() string { return "Test Output" }
func (a *scheduleOutputTestAdapter) Start(context.Context, chan<- message.Message) error {
	return nil
}
func (a *scheduleOutputTestAdapter) Send(_ context.Context, msg message.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, msg)
	return nil
}
func (a *scheduleOutputTestAdapter) Stop() error { return nil }
func (a *scheduleOutputTestAdapter) Status() channels.AdapterStatus {
	return channels.AdapterStatus{Connected: true, Detail: "test"}
}
func (a *scheduleOutputTestAdapter) last() (message.Message, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.sent) == 0 {
		return message.Message{}, false
	}
	return a.sent[len(a.sent)-1], true
}

func TestGatewayHandleTestScheduledOutput_SendsViaConfiguredChannel(t *testing.T) {
	s := newTestGateway(t, "secret")
	adp := &scheduleOutputTestAdapter{}
	s.channels.Register(adp)

	body := `{
		"id":"sched-output-agent",
		"name":"Schedule Output Agent",
		"trigger":"cron",
		"channels":["http"],
		"enabled":true,
		"schedule":{
			"cron":"0 7 * * *",
			"output":{
				"channel":"test-output",
				"to":"chat-123",
				"bot_name":"QA Bot",
				"template":"[TEST {trigger}] {agent_id}: {reply}"
			}
		},
		"llm":{"provider":"test","model":"fake-model"},
		"system_prompt":"hello"
	}`
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", body)
	if status != http.StatusCreated {
		t.Fatalf("create agent status = %d", status)
	}

	status, res := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/sched-output-agent/schedule-output/test", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("test output status = %d body=%v", status, res)
	}
	if res["channel"] != "test-output" || res["to"] != "chat-123" {
		t.Fatalf("unexpected response: %v", res)
	}

	msg, ok := adp.last()
	if !ok {
		t.Fatal("adapter did not receive a message")
	}
	if msg.Channel != "test-output" || msg.ThreadID != "chat-123" || msg.AgentID != "sched-output-agent" {
		t.Fatalf("unexpected outbound message: %+v", msg)
	}
	text := strings.Join(func() []string {
		var out []string
		for _, p := range msg.Parts {
			if p.Type == message.ContentText {
				out = append(out, p.Text)
			}
		}
		return out
	}(), "\n")
	if !strings.Contains(text, "[TEST test_output] sched-output-agent: Soulacy scheduled-output test") {
		t.Fatalf("unexpected outbound text: %q", text)
	}
}

func TestGatewayHandleTestScheduledOutput_RejectsUnregisteredChannel(t *testing.T) {
	s := newTestGateway(t, "secret")
	body := `{
		"id":"sched-output-missing-channel",
		"name":"Schedule Output Missing Channel",
		"trigger":"cron",
		"channels":["http"],
		"enabled":true,
		"schedule":{"cron":"0 7 * * *","output":{"channel":"missing-output","to":"chat-123"}},
		"llm":{"provider":"test","model":"fake-model"},
		"system_prompt":"hello"
	}`
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", body)
	if status != http.StatusCreated {
		t.Fatalf("create agent status = %d", status)
	}
	status, res := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/sched-output-missing-channel/schedule-output/test", "secret", "")
	if status != http.StatusBadRequest {
		t.Fatalf("test output status = %d body=%v", status, res)
	}
	if !strings.Contains(res["error"].(string), "not registered") {
		t.Fatalf("unexpected error: %v", res)
	}
}

func TestGatewayHandleTestChannelDelivery_UsesConfiguredDefaultDestination(t *testing.T) {
	s := newTestGateway(t, "secret")
	adp := &channelDeliveryTestAdapter{id: "telegram"}
	s.channels.Register(adp)
	s.cfg.Channels = map[string]map[string]any{}
	s.cfg.Channels["telegram"] = map[string]any{
		"enabled":           true,
		"token":             "test-token",
		"default_output_to": "chat-123",
	}

	status, res := gatewayJSON(t, s, http.MethodPost, "/api/v1/channels/telegram/test", "secret", `{"text":"hello channel"}`)
	if status != http.StatusOK {
		t.Fatalf("channel test status = %d body=%v", status, res)
	}
	msg, ok := adp.last()
	if !ok {
		t.Fatal("adapter did not receive a test message")
	}
	if msg.Channel != "telegram" || msg.ThreadID != "chat-123" {
		t.Fatalf("unexpected message destination: %+v", msg)
	}
	if len(msg.Parts) == 0 || msg.Parts[0].Text != "hello channel" {
		t.Fatalf("unexpected message body: %+v", msg.Parts)
	}
}

func TestGatewayHandleTestChannelDelivery_UsesBotMappingAdapterAndDefaultDestination(t *testing.T) {
	s := newTestGateway(t, "secret")
	adp := &channelDeliveryTestAdapter{id: "slack-research-librarian"}
	s.channels.Register(adp)
	s.cfg.Channels = map[string]map[string]any{}
	s.cfg.Channels["slack"] = map[string]any{
		"enabled":   true,
		"bot_token": "xoxb-default",
		"app_token": "xapp-default",
		"bots": []any{
			map[string]any{
				"bot_token":         "xoxb-test",
				"app_token":         "xapp-test",
				"agent_id":          "research-librarian",
				"default_output_to": "C123",
			},
		},
	}

	status, res := gatewayJSON(t, s, http.MethodPost, "/api/v1/channels/slack/test", "secret", `{"adapter_id":"slack-research-librarian","message":"hello bot mapping"}`)
	if status != http.StatusOK {
		t.Fatalf("channel test status = %d body=%v", status, res)
	}
	if res["channel"] != "slack-research-librarian" || res["channel_family"] != "slack" {
		t.Fatalf("unexpected response: %v", res)
	}
	msg, ok := adp.last()
	if !ok {
		t.Fatal("adapter did not receive a test message")
	}
	if msg.Channel != "slack-research-librarian" || msg.ThreadID != "C123" {
		t.Fatalf("unexpected message destination: %+v", msg)
	}
	if len(msg.Parts) == 0 || msg.Parts[0].Text != "hello bot mapping" {
		t.Fatalf("unexpected message body: %+v", msg.Parts)
	}
}

func TestGatewayHandleTestChannelDelivery_RejectsMissingDestination(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.channels.Register(&channelDeliveryTestAdapter{id: "telegram"})
	s.cfg.Channels = map[string]map[string]any{}
	s.cfg.Channels["telegram"] = map[string]any{"enabled": true, "token": "test-token"}

	status, res := gatewayJSON(t, s, http.MethodPost, "/api/v1/channels/telegram/test", "secret", `{}`)
	if status != http.StatusBadRequest {
		t.Fatalf("channel test status = %d body=%v", status, res)
	}
	if !strings.Contains(res["error"].(string), "destination is required") {
		t.Fatalf("unexpected error: %v", res)
	}
}

func TestGatewayHandleTestChannelDelivery_ReturnsDiagnosisOnSendFailure(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.channels.Register(&channelDeliveryTestAdapter{
		id:      "telegram",
		sendErr: errors.New("telegram: send: API returned status 400: Bad Request: chat not found"),
	})
	s.cfg.Channels = map[string]map[string]any{}
	s.cfg.Channels["telegram"] = map[string]any{
		"enabled":           true,
		"token":             "test-token",
		"default_output_to": "8546291328",
	}

	status, res := gatewayJSON(t, s, http.MethodPost, "/api/v1/channels/telegram/test", "secret", `{"text":"hello channel"}`)
	if status != http.StatusBadGateway {
		t.Fatalf("channel test status = %d body=%v", status, res)
	}
	diag, ok := res["diagnosis"].(map[string]any)
	if !ok {
		t.Fatalf("diagnosis missing: %#v", res)
	}
	if diag["category"] != string(channels.DeliveryInvalidDest) {
		t.Fatalf("category = %v, want invalid destination; body=%v", diag["category"], res)
	}
	if fix, _ := diag["fix"].(string); !strings.Contains(fix, "-100") {
		t.Fatalf("fix = %q, want Telegram chat id guidance", fix)
	}
}
