// lease_test.go — recovery must tell a DEAD worker from a BUSY one.
//
// Before the lease it could not, and the consequence was not subtle: the
// startup sweep selected every unfinished run in the deployment and re-queued
// or failed it. On one gateway that is right. On two, replica B booting
// re-queues runs replica A is executing at that moment — and a rolling
// restart, which is how a second replica normally appears, duplicates
// everything in flight on every deploy.
package runs

import (
	"context"
	"testing"
	"time"
)

func TestALiveWorkersRunSurvivesAnotherReplicasBootSweep(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	// Replica A claims and is still working: its lease is current.
	run := submit(t, store, "run_live", "ws-a", "agent")
	if _, err := store.Transition(ctx, run.WorkspaceID, run.ID, StatusRunning,
		TransitionOptions{Owner: "replica-a", Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}

	// Replica B boots and sweeps.
	outcomes, err := store.RecoverPending(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, outcome := range outcomes {
		if outcome.Run.ID == "run_live" {
			t.Fatalf("a booting replica recovered a run another replica is executing (%s: %s) — "+
				"the agent runs twice", outcome.Action, outcome.Reason)
		}
	}
	stored, err := store.Get(ctx, "ws-a", "run_live")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusRunning {
		t.Errorf("status = %s, want running — the sweep moved a live worker's run", stored.Status)
	}
}

func TestAnExpiredLeaseIsRecoveredAsBefore(t *testing.T) {
	// The other half: a lease that stops being renewed is what a crash looks
	// like, and the pre-existing recovery policy must still apply to it. A
	// lease that made runs unrecoverable would trade a duplicate-execution bug
	// for a stuck-run bug.
	store := newStore(t)
	ctx := context.Background()

	run := submit(t, store, "run_dead", "ws-a", "agent")
	claimed, err := store.Transition(ctx, run.WorkspaceID, run.ID, StatusRunning,
		TransitionOptions{Owner: "replica-a", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	// Expired by moving the clock forward in the row rather than by claiming
	// with a negative lease: Transition treats a non-positive lease as "use
	// the default", because a caller passing one has made a mistake and the
	// safe reading of a mistake is a normal lease, not an instantly-dead one.
	abandon(t, store, claimed)

	outcomes, recErr := store.RecoverPending(ctx)
	if recErr != nil {
		t.Fatal(recErr)
	}
	if got := outcomeFor(t, outcomes, "run_dead"); got.Action != RecoveryRequeued {
		t.Errorf("action = %s, want requeued — a crashed worker's run is stuck", got.Action)
	}
}

func TestARunWithNoLeaseIsStillRecoverable(t *testing.T) {
	// Every run written before this change has a NULL expiry. Treating a
	// missing lease as "held" would make them permanently invisible to
	// recovery — a silent regression that only appears on upgraded
	// deployments, and only for work that was in flight during the upgrade.
	store := newStore(t)
	ctx := context.Background()

	run := submit(t, store, "run_legacy", "ws-a", "agent")
	if _, err := store.Transition(ctx, run.WorkspaceID, run.ID, StatusRunning,
		TransitionOptions{Owner: "replica-a", Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}
	// Exactly what a pre-migration row looks like.
	if _, err := store.db.ExecContext(ctx,
		`UPDATE agent_runs SET lease_expires_at = NULL WHERE workspace_id = ? AND id = ?`,
		"ws-a", "run_legacy"); err != nil {
		t.Fatal(err)
	}

	outcomes, err := store.RecoverPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomeFor(t, outcomes, "run_legacy"); got.Action != RecoveryRequeued {
		t.Errorf("action = %s, want requeued", got.Action)
	}
}

func TestOnlyTheHolderCanRenew(t *testing.T) {
	// A worker whose lease expired and was taken over must not extend the new
	// holder's claim. That would produce two live holders — the one state the
	// lease exists to make impossible — and neither would know.
	store := newStore(t)
	ctx := context.Background()

	run := submit(t, store, "run_x", "ws-a", "agent")
	if _, err := store.Transition(ctx, run.WorkspaceID, run.ID, StatusRunning,
		TransitionOptions{Owner: "replica-a", Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}

	if err := store.RenewLease(ctx, "ws-a", "run_x", "replica-a", time.Minute); err != nil {
		t.Fatalf("the holder could not renew its own lease: %v", err)
	}
	if err := store.RenewLease(ctx, "ws-a", "run_x", "replica-b", time.Minute); err != ErrLeaseLost {
		t.Errorf("a non-holder renewed the lease (err = %v)", err)
	}
	// An anonymous renewal is refused rather than treated as the holder: two
	// anonymous workers are indistinguishable, so "" can never be an owner.
	if err := store.RenewLease(ctx, "ws-a", "run_x", "", time.Minute); err == nil {
		t.Error("an anonymous renewal was accepted")
	}
}

func TestReleasingALeaseMakesTheRunRecoverableImmediately(t *testing.T) {
	// A clean shutdown must not cost a full lease period of recovery latency.
	// Without release, every ordinary restart would pause in-flight work for
	// a minute — which operators would correctly read as the lease making
	// things worse, and would respond to by shortening it until it stopped
	// protecting anything.
	store := newStore(t)
	ctx := context.Background()

	run := submit(t, store, "run_clean", "ws-a", "agent")
	if _, err := store.Transition(ctx, run.WorkspaceID, run.ID, StatusRunning,
		TransitionOptions{Owner: "replica-a", Lease: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseLease(ctx, "ws-a", "run_clean", "replica-a"); err != nil {
		t.Fatal(err)
	}

	outcomes, err := store.RecoverPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomeFor(t, outcomes, "run_clean"); got.Action != RecoveryRequeued {
		t.Errorf("action = %s, want requeued — a cleanly released run waited out its lease", got.Action)
	}
}

func TestTwoWorkspacesRunsAreLeasedIndependently(t *testing.T) {
	// Run IDs are unique per workspace, not per deployment (the recurring
	// trap in this codebase). A lease keyed by run ID alone would let one
	// tenant's renewal extend — or its release expose — another tenant's run
	// with the same ID.
	store := newStore(t)
	ctx := context.Background()

	for _, ws := range []string{"ws-a", "ws-b"} {
		run := submit(t, store, "daily-report", ws, "agent")
		if _, err := store.Transition(ctx, run.WorkspaceID, run.ID, StatusRunning,
			TransitionOptions{Owner: "replica-" + ws, Lease: time.Hour}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ReleaseLease(ctx, "ws-a", "daily-report", "replica-ws-a"); err != nil {
		t.Fatal(err)
	}

	other, err := store.Get(ctx, "ws-b", "daily-report")
	if err != nil {
		t.Fatal(err)
	}
	if !other.LeaseHeld(time.Now().UTC()) {
		t.Error("releasing one workspace's run released another workspace's run with the same id")
	}
}
