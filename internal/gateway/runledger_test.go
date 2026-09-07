package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestRunLedgerGroupsAllTriggerSources(t *testing.T) {
	s := newTestGateway(t, "secret")
	base := time.Date(2026, 7, 4, 7, 0, 0, 0, time.UTC)
	s.actions = &fakeTailBackend{events: []message.Event{
		{
			Type: "message.in", AgentID: "research-librarian", SessionID: "scheduler", Timestamp: base,
			Payload: message.Message{Channel: "http", Metadata: map[string]string{"trigger": "cron"}, Parts: message.Text("__trigger:cron__")},
		},
		{
			Type: "message.out", AgentID: "research-librarian", SessionID: "scheduler", Timestamp: base.Add(time.Second),
			Payload: message.Message{Parts: message.Text("daily digest")},
		},
		{
			Type: "schedule.output", AgentID: "research-librarian", SessionID: "scheduler", Timestamp: base.Add(2 * time.Second),
			Payload: map[string]any{"delivered": true, "channel": "telegram", "to": "123", "trigger": "cron", "reply_preview": "daily digest"},
		},
		{
			Type: "message.in", AgentID: "research-librarian", SessionID: "slack-D1", Timestamp: base.Add(3 * time.Second),
			Payload: message.Message{Channel: "slack", Parts: message.Text("add url one")},
		},
		{
			Type: "message.out", AgentID: "research-librarian", SessionID: "slack-D1", Timestamp: base.Add(4 * time.Second),
			Payload: message.Message{Parts: message.Text("queued one")},
		},
		{
			Type: "message.in", AgentID: "research-librarian", SessionID: "slack-D1", Timestamp: base.Add(5 * time.Minute),
			Payload: message.Message{Channel: "slack", Parts: message.Text("add url two")},
		},
		{
			Type: "tool.result", AgentID: "research-librarian", SessionID: "slack-D1", Timestamp: base.Add(5*time.Minute + time.Second),
			Payload: map[string]any{"name": "queue_put", "is_error": false, "content": "queued two"},
		},
		{
			Type: "message.out", AgentID: "research-librarian", SessionID: "slack-D1", Timestamp: base.Add(5*time.Minute + 2*time.Second),
			Payload: message.Message{Parts: message.Text("queued two")},
		},
		{
			Type: "schedule.output", AgentID: "stock-screener", SessionID: "delivery-only", Timestamp: base.Add(10 * time.Minute),
			Payload: map[string]any{"delivered": false, "channel": "telegram", "to": "999", "trigger": "cron", "reason": "chat not found", "reply_preview": "screen report"},
		},
	}}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/runs/ledger?limit=20", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("ledger status = %d body=%v", status, body)
	}
	rawRuns, ok := body["runs"].([]any)
	if !ok {
		t.Fatalf("runs missing: %#v", body)
	}
	if len(rawRuns) != 4 {
		t.Fatalf("runs len = %d, want 4: %#v", len(rawRuns), rawRuns)
	}
	byOutput := map[string]map[string]any{}
	for _, raw := range rawRuns {
		run := raw.(map[string]any)
		if out, _ := run["output"].(string); out != "" {
			byOutput[out] = run
		}
	}
	if got := byOutput["daily digest"]["deliveryStatus"]; got != "delivered" {
		t.Fatalf("cron delivery status = %v, want delivered", got)
	}
	if got := byOutput["queued one"]["trigger"]; got != "slack" {
		t.Fatalf("slack trigger = %v, want slack", got)
	}
	if got := byOutput["queued two"]["sessionId"]; got != "slack-D1" {
		t.Fatalf("second slack session = %v, want slack-D1", got)
	}
	if got := byOutput["screen report"]["status"]; got != "failed" {
		t.Fatalf("delivery-only status = %v, want failed", got)
	}
	if got := byOutput["screen report"]["deliveryError"]; got != "chat not found" {
		t.Fatalf("delivery-only error = %v, want chat not found", got)
	}
}

