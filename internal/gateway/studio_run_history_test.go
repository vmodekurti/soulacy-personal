package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestStudioRunHistoryMergesFlowAndDurableRuns(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.engine.TagFlowRun("history-agent", "flow-only", "http")

	base := time.Date(2026, 7, 4, 7, 0, 0, 0, time.UTC)
	s.actions = &fakeTailBackend{events: []message.Event{
		{
			Type: "message.in", AgentID: "history-agent", SessionID: "cron-run", Timestamp: base,
			Payload: message.Message{
				Channel:  "http",
				Metadata: map[string]string{"trigger": "cron"},
				Parts:    message.Text("__trigger:cron__"),
			},
		},
		{
			Type: "tool.result", AgentID: "history-agent", SessionID: "cron-run", Timestamp: base.Add(time.Second),
			Payload: map[string]any{"name": "kb_search", "is_error": false, "content": "ok"},
		},
		{
			Type: "message.out", AgentID: "history-agent", SessionID: "cron-run", Timestamp: base.Add(2 * time.Second),
			Payload: message.Message{Parts: message.Text("daily report")},
		},
		{
			Type: "schedule.output", AgentID: "history-agent", SessionID: "cron-run", Timestamp: base.Add(3 * time.Second),
			Payload: map[string]any{"delivered": true, "channel": "telegram", "to": "123", "trigger": "cron", "reply_preview": "daily report"},
		},
		{
			Type: "message.in", AgentID: "history-agent", SessionID: "slack-fail", Timestamp: base.Add(4 * time.Second),
			Payload: message.Message{Channel: "slack", Parts: message.Text("run it")},
		},
		{
			Type: "tool.result", AgentID: "history-agent", SessionID: "slack-fail", Timestamp: base.Add(5 * time.Second),
			Payload: map[string]any{"name": "fetch_url", "is_error": true, "content": "fetch failed"},
		},
		{
			Type: "schedule.output", AgentID: "history-agent", SessionID: "delivery-only", Timestamp: base.Add(6 * time.Second),
			Payload: map[string]any{
				"delivered":     true,
				"fallback":      true,
				"channel":       "slack",
				"to":            "C123",
				"trigger":       "cron",
				"reply_preview": "fallback-routed digest",
			},
		},
	}}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/studio/run-history?agentId=history-agent", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("run history status = %d body=%v", status, body)
	}
	rawRuns, ok := body["runs"].([]any)
	if !ok {
		t.Fatalf("runs missing: %#v", body)
	}
	runs := map[string]map[string]any{}
	for _, raw := range rawRuns {
		m, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("run is not object: %#v", raw)
		}
		id, _ := m["runId"].(string)
		runs[id] = m
	}
	if _, ok := runs["flow-only"]; !ok {
		t.Fatalf("flow-only run missing from merged history: %#v", runs)
	}
	if got := runs["cron-run"]["trigger"]; got != "cron" {
		t.Fatalf("cron trigger = %v, want cron", got)
	}
	if got := runs["cron-run"]["deliveryStatus"]; got != "delivered" {
		t.Fatalf("cron delivery status = %v, want delivered", got)
	}
	if got := runs["slack-fail"]["status"]; got != "failed" {
		t.Fatalf("slack failure status = %v, want failed", got)
	}
	if got := runs["slack-fail"]["trigger"]; got != "slack" {
		t.Fatalf("slack trigger = %v, want slack", got)
	}
	if got := runs["delivery-only"]["status"]; got != "success" {
		t.Fatalf("delivery-only status = %v, want success", got)
	}
	if got := runs["delivery-only"]["trigger"]; got != "cron" {
		t.Fatalf("delivery-only trigger = %v, want cron", got)
	}
	if got := runs["delivery-only"]["deliveryStatus"]; got != "delivered via fallback" {
		t.Fatalf("delivery-only delivery status = %v, want delivered via fallback", got)
	}
	if got := runs["delivery-only"]["output"]; got != "fallback-routed digest" {
		t.Fatalf("delivery-only output = %v, want fallback-routed digest", got)
	}
}

func TestStudioRunHistorySplitsRepeatedChannelMessagesInSameSession(t *testing.T) {
	s := newTestGateway(t, "secret")
	base := time.Date(2026, 7, 4, 11, 0, 0, 0, time.UTC)
	s.actions = &fakeTailBackend{events: []message.Event{
		{
			Type: "message.in", AgentID: "research-librarian", SessionID: "slack-D123", Timestamp: base,
			Payload: message.Message{Channel: "slack", Parts: message.Text("add url one")},
		},
		{
			Type: "message.out", AgentID: "research-librarian", SessionID: "slack-D123", Timestamp: base.Add(time.Second),
			Payload: message.Message{Parts: message.Text("queued one")},
		},
		{
			Type: "message.in", AgentID: "research-librarian", SessionID: "slack-D123", Timestamp: base.Add(5 * time.Minute),
			Payload: message.Message{Channel: "slack", Parts: message.Text("add url two")},
		},
		{
			Type: "tool.result", AgentID: "research-librarian", SessionID: "slack-D123", Timestamp: base.Add(5*time.Minute + time.Second),
			Payload: map[string]any{"name": "queue_put", "is_error": false, "content": "queued two"},
		},
		{
			Type: "message.out", AgentID: "research-librarian", SessionID: "slack-D123", Timestamp: base.Add(5*time.Minute + 2*time.Second),
			Payload: message.Message{Parts: message.Text("queued two")},
		},
	}}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/studio/run-history?agentId=research-librarian", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("run history status = %d body=%v", status, body)
	}
	rawRuns, ok := body["runs"].([]any)
	if !ok {
		t.Fatalf("runs missing: %#v", body)
	}
	if len(rawRuns) != 2 {
		t.Fatalf("runs len = %d, want 2: %#v", len(rawRuns), rawRuns)
	}
	first, _ := rawRuns[0].(map[string]any)
	second, _ := rawRuns[1].(map[string]any)
	if first["sessionId"] != "slack-D123" || second["sessionId"] != "slack-D123" {
		t.Fatalf("session ids = %#v / %#v, want same channel session", first["sessionId"], second["sessionId"])
	}
	if first["runId"] == second["runId"] {
		t.Fatalf("run IDs collapsed for repeated channel messages: %#v", rawRuns)
	}
	if first["output"] != "queued two" || second["output"] != "queued one" {
		t.Fatalf("outputs not split newest-first: %#v", rawRuns)
	}
}
