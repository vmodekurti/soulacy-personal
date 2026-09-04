package runtime

import (
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/reasoning"
)

func TestFlowRunHistory_ListsEveryRunNewestFirst(t *testing.T) {
	s := newFlowTraceStore(50, "")

	// Run 1: scheduled, succeeds.
	s.tag("agent", "run-1", "schedule")
	s.Record("agent", "run-1", reasoning.FlowNodeRun{NodeID: "a", StartedAt: time.Now()})

	// Run 2: on-demand (http), fails on a block.
	s.tag("agent", "run-2", "http")
	s.Record("agent", "run-2", reasoning.FlowNodeRun{NodeID: "a", StartedAt: time.Now()})
	s.Record("agent", "run-2", reasoning.FlowNodeRun{NodeID: "b", Error: "boom", StartedAt: time.Now()})

	hist := s.list("agent")
	if len(hist) != 2 {
		t.Fatalf("want 2 runs in history, got %d", len(hist))
	}
	// Newest first.
	if hist[0].RunID != "run-2" || hist[1].RunID != "run-1" {
		t.Fatalf("history should be newest-first; got %s, %s", hist[0].RunID, hist[1].RunID)
	}
	// Verdict + trigger captured.
	if hist[0].Ok || hist[0].Error != "boom" || hist[0].Trigger != "http" {
		t.Errorf("failed on-demand run summarized wrong: %+v", hist[0])
	}
	if !hist[1].Ok || hist[1].Trigger != "schedule" || hist[1].Steps != 1 {
		t.Errorf("ok scheduled run summarized wrong: %+v", hist[1])
	}
}

func TestFlowRunHistory_TagBeforeAnyBlockStillListed(t *testing.T) {
	// A run that fails before any block records (tagged at start) must still appear.
	s := newFlowTraceStore(50, "")
	s.tag("agent", "run-x", "schedule")
	hist := s.list("agent")
	if len(hist) != 1 || hist[0].RunID != "run-x" || hist[0].Steps != 0 {
		t.Fatalf("a tagged-but-unexecuted run must be listed; got %+v", hist)
	}
}

func TestFlowRunHistory_EmptyForUnknownAgent(t *testing.T) {
	s := newFlowTraceStore(50, "")
	if h := s.list("nobody"); len(h) != 0 {
		t.Errorf("unknown agent should have empty history, got %+v", h)
	}
}

// A run persisted to disk is still listed and viewable from a FRESH store (as if
// after a gateway restart) — durable history.
func TestFlowRunHistory_DurableAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	s1 := newFlowTraceStore(50, dir)
	s1.tag("agent", "run-1", "schedule")
	s1.Record("agent", "run-1", reasoning.FlowNodeRun{NodeID: "a", StartedAt: time.Now()})
	s1.Record("agent", "run-1", reasoning.FlowNodeRun{NodeID: "b", Error: "kaboom", StartedAt: time.Now()})
	s1.persist("agent", "run-1")

	// Fresh store pointed at the same dir = post-restart.
	s2 := newFlowTraceStore(50, dir)
	hist := s2.list("agent")
	if len(hist) != 1 || hist[0].RunID != "run-1" {
		t.Fatalf("durable run should survive restart; got %+v", hist)
	}
	if hist[0].Ok || hist[0].Error != "kaboom" || hist[0].Trigger != "schedule" {
		t.Errorf("durable summary wrong: %+v", hist[0])
	}
	// And the full trace is viewable from disk.
	tr, ok := s2.getFrom("agent", "run-1")
	if !ok || len(tr.Entries) != 2 {
		t.Errorf("durable full trace not readable from disk; ok=%v entries=%d", ok, len(tr.Entries))
	}
}
