package actionlog

import (
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/message"
)

func newStatsTestLogger(t *testing.T) *Logger {
	t.Helper()
	dir := t.TempDir()
	l, err := New(filepath.Join(dir, "logs"), filepath.Join(dir, "actions.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

// waitForEvents polls until the session has at least n events in SQLite
// (the writer is async with a 250ms flush interval).
func waitForEvents(t *testing.T, l *Logger, agentID, sessionID string, n int) SessionStats {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st, err := l.SessionStats(agentID, sessionID)
		if err != nil {
			t.Fatalf("SessionStats: %v", err)
		}
		if st.Events >= n {
			return st
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s never reached %d events", sessionID, n)
	return SessionStats{}
}

func TestSessionStats_CountsAndTimestamps(t *testing.T) {
	l := newStatsTestLogger(t)
	base := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

	evs := []message.Event{
		{Type: "message.in", AgentID: "bot", SessionID: "s1", Timestamp: base},
		{Type: "tool.call", AgentID: "bot", SessionID: "s1", Timestamp: base.Add(1 * time.Second)},
		{Type: "tool.result", AgentID: "bot", SessionID: "s1", Timestamp: base.Add(2 * time.Second)},
		{Type: "tool.call", AgentID: "bot", SessionID: "s1", Timestamp: base.Add(3 * time.Second)},
		{Type: "message.out", AgentID: "bot", SessionID: "s1", Timestamp: base.Add(5 * time.Second)},
		// Noise in another session and another agent.
		{Type: "tool.call", AgentID: "bot", SessionID: "s2", Timestamp: base},
		{Type: "tool.call", AgentID: "other", SessionID: "s1", Timestamp: base},
	}
	for _, ev := range evs {
		l.Append(ev)
	}

	st := waitForEvents(t, l, "bot", "s1", 5)
	if st.Events != 5 {
		t.Errorf("Events = %d, want 5", st.Events)
	}
	if st.ToolCalls != 2 {
		t.Errorf("ToolCalls = %d, want 2", st.ToolCalls)
	}
	if !st.FirstEvent.Equal(base) {
		t.Errorf("FirstEvent = %v, want %v", st.FirstEvent, base)
	}
	if !st.LastEvent.Equal(base.Add(5 * time.Second)) {
		t.Errorf("LastEvent = %v, want %v", st.LastEvent, base.Add(5*time.Second))
	}
	if st.LastError != "" {
		t.Errorf("LastError = %q, want empty", st.LastError)
	}
}

func TestSessionStats_CapturesLastError(t *testing.T) {
	l := newStatsTestLogger(t)
	base := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

	l.Append(message.Event{Type: "message.in", AgentID: "bot", SessionID: "s1", Timestamp: base})
	l.Append(message.Event{
		Type: "error", AgentID: "bot", SessionID: "s1", Timestamp: base.Add(time.Second),
		Payload: map[string]any{"error": "first failure"},
	})
	l.Append(message.Event{
		Type: "error", AgentID: "bot", SessionID: "s1", Timestamp: base.Add(2 * time.Second),
		Payload: map[string]any{"error": "boom: provider exploded"},
	})

	st := waitForEvents(t, l, "bot", "s1", 3)
	if st.LastError != "boom: provider exploded" {
		t.Errorf("LastError = %q, want %q", st.LastError, "boom: provider exploded")
	}
}

func TestSessionStats_AgentOptional(t *testing.T) {
	l := newStatsTestLogger(t)
	base := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	l.Append(message.Event{Type: "tool.call", AgentID: "bot-a", SessionID: "shared", Timestamp: base})
	l.Append(message.Event{Type: "tool.call", AgentID: "bot-b", SessionID: "shared", Timestamp: base.Add(time.Second)})

	// Empty agentID matches across agents.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st, err := l.SessionStats("", "shared")
		if err != nil {
			t.Fatalf("SessionStats: %v", err)
		}
		if st.Events == 2 {
			if st.ToolCalls != 2 {
				t.Errorf("ToolCalls = %d, want 2", st.ToolCalls)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("cross-agent stats never reached 2 events")
}

func TestSessionStats_EmptySession(t *testing.T) {
	l := newStatsTestLogger(t)
	st, err := l.SessionStats("bot", "never-existed")
	if err != nil {
		t.Fatalf("SessionStats: %v", err)
	}
	if st.Events != 0 || st.ToolCalls != 0 {
		t.Errorf("empty session stats = %+v", st)
	}
	if !st.FirstEvent.IsZero() || !st.LastEvent.IsZero() {
		t.Errorf("timestamps should be zero for empty session: %+v", st)
	}
}
