package events

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/queue"
	queuememory "github.com/soulacy/soulacy/internal/queue/memory"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestEnvelope_SchemaV1Shape(t *testing.T) {
	ts := time.Date(2026, 6, 6, 18, 0, 0, 0, time.UTC)
	env := NewEnvelope(message.Event{
		Type:      "run.failed",
		AgentID:   "bot",
		SessionID: "wb-1-99",
		Payload:   map[string]any{"reason": "boom"},
		Timestamp: ts,
	})

	if env.Schema != 1 {
		t.Errorf("Schema = %d, want 1", env.Schema)
	}
	if env.ID == "" {
		t.Error("ID should be generated")
	}
	if env.Type != "run.failed" || env.AgentID != "bot" || env.SessionID != "wb-1-99" {
		t.Errorf("envelope = %+v", env)
	}
	if !env.TS.Equal(ts) {
		t.Errorf("TS = %v, want event timestamp %v", env.TS, ts)
	}

	// Exact JSON keys are the public contract (docs/EVENTS.md).
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"schema", "id", "type", "agent_id", "session_id", "ts", "data"} {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON missing key %q: %s", key, raw)
		}
	}
	data, _ := m["data"].(map[string]any)
	if data["reason"] != "boom" {
		t.Errorf("data = %v", m["data"])
	}
}

func TestEnvelope_ZeroTimestampDefaultsToNow(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	env := NewEnvelope(message.Event{Type: "message.in", AgentID: "bot"})
	if env.TS.Before(before) {
		t.Errorf("TS = %v, should default to now", env.TS)
	}
}

func TestSubjectFor(t *testing.T) {
	cases := map[string]string{
		"message.in":  "soulacy.events.message.in",
		"run.failed":  "soulacy.events.run.failed",
		"tool.call":   "soulacy.events.tool.call",
		"":            "soulacy.events.unknown",
		"weird type!": "soulacy.events.weird_type_",
	}
	for in, want := range cases {
		if got := SubjectFor(in); got != want {
			t.Errorf("SubjectFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPublisher_DeliversToQueue(t *testing.T) {
	q := queuememory.New()
	t.Cleanup(func() { q.Close() })

	received := make(chan [2]string, 4) // [subject, body]
	_, err := q.Subscribe(context.Background(), "soulacy.events.>", "",
		func(m *queue.Message) {
			received <- [2]string{m.Subject, string(m.Data)}
			_ = m.Ack()
		})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	p := NewPublisher(q, zap.NewNop())
	t.Cleanup(func() { p.Close() })

	p.PublishEvent(message.Event{
		Type: "message.out", AgentID: "bot", SessionID: "s1",
		Payload: map[string]any{"ok": true},
	})

	select {
	case got := <-received:
		if got[0] != "soulacy.events.message.out" {
			t.Errorf("subject = %q", got[0])
		}
		var env map[string]any
		if err := json.Unmarshal([]byte(got[1]), &env); err != nil {
			t.Fatalf("payload not JSON: %v", err)
		}
		if env["schema"] != float64(1) || env["type"] != "message.out" || env["agent_id"] != "bot" {
			t.Errorf("envelope = %v", env)
		}
		if env["session_id"] != "s1" || env["id"] == "" || env["ts"] == nil {
			t.Errorf("envelope = %v", env)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event never reached the queue")
	}
}

// blockingBackend's Publish never returns — proves the publisher decouples
// the caller from broker latency.
type blockingBackend struct{}

func (b *blockingBackend) Publish(ctx context.Context, subject string, data []byte) error {
	<-ctx.Done()
	return ctx.Err()
}
func (b *blockingBackend) Subscribe(context.Context, string, string, func(*queue.Message)) (queue.Subscription, error) {
	return nil, nil
}
func (b *blockingBackend) Close() error { return nil }

func TestPublisher_NeverBlocksCaller(t *testing.T) {
	p := NewPublisher(&blockingBackend{}, zap.NewNop())
	t.Cleanup(func() { p.Close() })

	done := make(chan struct{})
	go func() {
		for i := 0; i < 5000; i++ { // far beyond any internal buffer
			p.PublishEvent(message.Event{Type: "tool.call", AgentID: "bot"})
		}
		close(done)
	}()
	select {
	case <-done:
		// good — caller never blocked
	case <-time.After(3 * time.Second):
		t.Fatal("PublishEvent blocked the caller")
	}
}

func TestPublisher_NilBackendIsNoop(t *testing.T) {
	p := NewPublisher(nil, zap.NewNop())
	p.PublishEvent(message.Event{Type: "message.in", AgentID: "bot"}) // must not panic
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
