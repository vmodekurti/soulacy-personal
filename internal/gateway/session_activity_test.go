package gateway

import (
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/message"
)

// TestSessionActivityTracker pins the E4c behaviours:
//   - message.in starts a session with StartedAt = event ts
//   - subsequent llm/tool events bump LastEventAt
//   - message.out / error evict the session
//   - a session silent past the threshold is flagged Hung with a reason
//   - a very old session (last event > 1h ago) is swept on Snapshot()
//   - events without a session_id are ignored (connected, etc.)
func TestSessionActivityTracker(t *testing.T) {
	tr := newSessionActivityTracker()
	// Make time deterministic so we can walk a fake clock through the run.
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	tr.nowFn = func() time.Time { return now }
	tr.SetHungThreshold(30 * time.Second)

	// System event without session — no-op.
	tr.Note(message.Event{Type: "connected", Timestamp: now})
	if len(tr.Snapshot()) != 0 {
		t.Fatal("connected event without session should not create a record")
	}

	// Session starts.
	tr.Note(message.Event{
		Type: "message.in", AgentID: "briefer", SessionID: "s1", Timestamp: now,
	})
	snap := tr.Snapshot()
	if len(snap) != 1 || snap[0].SessionID != "s1" {
		t.Fatalf("expected s1 tracked, got %+v", snap)
	}
	if snap[0].ElapsedSeconds != 0 || snap[0].SilentSeconds != 0 {
		t.Fatalf("timers should be zero at start, got %+v", snap[0])
	}
	if snap[0].Hung {
		t.Fatal("session should not be hung immediately after start")
	}

	// 10s later, LLM call fires.
	now = now.Add(10 * time.Second)
	tr.Note(message.Event{
		Type: "llm.call", AgentID: "briefer", SessionID: "s1", Timestamp: now,
	})
	snap = tr.Snapshot()
	if snap[0].LastEventType != "llm.call" {
		t.Fatalf("LastEventType should update to llm.call, got %q", snap[0].LastEventType)
	}
	if snap[0].SilentSeconds != 0 || snap[0].ElapsedSeconds != 10 {
		t.Fatalf("expected elapsed=10s silent=0s, got %+v", snap[0])
	}

	// 45s later (silent=45, past 30s threshold): should flag hung with llm-flavored reason.
	now = now.Add(45 * time.Second)
	snap = tr.Snapshot()
	if !snap[0].Hung {
		t.Fatalf("session should be hung after silent>30s, got %+v", snap[0])
	}
	if snap[0].HungReason == "" || snap[0].HungFix == "" {
		t.Fatalf("hung session should have reason+fix, got %+v", snap[0])
	}
	// Reason must reference the LLM (last event was llm.call).
	if !containsSubstring(snap[0].HungReason, "LLM") {
		t.Fatalf("hung reason should mention LLM for llm.call last-event, got %q", snap[0].HungReason)
	}

	// Terminal message.out evicts.
	tr.Note(message.Event{
		Type: "message.out", AgentID: "briefer", SessionID: "s1", Timestamp: now,
	})
	if len(tr.Snapshot()) != 0 {
		t.Fatal("message.out should evict the session")
	}

	// Bootstrap-from-tail case: an llm.call for an unknown session should be
	// tracked with StartedAt = event ts (so /activity/running still reports it).
	orphan := now.Add(60 * time.Second)
	tr.Note(message.Event{
		Type: "llm.call", AgentID: "briefer", SessionID: "orphan", Timestamp: orphan,
	})
	if got := tr.Snapshot(); len(got) != 1 || got[0].SessionID != "orphan" {
		t.Fatalf("expected orphan session tracked, got %+v", got)
	}

	// Sweep: last-event > 1h ago drops.
	now = orphan.Add(2 * time.Hour)
	if len(tr.Snapshot()) != 0 {
		t.Fatal("session with last event > 1h ago should be evicted by Snapshot")
	}
}

// TestSessionActivitySortByStart pins the "newest first" sort so operators
// looking at "Running now" see the freshest run at the top.
func TestSessionActivitySortByStart(t *testing.T) {
	tr := newSessionActivityTracker()
	base := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	now := base
	tr.nowFn = func() time.Time { return now }

	// Older session
	tr.Note(message.Event{Type: "message.in", SessionID: "older", AgentID: "a", Timestamp: base})
	// Newer session 30s later
	now = base.Add(30 * time.Second)
	tr.Note(message.Event{Type: "message.in", SessionID: "newer", AgentID: "b", Timestamp: now})

	snap := tr.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(snap))
	}
	if snap[0].SessionID != "newer" {
		t.Fatalf("newest first: expected newer, got %q", snap[0].SessionID)
	}
}

// TestEventHubEmitFeedsTracker verifies the wiring — a raw Emit() must show up
// in the tracker snapshot. This is the E4c regression fence: if a future
// refactor pulls the Note() call out of Emit(), this test breaks.
func TestEventHubEmitFeedsTracker(t *testing.T) {
	h := NewEventHub(zap.NewNop(), nil)
	h.Emit(message.Event{
		Type:      "message.in",
		AgentID:   "tester",
		SessionID: "session-x",
		Timestamp: time.Now(),
	})
	snap := h.RunningSessions()
	if len(snap) != 1 || snap[0].SessionID != "session-x" {
		t.Fatalf("expected session-x tracked, got %+v", snap)
	}
}

func containsSubstring(s, sub string) bool {
	if sub == "" {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
