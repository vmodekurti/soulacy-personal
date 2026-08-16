// side_effects_test.go — MU-021 criterion 6, the app half: the run's identity
// reaches the engine's side-effect recorder, and nothing else does.
package app

import (
	"context"
	"testing"

	"github.com/soulacy/soulacy/internal/runs"
)

// The marker only means something if it is attached to the right run. A
// recorder that marked the wrong one would fail the wrong run for review while
// letting the real one retry.
func TestTheRecorderMarksTheRunOnItsContext(t *testing.T) {
	store := newRunStore(t)
	ctx := context.Background()
	run := submitRun(t, store, "run_marked", "ws-a")

	rec := runSideEffectRecorder{store: store}
	if err := rec.RecordSideEffect(withRunID(ctx, run.WorkspaceID, run.ID), "shell_exec"); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(ctx, run.WorkspaceID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SideEffectAt == nil || stored.SideEffectTool != "shell_exec" {
		t.Fatalf("run was not marked: at=%v tool=%q", stored.SideEffectAt, stored.SideEffectTool)
	}
}

// Chat requests, schedules and channel messages reach the same tool dispatch
// with no run on their context. That is the ordinary case, not a miss: it must
// be silent, and it must not mark anything.
func TestACallWithNoRunOnItsContextMarksNothing(t *testing.T) {
	store := newRunStore(t)
	ctx := context.Background()
	run := submitRun(t, store, "run_untouched", "ws-a")

	rec := runSideEffectRecorder{store: store}
	if err := rec.RecordSideEffect(ctx, "shell_exec"); err != nil {
		t.Fatalf("a call with no run should be a silent no-op: %v", err)
	}
	// withRunID with an empty id must not put a half-formed pair on the
	// context either — that would mark run "" and, worse, look like it worked.
	if err := rec.RecordSideEffect(withRunID(ctx, "ws-a", "  "), "shell_exec"); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(ctx, run.WorkspaceID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SideEffectAt != nil {
		t.Fatal("a run with no side effect was marked")
	}
}

// The recorder must survive the run's own deadline. A run killed mid-tool-call
// is precisely the case the marker exists for, so a cancelled context must
// still write it — otherwise the run looks retry-safe and gets re-executed.
func TestTheMarkerSurvivesTheRunsOwnCancellation(t *testing.T) {
	store := newRunStore(t)
	run := submitRun(t, store, "run_timeout", "ws-a")

	ctx, cancel := context.WithCancel(withRunID(context.Background(), run.WorkspaceID, run.ID))
	cancel()

	rec := runSideEffectRecorder{store: store}
	if err := rec.RecordSideEffect(ctx, "http_request"); err != nil {
		t.Fatalf("marker lost to cancellation: %v", err)
	}
	stored, err := store.Get(context.Background(), run.WorkspaceID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SideEffectAt == nil {
		t.Fatal("a run cancelled mid-call was left looking retry-safe")
	}
}

// A deployment with no durable run store has no record to protect. The
// recorder must be inert rather than an error path every tool call walks.
func TestTheRecorderIsInertWithoutAStore(t *testing.T) {
	rec := runSideEffectRecorder{}
	if err := rec.RecordSideEffect(withRunID(context.Background(), "ws-a", "run_x"), "shell_exec"); err != nil {
		t.Fatalf("recorder without a store returned an error: %v", err)
	}
}

// A recovered run needs a message, and that message must carry the run id back
// — otherwise the worker executes it as an anonymous chat turn and the record
// never leaves `queued`.
func TestARecoveredRunsMessageStillIdentifiesTheRun(t *testing.T) {
	run := runs.Run{ID: "run_z", WorkspaceID: "ws-a", AgentID: "bot", Payload: []byte(`{"prompt":"go"}`)}
	msg := runs.InboundMessage(run)
	if RunIDOf(msg) != "run_z" {
		t.Fatalf("RunIDOf = %q, want run_z", RunIDOf(msg))
	}
	if msg.WorkspaceID != "ws-a" {
		t.Fatalf("workspace = %q", msg.WorkspaceID)
	}
	if msg.Channel != RunChannel {
		t.Fatalf("channel = %q, want %q", msg.Channel, RunChannel)
	}
}