func TestRunLedgerFiltersAgent(t *testing.T) {
	s := newTestGateway(t, "secret")
	base := time.Date(2026, 7, 4, 7, 0, 0, 0, time.UTC)
	s.actions = &fakeTailBackend{events: []message.Event{
		{Type: "message.in", AgentID: "a", SessionID: "s1", Timestamp: base, Payload: message.Message{Channel: "http"}},
		{Type: "message.out", AgentID: "a", SessionID: "s1", Timestamp: base.Add(time.Second), Payload: message.Message{Parts: message.Text("a")}},
		{Type: "message.in", AgentID: "b", SessionID: "s2", Timestamp: base.Add(2 * time.Second), Payload: message.Message{Channel: "http"}},
		{Type: "message.out", AgentID: "b", SessionID: "s2", Timestamp: base.Add(3 * time.Second), Payload: message.Message{Parts: message.Text("b")}},
	}}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/runs/ledger?agent_id=a", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("ledger status = %d body=%v", status, body)
	}
	rawRuns := body["runs"].([]any)
	if len(rawRuns) != 1 {
		t.Fatalf("runs len = %d, want 1: %#v", len(rawRuns), rawRuns)
	}
	if got := rawRuns[0].(map[string]any)["agentId"]; got != "a" {
		t.Fatalf("agentId = %v, want a", got)
	}
}

func TestRunLedgerMarksBrowserTraceRuns(t *testing.T) {
	s := newTestGateway(t, "secret")
	base := time.Date(2026, 7, 4, 7, 0, 0, 0, time.UTC)
	s.actions = &fakeTailBackend{events: []message.Event{
		{Type: "message.in", AgentID: "browser-agent", SessionID: "sess-browser", Timestamp: base, Payload: message.Message{Channel: "http"}},
		{
			Type:      "tool.call",
			AgentID:   "browser-agent",
			SessionID: "sess-browser",
			Timestamp: base.Add(time.Second),
			Payload:   map[string]any{"name": "mcp__playwright__browser_navigate", "arguments": map[string]any{"url": "https://example.com"}},
		},
		{
			Type:      "tool.result",
			AgentID:   "browser-agent",
			SessionID: "sess-browser",
			Timestamp: base.Add(2 * time.Second),
			Payload:   map[string]any{"name": "mcp__playwright__browser_navigate", "content": "ok"},
		},
		{Type: "message.out", AgentID: "browser-agent", SessionID: "sess-browser", Timestamp: base.Add(3 * time.Second), Payload: message.Message{Parts: message.Text("done")}},
	}}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/runs/ledger?agent_id=browser-agent", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("ledger status = %d body=%v", status, body)
	}
	rawRuns := body["runs"].([]any)
	if len(rawRuns) != 1 {
		t.Fatalf("runs len = %d, want 1: %#v", len(rawRuns), rawRuns)
	}
	run := rawRuns[0].(map[string]any)
	if got := run["hasBrowserTrace"]; got != true {
		t.Fatalf("hasBrowserTrace = %v, want true: %#v", got, run)
	}
	if got := run["browserEvents"]; got != float64(2) {
		t.Fatalf("browserEvents = %v, want 2: %#v", got, run)
	}
}

func TestRunLedgerMergesFlowHistory(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.engine.TagFlowRun("history-agent", "flow-only", "http")
	base := time.Date(2026, 7, 4, 7, 0, 0, 0, time.UTC)
	s.actions = &fakeTailBackend{events: []message.Event{
		{Type: "message.in", AgentID: "history-agent", SessionID: "durable-run", Timestamp: base, Payload: message.Message{Channel: "slack"}},
		{Type: "message.out", AgentID: "history-agent", SessionID: "durable-run", Timestamp: base.Add(time.Second), Payload: message.Message{Parts: message.Text("ok")}},
	}}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/runs/ledger?agent_id=history-agent&limit=20", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("ledger status = %d body=%v", status, body)
	}
	rawRuns := body["runs"].([]any)
	if len(rawRuns) != 2 {
		t.Fatalf("runs len = %d, want 2: %#v", len(rawRuns), rawRuns)
	}
	seen := map[string]bool{}
	for _, raw := range rawRuns {
		run := raw.(map[string]any)
		id, _ := run["runId"].(string)
		seen[id] = true
	}
	if !seen["flow-only"] || !seen["durable-run"] {
		t.Fatalf("merged ledger missing flow or durable run: %#v", rawRuns)
	}
	if got := body["source"]; got != "action-log+flow" {
		t.Fatalf("source = %v, want action-log+flow", got)
	}
}

