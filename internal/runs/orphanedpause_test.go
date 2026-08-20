package runs

import (
	"context"
	"testing"
	"time"
)

func pausedRun(t *testing.T, store *Store, id, workspaceID string) Run {
	t.Helper()
	ctx := context.Background()
	submit(t, store, id, workspaceID, "bot")
	if _, err := store.Transition(ctx, workspaceID, id, StatusRunning,
		TransitionOptions{Owner: "worker-1", Lease: time.Minute}); err != nil {
		t.Fatalf("claim %s: %v", id, err)
	}
	run, err := store.Transition(ctx, workspaceID, id, StatusPaused, TransitionOptions{})
	if err != nil {
		t.Fatalf("pause %s: %v", id, err)
	}
	return run
}

// The orphan itself: before this existed, the crash sweep skipped the paused
// run and the approvals sweep closed the only thing that could have released
// it. Nothing was left that could move either record.
func TestARunPausedOnAClosedApprovalIsRequeuedNotStranded(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	pausedRun(t, store, "run_a", "ws_a")

	outcome, err := store.ResolveOrphanedPause(ctx, "ws_a", "run_a", "the approval was closed")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Action != RecoveryRequeued {
		t.Fatalf("action = %q (%s), want requeued", outcome.Action, outcome.Reason)
	}
	stored, err := store.Get(ctx, "ws_a", "run_a")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusQueued {
		t.Fatalf("status = %q; a run left paused on an invalidated approval can never be released "+
			"by anybody", stored.Status)
	}
}

// The crash sweep's own policy, not a second one: a run that already acted on
// the world is not silently replayed just because its approval lapsed.
func TestARunThatAlreadyActedIsFailedForReviewRatherThanReplayed(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	pausedRun(t, store, "run_b", "ws_a")
	if err := store.MarkSideEffect(ctx, "ws_a", "run_b", "send_email"); err != nil {
		t.Fatal(err)
	}

	outcome, err := store.ResolveOrphanedPause(ctx, "ws_a", "run_b", "the approval was closed")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Action != RecoveryFailedSideEffects {
		t.Fatalf("action = %q, want failed_side_effects", outcome.Action)
	}
	stored, _ := store.Get(ctx, "ws_a", "run_b")
	if stored.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", stored.Status)
	}
	if stored.FailureReason == "" {
		t.Error("a run a human has to inspect must say why it is waiting for them")
	}
}

// A run released, cancelled, or claimed between the two sweeps is not paused
// any more, and this must not reach back into it.
func TestARunThatIsNoLongerPausedIsLeftAlone(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	submit(t, store, "run_c", "ws_a", "bot")

	outcome, err := store.ResolveOrphanedPause(ctx, "ws_a", "run_c", "the approval was closed")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Action != RecoverySkipped {
		t.Fatalf("action = %q, want skipped", outcome.Action)
	}
	stored, _ := store.Get(ctx, "ws_a", "run_c")
	if stored.Status != StatusQueued {
		t.Fatalf("a queued run was moved by the orphan sweep: %q", stored.Status)
	}
}

// The sweep is deployment-wide and takes each run's own workspace. Repairing
// ws_a's run must not touch the identically-named run in ws_b.
func TestTheOrphanSweepActsOnlyOnTheWorkspaceItWasGiven(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	pausedRun(t, store, "run_shared", "ws_a")
	pausedRun(t, store, "run_shared", "ws_b")

	if _, err := store.ResolveOrphanedPause(ctx, "ws_a", "run_shared", "closed"); err != nil {
		t.Fatal(err)
	}
	other, err := store.Get(ctx, "ws_b", "run_shared")
	if err != nil {
		t.Fatal(err)
	}
	if other.Status != StatusPaused {
		t.Fatalf("ws_b's run changed to %q while repairing ws_a's", other.Status)
	}
}
