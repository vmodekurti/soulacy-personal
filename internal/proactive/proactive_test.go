package proactive

import (
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

func msgIn(agent, channel string) message.Event {
	return message.Event{
		Type: "message.in", AgentID: agent, Timestamp: time.Now(),
		Payload: map[string]any{"channel": channel, "text": "hi"},
	}
}

func errEv(agent string) message.Event {
	return message.Event{Type: "error", AgentID: agent, Timestamp: time.Now(), Payload: map[string]any{"error": "boom"}}
}

func TestDetect_SuggestsScheduleForRepeatedManualRuns(t *testing.T) {
	events := []message.Event{msgIn("a", "http"), msgIn("a", "http"), msgIn("a", "chat")}
	agents := map[string]AgentSnapshot{"a": {ID: "a", Name: "Screener", HasSchedule: false, LearningEnabled: true}}

	got := Detect(events, agents)
	if !hasKind(got, "schedule", "a") {
		t.Fatalf("expected a schedule suggestion, got %+v", got)
	}
}

func TestDetect_NoScheduleWhenAlreadyScheduled(t *testing.T) {
	events := []message.Event{msgIn("a", "http"), msgIn("a", "http"), msgIn("a", "http")}
	agents := map[string]AgentSnapshot{"a": {ID: "a", HasSchedule: true, LearningEnabled: true}}
	if hasKind(Detect(events, agents), "schedule", "a") {
		t.Fatalf("should not suggest scheduling an already-scheduled agent")
	}
}

func TestDetect_SuggestsReviewOnFailures(t *testing.T) {
	events := []message.Event{errEv("b"), errEv("b")}
	got := Detect(events, map[string]AgentSnapshot{"b": {ID: "b", Name: "Flaky"}})
	if !hasKind(got, "review", "b") {
		t.Fatalf("expected a review suggestion, got %+v", got)
	}
}

func TestDetect_SuggestsEnableLearning(t *testing.T) {
	events := []message.Event{msgIn("c", "http"), msgIn("c", "http"), msgIn("c", "http")}
	agents := map[string]AgentSnapshot{"c": {ID: "c", HasSchedule: true, LearningEnabled: false}}
	if !hasKind(Detect(events, agents), "enable_learning", "c") {
		t.Fatalf("expected an enable_learning suggestion")
	}
}

func TestDetect_OrdersByScore(t *testing.T) {
	// b has failures (higher score) than a's manual-run schedule suggestion.
	events := []message.Event{
		msgIn("a", "http"), msgIn("a", "http"), msgIn("a", "http"),
		errEv("b"), errEv("b"), errEv("b"),
	}
	agents := map[string]AgentSnapshot{
		"a": {ID: "a", LearningEnabled: true},
		"b": {ID: "b", HasSchedule: true, LearningEnabled: true},
	}
	got := Detect(events, agents)
	if len(got) == 0 || got[0].Kind != "review" {
		t.Fatalf("expected review suggestion first (highest score), got %+v", got)
	}
}

func hasKind(s []Suggestion, kind, agent string) bool {
	for _, x := range s {
		if x.Kind == kind && x.AgentID == agent {
			return true
		}
	}
	return false
}
