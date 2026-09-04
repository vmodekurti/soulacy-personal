package scheduler

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

type captureSink struct{ events []message.Event }

func (c *captureSink) Emit(ev message.Event) { c.events = append(c.events, ev) }

func lastSchedEvent(t *testing.T, sink *captureSink) map[string]any {
	t.Helper()
	for i := len(sink.events) - 1; i >= 0; i-- {
		if sink.events[i].Type == "schedule.output" {
			m, ok := sink.events[i].Payload.(map[string]any)
			if !ok {
				t.Fatalf("schedule.output payload not a map: %T", sink.events[i].Payload)
			}
			return m
		}
	}
	t.Fatalf("no schedule.output event emitted")
	return nil
}

// HasScheduledOutputTarget reflects whether the agent has a resolvable target.
func TestHasScheduledOutputTarget(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	withOut := &agent.Definition{ID: "a", Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{Output: &agent.ScheduleOutput{Channel: "telegram", To: "123"}}}
	if !s.HasScheduledOutputTarget(withOut) {
		t.Fatalf("expected target for agent with schedule.output")
	}
	noOut := &agent.Definition{ID: "b", Trigger: agent.TriggerCron, Schedule: &agent.Schedule{}}
	if s.HasScheduledOutputTarget(noOut) {
		t.Fatalf("did not expect a target for agent without output")
	}
}

// DeliverScheduledOutput (used by the manual-trigger path) publishes and reports
// exactly like a cron fire.
func TestDeliverScheduledOutput_ManualDelivers(t *testing.T) {
	reg := channels.NewRegistry(1)
	reg.Register(&captureAdapter{id: "telegram"})
	sink := &captureSink{}
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.SetChannelRegistry(reg)
	s.SetEventSink(sink)

	def := &agent.Definition{ID: "brief", Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{Output: &agent.ScheduleOutput{Channel: "telegram", To: "123"}}}
	s.DeliverScheduledOutput(context.Background(), def, message.Message{}, "manual result", "manual")

	ev := lastSchedEvent(t, sink)
	if ev["delivered"] != true {
		t.Fatalf("manual delivery should have delivered, got %v", ev)
	}
}

// A delivered scheduled reply emits a schedule.output event with delivered=true.
func TestScheduledDelivery_EmitsDeliveredEvent(t *testing.T) {
	reg := channels.NewRegistry(1)
	reg.Register(&captureAdapter{id: "telegram"})
	sink := &captureSink{}

	s := New(nil, nil, zap.NewNop(), context.Background())
	s.SetChannelRegistry(reg)
	s.SetEventSink(sink)

	def := &agent.Definition{ID: "brief", Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{Output: &agent.ScheduleOutput{Channel: "telegram", To: "123"}}}
	s.sendScheduledOutput(context.Background(), def, message.Message{}, "the result", "cron", nil)

	ev := lastSchedEvent(t, sink)
	if ev["delivered"] != true || ev["reason"] != deliveryDelivered {
		t.Fatalf("expected delivered event, got %v", ev)
	}
}

// A cron reply with no output target must NOT vanish silently: it emits an
// undelivered schedule.output event with a clear reason and the reply preview.
func TestScheduledDelivery_UndeliveredEmitsEvent(t *testing.T) {
	reg := channels.NewRegistry(1)
	reg.Register(&captureAdapter{id: "telegram"})
	sink := &captureSink{}

	s := New(nil, nil, zap.NewNop(), context.Background())
	s.SetChannelRegistry(reg)
	s.SetEventSink(sink)

	// Cron agent, no schedule.output, no default channel → nowhere to deliver.
	def := &agent.Definition{ID: "orphan", Trigger: agent.TriggerCron}
	s.sendScheduledOutput(context.Background(), def, message.Message{}, "important result", "cron", nil)

	ev := lastSchedEvent(t, sink)
	if ev["delivered"] != false || ev["reason"] != deliveryNoOutput {
		t.Fatalf("expected undelivered event, got %v", ev)
	}
	if p, _ := ev["reply_preview"].(string); !strings.Contains(p, "important result") {
		t.Fatalf("event should carry the reply preview, got %v", ev["reply_preview"])
	}
}

// When the agent has no output but a single default outbound channel exists, the
// result is routed to that fallback (with a notice) instead of being lost.
func TestScheduledDelivery_FallbackToSingleDefault(t *testing.T) {
	reg := channels.NewRegistry(1)
	adapter := &captureAdapter{id: "telegram"}
	reg.Register(adapter)
	sink := &captureSink{}

	s := New(nil, nil, zap.NewNop(), context.Background())
	s.SetChannelRegistry(reg)
	s.SetEventSink(sink)
	s.SetDefaultOutputs(map[string]agent.ScheduleOutput{
		"telegram": {Channel: "telegram", To: "999"},
	})

	// Agent lists no channels and no output → primary resolve fails → fallback.
	def := &agent.Definition{ID: "orphan", Trigger: agent.TriggerCron}
	s.sendScheduledOutput(context.Background(), def, message.Message{}, "brief body", "cron", nil)

	if len(adapter.sent) != 1 {
		t.Fatalf("expected 1 fallback delivery, got %d", len(adapter.sent))
	}
	if adapter.sent[0].ThreadID != "999" {
		t.Fatalf("fallback should go to default destination, got %q", adapter.sent[0].ThreadID)
	}
	if txt := firstText(adapter.sent[0]); !strings.Contains(txt, "no delivery target") || !strings.Contains(txt, "brief body") {
		t.Fatalf("fallback message should note the routing + include the reply: %q", txt)
	}
	ev := lastSchedEvent(t, sink)
	if ev["delivered"] != true || ev["fallback"] != true || ev["reason"] != deliveryViaFallback {
		t.Fatalf("expected fallback-delivered event, got %v", ev)
	}
}

func TestSingleDefaultOutput(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	if _, ok := s.singleDefaultOutput(); ok {
		t.Fatalf("no defaults → not ok")
	}
	s.SetDefaultOutputs(map[string]agent.ScheduleOutput{"telegram": {Channel: "telegram", To: "1"}})
	if o, ok := s.singleDefaultOutput(); !ok || o.To != "1" {
		t.Fatalf("single default should resolve, got %v %v", o, ok)
	}
	s.SetDefaultOutputs(map[string]agent.ScheduleOutput{
		"telegram": {Channel: "telegram", To: "1"},
		"slack":    {Channel: "slack", To: "2"},
	})
	if _, ok := s.singleDefaultOutput(); ok {
		t.Fatalf("ambiguous (2) defaults → not ok")
	}
}
