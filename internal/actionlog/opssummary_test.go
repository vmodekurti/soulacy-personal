package actionlog

import (
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestOpsSummaryAggregatesRunsAndFailures(t *testing.T) {
	l := newLogger(t)
	base := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	for _, ev := range []message.Event{
		{Type: "message.in", AgentID: "alpha", SessionID: "ok-1", Timestamp: base},
		{Type: "tool.call", AgentID: "alpha", SessionID: "ok-1", Timestamp: base.Add(time.Second)},
		{Type: "message.out", AgentID: "alpha", SessionID: "ok-1", Timestamp: base.Add(2 * time.Second)},
		{Type: "message.in", AgentID: "alpha", SessionID: "bad-1", Timestamp: base.Add(3 * time.Second)},
		{Type: "error", AgentID: "alpha", SessionID: "bad-1", Timestamp: base.Add(4 * time.Second), Payload: map[string]any{"error": "provider 502 exploded"}},
		{Type: "message.in", AgentID: "beta", SessionID: "hung-1", Timestamp: base.Add(5 * time.Second)},
		{Type: "message.in", AgentID: "beta", SessionID: "bad-2", Timestamp: base.Add(6 * time.Second)},
		{Type: "error", AgentID: "beta", SessionID: "bad-2", Timestamp: base.Add(7 * time.Second), Payload: map[string]any{"error": "provider 502 exploded"}},
	} {
		l.Append(ev)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := l.OpsSummary(base.Add(-time.Hour), "1h", 10)
		if err == nil && got.TotalRuns == 4 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	got, err := l.OpsSummary(base.Add(-time.Hour), "1h", 10)
	if err != nil {
		t.Fatalf("OpsSummary: %v", err)
	}
	if got.TotalRuns != 4 || got.SuccessfulRuns != 1 || got.FailedRuns != 2 || got.IncompleteRuns != 1 {
		t.Fatalf("run totals = %#v", got)
	}
	if got.ToolCalls != 1 {
		t.Fatalf("tool_calls = %d, want 1", got.ToolCalls)
	}
	if got.FailureRate != 0.5 {
		t.Fatalf("failure_rate = %v, want 0.5", got.FailureRate)
	}
	if got.IncompleteRate != 0.25 {
		t.Fatalf("incomplete_rate = %v, want 0.25", got.IncompleteRate)
	}
	if got.AvgDurationMS != 1000 || got.P95DurationMS != 2000 || got.MaxDurationMS != 2000 {
		t.Fatalf("duration rollup = avg %d p95 %d max %d", got.AvgDurationMS, got.P95DurationMS, got.MaxDurationMS)
	}
	if len(got.TopFailing) != 2 || got.TopFailing[0].Failures != 1 || got.TopFailing[1].Failures != 1 {
		t.Fatalf("top failing = %#v", got.TopFailing)
	}
	if len(got.TopErrors) != 1 || got.TopErrors[0].Count != 2 || got.TopErrors[0].Message != "provider 502 exploded" {
		t.Fatalf("top errors = %#v", got.TopErrors)
	}
	if len(got.RecentFailures) != 2 || got.RecentFailures[0].SessionID != "bad-2" {
		t.Fatalf("recent failures = %#v", got.RecentFailures)
	}
}

func TestQueryEventsSupportsAllAgentsSessionAndTypes(t *testing.T) {
	l := newLogger(t)
	base := time.Date(2026, 7, 4, 13, 0, 0, 0, time.UTC)
	for _, ev := range []message.Event{
		{Type: "message.in", AgentID: "alpha", SessionID: "a-1", Timestamp: base, Payload: map[string]any{"text": "alpha start"}},
		{Type: "tool.call", AgentID: "alpha", SessionID: "a-1", Timestamp: base.Add(time.Second), Payload: map[string]any{"name": "tool_a"}},
		{Type: "message.out", AgentID: "alpha", SessionID: "a-1", Timestamp: base.Add(2 * time.Second), Payload: map[string]any{"text": "alpha done"}},
		{Type: "message.in", AgentID: "beta", SessionID: "b-1", Timestamp: base.Add(3 * time.Second), Payload: map[string]any{"text": "beta start"}},
		{Type: "error", AgentID: "beta", SessionID: "b-1", Timestamp: base.Add(4 * time.Second), Payload: map[string]any{"error": "beta failed"}},
	} {
		l.Append(ev)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := l.QueryEvents("", "", 10, nil)
		if err == nil && len(got) == 5 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	all, err := l.QueryEvents("", "", 10, nil)
	if err != nil {
		t.Fatalf("QueryEvents all: %v", err)
	}
	if len(all) != 5 || all[0].AgentID != "alpha" || all[4].AgentID != "beta" {
		t.Fatalf("all events = %#v", all)
	}

	session, err := l.QueryEvents("", "b-1", 10, nil)
	if err != nil {
		t.Fatalf("QueryEvents session: %v", err)
	}
	if len(session) != 2 || session[0].AgentID != "beta" || session[1].Type != "error" {
		t.Fatalf("session events = %#v", session)
	}

	filtered, err := l.QueryEvents("", "", 10, map[string]bool{"message.in": true, "error": true})
	if err != nil {
		t.Fatalf("QueryEvents filtered: %v", err)
	}
	if len(filtered) != 3 || filtered[0].Type != "message.in" || filtered[2].Type != "error" {
		t.Fatalf("filtered events = %#v", filtered)
	}
}

func TestQueryEventsHonorsLargeRunLedgerWindow(t *testing.T) {
	l := newLogger(t)
	base := time.Date(2026, 7, 4, 14, 0, 0, 0, time.UTC)
	for i := 0; i < 1200; i++ {
		l.Append(message.Event{
			Type:      "message.in",
			AgentID:   "alpha",
			SessionID: "s",
			Timestamp: base.Add(time.Duration(i) * time.Millisecond),
			Payload:   map[string]any{"n": i},
		})
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := l.QueryEvents("alpha", "", 50000, map[string]bool{"message.in": true})
		if err == nil && len(got) == 1200 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	got, err := l.QueryEvents("alpha", "", 50000, map[string]bool{"message.in": true})
	if err != nil {
		t.Fatalf("QueryEvents large window: %v", err)
	}
	if len(got) != 1200 {
		t.Fatalf("large QueryEvents window returned %d events, want 1200", len(got))
	}
}
