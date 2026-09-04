package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestSessionSearchBuiltinFindsRelevantPriorRuns(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetActionLogBackend(&fakeSessionSearchActionLog{events: []message.Event{
		sessionSearchEvent("agent-a", "s-old", "message.in", message.Message{
			AgentID: "agent-a", SessionID: "s-old", Channel: "slack", Parts: message.Text("add the CIO article to the notebook queue"),
		}),
		sessionSearchEvent("agent-a", "s-old", "tool.call", map[string]any{"name": "queue_put"}),
		sessionSearchEvent("agent-a", "s-old", "message.out", message.Message{
			AgentID: "agent-a", SessionID: "s-old", Channel: "slack", Parts: message.Text("Captured. URL queued for daily processing."),
		}),
		sessionSearchEvent("agent-a", "s-new", "message.in", message.Message{
			AgentID: "agent-a", SessionID: "s-new", Channel: "telegram", Parts: message.Text("what is the weather in Austin"),
		}),
		sessionSearchEvent("agent-a", "s-new", "message.out", message.Message{
			AgentID: "agent-a", SessionID: "s-new", Channel: "telegram", Parts: message.Text("It is hot and dry."),
		}),
	}})

	tool := builtinByName(t, e.buildBuiltins(), "session_search")
	ctx := context.WithValue(context.Background(), inboundMsgKey{}, message.Message{AgentID: "agent-a"})
	out, err := tool.Handler(ctx, map[string]any{"query": "notebook queue", "limit": 5})
	if err != nil {
		t.Fatalf("session_search returned error: %v", err)
	}

	var got struct {
		AgentID string `json:"agent_id"`
		Count   int    `json:"count"`
		Results []struct {
			SessionID string   `json:"session_id"`
			Channel   string   `json:"channel"`
			UserText  string   `json:"user_text"`
			ReplyText string   `json:"reply_text"`
			Tools     []string `json:"tools"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("session_search output is not JSON: %v\n%s", err, out)
	}
	if got.AgentID != "agent-a" || got.Count != 1 || len(got.Results) != 1 {
		t.Fatalf("unexpected summary: %+v", got)
	}
	r := got.Results[0]
	if r.SessionID != "s-old" || r.Channel != "slack" {
		t.Fatalf("unexpected result route: %+v", r)
	}
	if !strings.Contains(r.UserText, "notebook queue") || !strings.Contains(r.ReplyText, "queued") {
		t.Fatalf("unexpected text fields: %+v", r)
	}
	if len(r.Tools) != 1 || r.Tools[0] != "queue_put" {
		t.Fatalf("tools = %+v, want queue_put", r.Tools)
	}
}

func TestSessionSearchBuiltinRequiresAgentContextOrArgument(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetActionLogBackend(&fakeSessionSearchActionLog{})
	tool := builtinByName(t, e.buildBuiltins(), "session_search")

	_, err := tool.Handler(context.Background(), map[string]any{"query": "anything"})
	if err == nil || !strings.Contains(err.Error(), "agent_id is required") {
		t.Fatalf("err = %v, want missing agent id", err)
	}
}

func sessionSearchEvent(agentID, sessionID, typ string, payload any) message.Event {
	return message.Event{
		Type:      typ,
		AgentID:   agentID,
		SessionID: sessionID,
		Payload:   payload,
		Timestamp: time.Now().UTC(),
	}
}

type fakeSessionSearchActionLog struct {
	events []message.Event
}

func (f *fakeSessionSearchActionLog) Append(message.Event) {}

func (f *fakeSessionSearchActionLog) Tail(agentID string, limit int) ([]message.Event, error) {
	out := make([]message.Event, 0, len(f.events))
	for _, ev := range f.events {
		if ev.AgentID == agentID {
			out = append(out, ev)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (f *fakeSessionSearchActionLog) EventFilePath(string) string { return "" }

func (f *fakeSessionSearchActionLog) IncompleteMessageIns(time.Time) ([][]byte, error) {
	return nil, nil
}

func (f *fakeSessionSearchActionLog) CountMessageInAttempts(string, string, time.Time) (int, error) {
	return 0, nil
}

func (f *fakeSessionSearchActionLog) MarkDeadLetter(string, string, string) error { return nil }

func (f *fakeSessionSearchActionLog) Close() error { return nil }
