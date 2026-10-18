package gateway

import (
	"github.com/soulacy/soulacy/pkg/message"
	"strings"
	"testing"
	"time"
)

func TestRunLedgerExplainsRecordedRecoveryFailure(t *testing.T) {
	s := newTestGateway(t, "secret")
	now := time.Now()
	events := []message.Event{
		{Type: "message.in", Payload: map[string]any{"channel": "cron"}},
		{Type: "tool.result", Payload: map[string]any{"name": "authenticated_fetch", "content": "article text", "is_error": false}},
		{Type: "warn", Payload: map[string]any{"stage": "required-tool-recovery", "required_tool": "start_website_action", "error": "could not derive schema-valid arguments: invalid JSON: unexpected end of JSON input"}},
		{Type: "task.contract.completed", Payload: map[string]any{"state": "blocked", "blocker": "mission acceptance checks failed: Open the approved interactive website session: required tool was not observed"}},
		{Type: "run.completed", Payload: map[string]any{"success": false}},
		{Type: "schedule.output", Payload: map[string]any{"delivered": true, "channel": "mobile"}},
	}
	for i := range events {
		events[i].AgentID = "podcast"
		events[i].SessionID = "failed-podcast"
		events[i].Timestamp = now.Add(time.Duration(i) * time.Second)
	}
	rows := s.buildRunLedger(events, 0)
	if len(rows) != 1 || rows[0].Failure == nil {
		t.Fatalf("%#v", rows)
	}
	d := rows[0].Failure
	if d.Step != "start_website_action" || !strings.Contains(d.Detail, "invalid JSON") || d.NextAction == "" || len(d.Checks) != 1 {
		t.Fatalf("%#v", d)
	}
	if rows[0].DeliveryStatus != "delivered" || rows[0].Status != "failed" {
		t.Fatal("delivery and execution must remain separate")
	}
	if !runLedgerEventTypes()["warn"] {
		t.Fatal("recovery evidence excluded from durable ledger")
	}
}

func TestSuccessfulRunDoesNotShowRecoveredFailure(t *testing.T) {
	events := []message.Event{{Type: "tool.result", Payload: map[string]any{"name": "fetch", "is_error": true, "content": "timeout"}}}
	if diagnoseRunFailure(events, "success", "", "") != nil {
		t.Fatal("successful run displayed as failure")
	}
}
