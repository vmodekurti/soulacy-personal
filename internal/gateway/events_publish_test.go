package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/events"
	"github.com/soulacy/soulacy/internal/queue"
	queuememory "github.com/soulacy/soulacy/internal/queue/memory"
	"github.com/soulacy/soulacy/pkg/message"
)

// queueEnvelopes subscribes to all soulacy events on q and returns a channel
// of decoded envelopes.
func queueEnvelopes(t *testing.T, q queue.Backend) <-chan map[string]any {
	t.Helper()
	out := make(chan map[string]any, 16)
	_, err := q.Subscribe(context.Background(), "soulacy.events.>", "",
		func(m *queue.Message) {
			var env map[string]any
			if json.Unmarshal(m.Data, &env) == nil {
				out <- env
			}
			_ = m.Ack()
		})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	return out
}

func TestEventHub_ForwardsToQueuePublisher(t *testing.T) {
	q := queuememory.New()
	t.Cleanup(func() { q.Close() })
	pub := events.NewPublisher(q, zap.NewNop())
	t.Cleanup(func() { pub.Close() })

	hub := NewEventHub(zap.NewNop(), nil)
	hub.SetEventPublisher(pub)
	envelopes := queueEnvelopes(t, q)

	hub.Emit(message.Event{
		Type: "tool.call", AgentID: "bot", SessionID: "s9",
		Payload: map[string]any{"name": "web_search"},
	})

	select {
	case env := <-envelopes:
		if env["type"] != "tool.call" || env["agent_id"] != "bot" || env["schema"] != float64(1) {
			t.Errorf("envelope = %v", env)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("EventHub.Emit never reached the queue")
	}
}

func TestEventHub_FiltersEventsByPrincipal(t *testing.T) {
	hub := NewEventHub(zap.NewNop(), nil)
	hub.SetEventAuthorizer(func(p eventPrincipal, event message.Event) bool {
		return p.Admin || p.Principal == "viewer:"+event.SessionID
	})
	alice := &wsClient{send: make(chan []byte, 1), principal: eventPrincipal{Principal: "viewer:alice", Authenticated: true}}
	bob := &wsClient{send: make(chan []byte, 1), principal: eventPrincipal{Principal: "viewer:bob", Authenticated: true}}
	admin := &wsClient{send: make(chan []byte, 1), principal: eventPrincipal{Admin: true, Authenticated: true}}
	hub.clients[alice] = struct{}{}
	hub.clients[bob] = struct{}{}
	hub.clients[admin] = struct{}{}

	hub.Emit(message.Event{Type: "tool.call", SessionID: "alice", Payload: "private"})
	select {
	case <-alice.send:
	default:
		t.Fatal("owner did not receive event")
	}
	select {
	case <-admin.send:
	default:
		t.Fatal("admin did not receive event")
	}
	select {
	case leaked := <-bob.send:
		t.Fatalf("event leaked to another principal: %s", leaked)
	default:
	}
}

func TestWorkboardRun_EmitsLifecycleEvents(t *testing.T) {
	s := newTestGatewayWithWorkboard(t)

	q := queuememory.New()
	t.Cleanup(func() { q.Close() })
	pub := events.NewPublisher(q, zap.NewNop())
	t.Cleanup(func() { pub.Close() })
	hub := NewEventHub(zap.NewNop(), nil)
	hub.SetEventPublisher(pub)
	s.hub = hub

	envelopes := queueEnvelopes(t, q)

	// Ghost agent → run fails; we should see run.started then run.failed.
	created := wbCreate(t, s, `{"title":"observable","agent_id":"ghost-agent"}`)
	id := fmt.Sprintf("%v", created["id"])
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/workboard/tasks/"+id+"/run", "secret", "")
	if status != http.StatusAccepted {
		t.Fatalf("run status = %d", status)
	}

	var got []map[string]any
	deadline := time.After(5 * time.Second)
	for len(got) < 2 {
		select {
		case env := <-envelopes:
			if t, _ := env["type"].(string); t == "run.started" || t == "run.failed" || t == "run.finished" {
				got = append(got, env)
			}
		case <-deadline:
			t.Fatalf("only saw %d run lifecycle events: %v", len(got), got)
		}
	}

	if got[0]["type"] != "run.started" {
		t.Errorf("first event = %v, want run.started", got[0]["type"])
	}
	if got[1]["type"] != "run.failed" {
		t.Errorf("second event = %v, want run.failed", got[1]["type"])
	}
	for _, env := range got {
		if env["agent_id"] != "ghost-agent" {
			t.Errorf("agent_id = %v", env["agent_id"])
		}
		data, _ := env["data"].(map[string]any)
		if data["task_id"] == nil || data["run_id"] == nil || data["attempt"] == nil {
			t.Errorf("run event data missing task/run/attempt: %v", env)
		}
	}
	failData, _ := got[1]["data"].(map[string]any)
	if reason, _ := failData["failure_reason"].(string); reason == "" {
		t.Errorf("run.failed should carry failure_reason: %v", got[1])
	}
}
