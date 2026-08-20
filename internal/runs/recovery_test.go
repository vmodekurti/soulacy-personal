// recovery_test.go — MU-021 criterion 6: worker loss causes bounded retry only
// for retry-safe stages, and side-effecting calls are not repeated without
// approval.
package runs

import (
	"context"
	"strings"
	"testing"
	"time"
)

func claim(t *testing.T, store *Store, run Run) Run {
	t.Helper()
	claimed, err := store.Transition(context.Background(), run.WorkspaceID, run.ID, StatusRunning,
		TransitionOptions{Owner: "worker-1"})
	if err != nil {
		t.Fatalf("claim %s: %v", run.ID, err)
	}
	return claimed
}

// abandon expires a claim's lease, which is what a worker DYING looks like
// from the outside.
//
// Every recovery test below used to reach this state by simply claiming a run,
// because "running" and "abandoned" were the same thing: nothing recorded who
// held a run or for how long, so the sweep treated every running row as
// interrupted. That is exactly the bug MU-034 fixes — with two replicas it
// re-queued work a live worker was doing — and it means these tests have to
// say which of the two states they mean now.
func abandon(t *testing.T, store *Store, run Run) Run {
	t.Helper()
	if _, err := store.db.ExecContext(context.Background(),
		`UPDATE agent_runs SET lease_expires_at = ? WHERE workspace_id = ? AND id = ?`,
		time.Now().UTC().Add(-time.Minute), run.WorkspaceID, run.ID); err != nil {
		t.Fatalf("abandon %s: %v", run.ID, err)
	}
	updated, err := store.Get(context.Background(), run.WorkspaceID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

// claimAndAbandon is the old claim(): a run whose worker is gone.
func claimAndAbandon(t *testing.T, store *Store, run Run) Run {
	t.Helper()
	return abandon(t, store, claim(t, store, run))
}

func outcomeFor(t *testing.T, outcomes []RecoveryOutcome, id string) RecoveryOutcome {
	t.Helper()
	for _, outcome := range outcomes {
		if outcome.Run.ID == id {
			return outcome
		}
	}
	t.Fatalf("no recovery outcome for %s", id)
	return RecoveryOutcome{}
}

// A run whose worker died before it did anything is safe to run again — that
// is the whole reason not to fail everything on restart.
func TestARunThatNeverActedIsRequeuedAfterWorkerLoss(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	run := claimAndAbandon(t, store, submit(t, store, "run_clean", "ws-a", "agent"))
	if run.Attempt != 1 {
		t.Fatalf("claiming a run should count as an attempt, got %d", run.Attempt)
	}

	outcomes, err := store.RecoverPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	outcome := outcomeFor(t, outcomes, "run_clean")
	if outcome.Action != RecoveryRequeued {
		t.Fatalf("action = %s (%s), want requeued", outcome.Action, outcome.Reason)
	}
	if outcome.Run.Status != StatusQueued {
		t.Fatalf("status = %s, want queued", outcome.Run.Status)
	}
	if outcome.Run.StartedAt != nil {
		t.Fatal("a re-queued run still claims to have started")
	}
	// Re-queueing is not a claim, so it must not spend an attempt. If it did,
	// each crash would burn two and the bound would silently halve.
	if outcome.Run.Attempt != 1 {
		t.Fatalf("attempt = %d after requeue, want 1", outcome.Run.Attempt)
	}
}

// The case the criterion is actually about: the run already called out to the
// world, so "retry" means doing it twice.
func TestARunThatAlreadyActedIsNotRetried(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	run := claimAndAbandon(t, store, submit(t, store, "run_dirty", "ws-a", "agent"))
	if err := store.MarkSideEffect(ctx, run.WorkspaceID, run.ID, "http_request"); err != nil {
		t.Fatal(err)
	}

	outcomes, err := store.RecoverPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	outcome := outcomeFor(t, outcomes, "run_dirty")
	if outcome.Action != RecoveryFailedSideEffects {
		t.Fatalf("action = %s, want %s", outcome.Action, RecoveryFailedSideEffects)
	}
	if outcome.Run.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", outcome.Run.Status)
	}
	// The reason has to name the tool. An operator deciding whether to
	// resubmit needs to know what may already have happened; "worker lost" on
	// its own tells them nothing about where to look.
	if !strings.Contains(outcome.Run.FailureReason, "http_request") {
		t.Fatalf("failure reason does not name the tool: %q", outcome.Run.FailureReason)
	}
	// The payload and principal survive, because resubmission after review is
	// the intended next step — this is approval, not prohibition.
	if outcome.Run.Subject == "" || outcome.Run.AgentID == "" {
		t.Fatal("a run failed for review lost the facts needed to resubmit it")
	}
}

// The marker keeps the FIRST call, because that is the one that decides
// safety. Dispatch reports every side-effecting call, not only the first.
func TestTheSideEffectMarkerKeepsTheFirstCall(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	run := claimAndAbandon(t, store, submit(t, store, "run_many", "ws-a", "agent"))
	for _, tool := range []string{"http_request", "write_file", "shell_exec"} {
		if err := store.MarkSideEffect(ctx, run.WorkspaceID, run.ID, tool); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := store.Get(ctx, run.WorkspaceID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SideEffectTool != "http_request" {
		t.Fatalf("marker = %q, want the first call", stored.SideEffectTool)
	}
}

// A run that reliably kills its worker must stop being handed to workers.
func TestARunOutOfAttemptsIsNotRequeuedAgain(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	run, _, err := store.Submit(ctx, Run{ID: "run_flaky", WorkspaceID: "ws-a", AgentID: "agent", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		run = claimAndAbandon(t, store, run)
		outcomes, err := store.RecoverPending(ctx)
		if err != nil {
			t.Fatal(err)
		}
		outcome := outcomeFor(t, outcomes, "run_flaky")
		if i == 0 && outcome.Action != RecoveryRequeued {
			t.Fatalf("first loss: action = %s, want requeued", outcome.Action)
		}
		if i == 1 && outcome.Action != RecoveryFailedExhausted {
			t.Fatalf("second loss: action = %s (%s), want exhausted", outcome.Action, outcome.Reason)
		}
		run = outcome.Run
	}
	if run.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", run.Status)
	}
}

// A paused run is somebody's decision, not wreckage. Sweeping it back into the
// queue would restart work a person deliberately stopped.
func TestAPausedRunIsLeftAlone(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	run := claimAndAbandon(t, store, submit(t, store, "run_paused", "ws-a", "agent"))
	if _, err := store.Transition(ctx, run.WorkspaceID, run.ID, StatusPaused, TransitionOptions{}); err != nil {
		t.Fatal(err)
	}
	outcomes, err := store.RecoverPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	outcome := outcomeFor(t, outcomes, "run_paused")
	if outcome.Action != RecoverySkipped {
		t.Fatalf("action = %s, want skipped", outcome.Action)
	}
	if outcome.Run.Status != StatusPaused {
		t.Fatalf("status = %s, want paused", outcome.Run.Status)
	}
}

// The sweep is deployment-wide and must decide per run, against that run's own
// workspace — never inherit one workspace for all of them.
func TestTheSweepDecidesPerRunAcrossWorkspaces(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	clean := claimAndAbandon(t, store, submit(t, store, "run_a", "ws-a", "agent"))
	dirty := claimAndAbandon(t, store, submit(t, store, "run_b", "ws-b", "agent"))
	if err := store.MarkSideEffect(ctx, dirty.WorkspaceID, dirty.ID, "shell_exec"); err != nil {
		t.Fatal(err)
	}

	outcomes, err := store.RecoverPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomeFor(t, outcomes, clean.ID); got.Action != RecoveryRequeued {
		t.Fatalf("ws-a run: %s", got.Action)
	}
	if got := outcomeFor(t, outcomes, dirty.ID); got.Action != RecoveryFailedSideEffects {
		t.Fatalf("ws-b run: %s", got.Action)
	}
	// And the marker did not bleed across the boundary.
	stored, err := store.Get(ctx, clean.WorkspaceID, clean.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SideEffectAt != nil {
		t.Fatal("one workspace's side effect marked another workspace's run")
	}
}

// A marker written with the wrong workspace must not land. The run ID alone is
// not authority — that is product invariant 8 applied to a write.
func TestASideEffectMarkerIsScopedToItsWorkspace(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	run := claimAndAbandon(t, store, submit(t, store, "run_scoped", "ws-a", "agent"))
	if err := store.MarkSideEffect(ctx, "ws-attacker", run.ID, "shell_exec"); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(ctx, "ws-a", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SideEffectAt != nil {
		t.Fatal("a marker addressed to another workspace landed on this run")
	}
}

// The sweep reads the run, then writes. A worker that is not as dead as the
// sweep assumed can act in between, and the marker it writes must still win —
// otherwise the one case the criterion exists for slips through a race.
func TestARunThatActsWhileTheSweepIsDecidingIsStillNotRequeued(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	run := claimAndAbandon(t, store, submit(t, store, "run_race", "ws-a", "agent"))

	// `run` is the sweep's stale read: taken before the side effect happened.
	if err := store.MarkSideEffect(ctx, run.WorkspaceID, run.ID, "http_request"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.requeue(ctx, run); err == nil {
		t.Fatal("a run that acted after the sweep read it was re-queued anyway")
	}
	stored, err := store.Get(ctx, run.WorkspaceID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusRunning {
		t.Fatalf("status = %s, want running (the requeue must not have landed)", stored.Status)
	}
}
