// eventprojection_redaction_test.go — what a WebSocket client can read.
//
// Asserted through project(), the function whose output is marshalled and
// broadcast, rather than through a fake socket: the socket path adds an
// authorization gate that would make the test pass for the wrong reason — a
// client that receives nothing leaks nothing.
package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestAWebSocketClientNeverReceivesToolCredentials(t *testing.T) {
	wire, err := json.Marshal(project(message.Event{
		Type:        "tool.call",
		WorkspaceID: "ws-a",
		AgentID:     "fetcher",
		SessionID:   "s1",
		Payload: message.ToolCall{
			ID:   "call-1",
			Name: "http_request",
			Arguments: map[string]any{
				"url":      "https://api.example.com/orders",
				"password": "hunter2-LEAKED-IF-VISIBLE",
				"headers":  map[string]any{"Authorization": "Bearer LEAKED-IF-VISIBLE"},
			},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "LEAKED-IF-VISIBLE") {
		t.Errorf("a credential reached the WebSocket wire:\n%s", wire)
	}
	// The client renders these; losing them breaks the activity view.
	for _, want := range []string{"http_request", "api.example.com", "ws-a", "fetcher"} {
		if !strings.Contains(string(wire), want) {
			t.Errorf("the projection lost %q, so the UI can no longer show the call:\n%s", want, wire)
		}
	}
}

func TestTheLiveStreamAndTheHistoryNowAgree(t *testing.T) {
	// The bug this closes was not that one surface leaked in isolation; it was
	// that the SAME tool call read two different ways depending on which
	// surface the operator looked at. The action log redacted on write, so
	// history showed [REDACTED] while the live stream showed the secret. A
	// difference like that reads as a display bug, and the fix somebody
	// reaches for is to make history match the stream.
	event := message.Event{
		Type:    "tool.result",
		AgentID: "a",
		Payload: map[string]any{"token": "t-LEAKED-IF-VISIBLE", "status": 200},
	}

	live, err := json.Marshal(project(event))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(live), "LEAKED-IF-VISIBLE") {
		t.Fatalf("live stream still carries the secret:\n%s", live)
	}
	if !strings.Contains(string(live), "200") {
		t.Errorf("the non-secret sibling field was dropped too:\n%s", live)
	}
}
