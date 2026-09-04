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

func TestMarkDegradedReply(t *testing.T) {
	// A confident run is delivered verbatim — and so is a reply with no
	// reasoning metadata at all (non-reasoning agents must be untouched).
	for _, meta := range []map[string]string{
		nil,
		{},
		{message.MetaReasoningDegraded: "false"},
	} {
		got, degraded := MarkDegradedReply("the brief", meta)
		if got != "the brief" || degraded {
			t.Errorf("meta %v: got (%q,%v), want the text unchanged", meta, got, degraded)
		}
	}

	got, degraded := MarkDegradedReply("partial notes", map[string]string{
		message.MetaReasoningDegraded: "true",
		message.MetaReasoningSteps:    "7",
	})
	if !degraded {
		t.Fatal("expected the reply to be marked degraded")
	}
	if !strings.HasPrefix(got, "⚠️") {
		t.Errorf("degraded reply should lead with the notice: %q", got)
	}
	if !strings.Contains(got, "7 step(s) recorded") {
		t.Errorf("notice should carry the step count: %q", got)
	}
	if !strings.HasSuffix(got, "partial notes") {
		t.Errorf("original text must be preserved: %q", got)
	}
}

func TestScheduledDelivery_MarksDegradedRun(t *testing.T) {
	reg := channels.NewRegistry(1)
	adapter := &captureAdapter{id: "telegram"}
	reg.Register(adapter)
	sink := &captureSink{}
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.SetChannelRegistry(reg)
	s.SetEventSink(sink)

	def := &agent.Definition{ID: "brief", Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{Output: &agent.ScheduleOutput{Channel: "telegram", To: "123"}}}

	s.DeliverScheduledReply(context.Background(), def, message.Message{},
		"Looking at the trace, I need to continue from where the pipeline broke.",
		"cron",
		map[string]string{message.MetaReasoningDegraded: "true", message.MetaReasoningSteps: "7"})

	if len(adapter.sent) != 1 {
		t.Fatalf("expected one delivered message, got %d", len(adapter.sent))
	}
	// The user must be able to tell working notes from a finished brief.
	if !strings.Contains(firstText(adapter.sent[0]), "did not complete cleanly") {
		t.Errorf("degraded reply reached the channel unmarked: %q", firstText(adapter.sent[0]))
	}
	ev := lastSchedEvent(t, sink)
	if ev["degraded"] != true {
		t.Errorf("schedule.output should report degraded=true, got %v", ev["degraded"])
	}
	if ev["delivered"] != true {
		t.Errorf("marking must not suppress delivery, got %v", ev["delivered"])
	}
}

func TestScheduledDelivery_ConfidentRunIsUnmarked(t *testing.T) {
	reg := channels.NewRegistry(1)
	adapter := &captureAdapter{id: "telegram"}
	reg.Register(adapter)
	sink := &captureSink{}
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.SetChannelRegistry(reg)
	s.SetEventSink(sink)

	def := &agent.Definition{ID: "brief", Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{Output: &agent.ScheduleOutput{Channel: "telegram", To: "123"}}}

	s.DeliverScheduledReply(context.Background(), def, message.Message{},
		"## AI Articles Podcast Briefing\n\nThree pieces this week.", "cron", nil)

	if len(adapter.sent) != 1 {
		t.Fatalf("expected one delivered message, got %d", len(adapter.sent))
	}
	if strings.Contains(firstText(adapter.sent[0]), "did not complete cleanly") {
		t.Error("a confident run must not be marked degraded")
	}
	if ev := lastSchedEvent(t, sink); ev["degraded"] != false {
		t.Errorf("schedule.output should report degraded=false, got %v", ev["degraded"])
	}
}

func TestMarkDegradedReply_OutcomeSpecificNotice(t *testing.T) {
	// A run whose BUSINESS contract went unmet must not be described as a
	// tool failure — every node may have executed fine, and a message about
	// "a tool failed" would send the reader looking in the wrong place.
	got, degraded := MarkDegradedReply("Here is your brief.", map[string]string{
		message.MetaReasoningDegraded: "true",
		message.MetaOutcome:           "empty",
		message.MetaOutcomeSummary:    "three sources were added — 0 items",
	})
	if !degraded {
		t.Fatal("an unmet outcome contract must mark the reply degraded")
	}
	if !strings.Contains(got, "produced nothing") {
		t.Errorf("empty outcome should say so plainly: %q", got)
	}
	if strings.Contains(got, "a tool failed") {
		t.Errorf("must not blame a tool failure: %q", got)
	}
	if !strings.Contains(got, "three sources were added") {
		t.Errorf("notice should carry the author's own words: %q", got)
	}
	if !strings.HasSuffix(got, "Here is your brief.") {
		t.Errorf("the original text must be preserved: %q", got)
	}

	// Each outcome class reads differently.
	for outcome, want := range map[string]string{
		"partial": "only partly achieved",
		"failed":  "did not achieve",
	} {
		got, _ := MarkDegradedReply("x", map[string]string{
			message.MetaReasoningDegraded: "true",
			message.MetaOutcome:           outcome,
		})
		if !strings.Contains(got, want) {
			t.Errorf("outcome %q should read %q: %q", outcome, want, got)
		}
	}

	// Without outcome metadata the original generic notice still applies.
	generic, _ := MarkDegradedReply("x", map[string]string{
		message.MetaReasoningDegraded: "true",
		message.MetaReasoningSteps:    "7",
	})
	if !strings.Contains(generic, "did not complete cleanly") {
		t.Errorf("a non-outcome degraded run keeps the generic notice: %q", generic)
	}
}