func TestRunLedgerAutomationScopeIncludesScheduledAndManualAutomationRuns(t *testing.T) {
	s := newTestGateway(t, "secret")
	cronAgent := `{"id":"daily-brief","name":"Daily Brief","trigger":"cron","channels":[],"llm":{"provider":"test","model":"m"},"system_prompt":"brief","enabled":true,"schedule":{"cron":"0 8 * * *"}}`
	if status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", cronAgent); status != http.StatusCreated {
		t.Fatalf("create scheduled agent: status=%d body=%v", status, body)
	}
	chatAgent := `{"id":"chat-only","name":"Chat Only","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"m"},"system_prompt":"chat","enabled":true}`
	if status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", chatAgent); status != http.StatusCreated {
		t.Fatalf("create chat agent: status=%d body=%v", status, body)
	}

	base := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	s.actions = &fakeTailBackend{events: []message.Event{
		{Type: "message.in", AgentID: "daily-brief", SessionID: "cron-1", Timestamp: base, Payload: message.Message{Metadata: map[string]string{"trigger": "cron"}, Parts: message.Text("__trigger:cron__")}},
		{Type: "message.out", AgentID: "daily-brief", SessionID: "cron-1", Timestamp: base.Add(time.Second), Payload: message.Message{Parts: message.Text("scheduled output")}},
		{Type: "message.in", AgentID: "daily-brief", SessionID: "manual-1", Timestamp: base.Add(time.Minute), Payload: message.Message{Channel: "http", Parts: message.Text("__trigger:manual__")}},
		{Type: "message.out", AgentID: "daily-brief", SessionID: "manual-1", Timestamp: base.Add(time.Minute + time.Second), Payload: message.Message{Parts: message.Text("manual output")}},
		{Type: "message.in", AgentID: "daily-brief", SessionID: "chat-1", Timestamp: base.Add(2 * time.Minute), Payload: message.Message{Channel: "http", Parts: message.Text("ordinary chat")}},
		{Type: "message.out", AgentID: "daily-brief", SessionID: "chat-1", Timestamp: base.Add(2*time.Minute + time.Second), Payload: message.Message{Parts: message.Text("chat output")}},
		{Type: "message.in", AgentID: "chat-only", SessionID: "manual-chat", Timestamp: base.Add(3 * time.Minute), Payload: message.Message{Channel: "http", Parts: message.Text("__trigger:manual__")}},
		{Type: "message.out", AgentID: "chat-only", SessionID: "manual-chat", Timestamp: base.Add(3*time.Minute + time.Second), Payload: message.Message{Parts: message.Text("not an automation")}},
		{Type: "schedule.run_failed", AgentID: "daily-brief", SessionID: "cron-failed", Timestamp: base.Add(4 * time.Minute), Payload: map[string]any{"trigger": "cron", "error": "provider unavailable"}},
	}}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/runs/ledger?scope=automation&limit=20", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("automation ledger status = %d body=%v", status, body)
	}
	if got := body["scope"]; got != "automation" {
		t.Fatalf("scope = %v, want automation", got)
	}
	rawRuns := body["runs"].([]any)
	if len(rawRuns) != 3 {
		t.Fatalf("automation runs len = %d, want scheduled + manual + failed scheduled: %#v", len(rawRuns), rawRuns)
	}
	bySession := map[string]map[string]any{}
	for _, raw := range rawRuns {
		run := raw.(map[string]any)
		bySession[run["sessionId"].(string)] = run
	}
	if bySession["cron-1"]["trigger"] != "cron" || bySession["manual-1"]["trigger"] != "manual" {
		t.Fatalf("scheduled/manual origins not retained: %#v", bySession)
	}
	if bySession["cron-failed"]["status"] != "failed" || bySession["cron-failed"]["error"] != "provider unavailable" {
		t.Fatalf("scheduled failure not retained: %#v", bySession["cron-failed"])
	}
	if _, ok := bySession["chat-1"]; ok {
		t.Fatalf("ordinary chat run leaked into automation history: %#v", bySession["chat-1"])
	}
	if _, ok := bySession["manual-chat"]; ok {
		t.Fatalf("manual run of non-automation agent leaked into history: %#v", bySession["manual-chat"])
	}
}

func TestRunLedgerRejectsUnknownScope(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/runs/ledger?scope=other", "secret", "")
	if status != http.StatusBadRequest {
		t.Fatalf("unknown scope status = %d body=%v", status, body)
	}
}
