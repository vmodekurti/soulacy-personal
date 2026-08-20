// envelope_redaction_test.go — what a broker subscriber can read.
//
// The queue is the WIDEST audience an event has. WebSocket clients pass the
// gateway's per-workspace authorization; NATS subscribers are outside the
// process entirely, frequently a different team's log pipeline, and subscribe
// with "soulacy.events.>" to everything.
//
// The test asserts on the ENVELOPE rather than on a stubbed backend because
// NewEnvelope is the only way an envelope is constructed: a publisher added
// tomorrow gets the redaction whether or not its author knew to ask for it,
// and this test is what says so.
package events

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestABrokerSubscriberNeverReceivesToolCredentials(t *testing.T) {
	// The real shape: an agent calling an HTTP tool. The arguments are the
	// model's, so nothing upstream can promise they hold no credential —
	// which is exactly why the boundary, not the caller, has to redact.
	env := NewEnvelope(message.Event{
		Type:    "tool.call",
		AgentID: "fetcher",
		Payload: message.ToolCall{
			ID:   "call-1",
			Name: "http_request",
			Arguments: map[string]any{
				"url": "https://api.example.com/orders",
				"headers": map[string]any{
					"Authorization": "Bearer LEAKED-IF-VISIBLE",
					"Accept":        "application/json",
				},
				"api_key": "sk-LEAKED-IF-VISIBLE",
			},
		},
	})

	wire, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "LEAKED-IF-VISIBLE") {
		t.Errorf("a credential reached the broker envelope:\n%s", wire)
	}
	// The subscriber must still be able to tell WHAT happened, or redaction
	// has turned the event stream into noise and the next person will remove
	// it to get their observability back.
	for _, want := range []string{"tool.call", "http_request", "api.example.com"} {
		if !strings.Contains(string(wire), want) {
			t.Errorf("the envelope lost %q, so the subscriber cannot tell what happened:\n%s", want, wire)
		}
	}
}

func TestRedactingTheEnvelopeIsNotASchemaChange(t *testing.T) {
	// If redaction had changed the wire shape it would be a breaking change
	// for every existing subscriber, and somebody would revert it rather than
	// bump the schema. It does not: the same field, the same type, different
	// values inside.
	env := NewEnvelope(message.Event{Type: "run.completed", AgentID: "a", Payload: map[string]any{"ok": true}})
	if env.Schema != SchemaVersion {
		t.Errorf("schema = %d, want %d", env.Schema, SchemaVersion)
	}
	data, ok := env.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data is %T, want map[string]any — a plain payload changed shape", env.Data)
	}
	if data["ok"] != true {
		t.Errorf("a non-secret value was altered: %#v", data)
	}
}
