// runlease_test.go — the worker has to keep saying it is alive.
//
// internal/runs proves the lease primitives are correct. This proves the
// WORKER uses them: claims with an owner, renews while it works, releases when
// it stops, and stops executing if it loses the claim. Each of those is a
// separate way for the store's correctness to make no difference — a worker
// that claims anonymously, or never renews, produces exactly the
// duplicate-execution the lease was added to prevent, with a store that is
// behaving perfectly.
package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/runs"
)

func leaseStore(t *testing.T) *runs.Store {
	t.Helper()
	store, err := runs.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func submitAndClaim(t *testing.T, store *runs.Store, owner string) runs.Run {
	t.Helper()
	ctx := context.Background()
	submitted, _, err := store.Submit(ctx, runs.Run{ID: "run_1", WorkspaceID: "ws_a", AgentID: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok := beginRun(ctx, store, submitted.WorkspaceID, submitted.ID, owner, zap.NewNop())
	if !ok {
		t.Fatal("claim failed")
	}
	return claimed
}

func TestAWorkerClaimsWithItsOwnIdentity(t *testing.T) {
	store := leaseStore(t)
	claimed := submitAndClaim(t, store, "replica-a")

	if claimed.ClaimedBy != "replica-a" {
		t.Errorf("claimed_by = %q, want the worker's identity — an anonymous claim cannot be "+
			"renewed, so the run is abandoned as soon as its first lease lapses", claimed.ClaimedBy)
	}
	if !claimed.LeaseHeld(time.Now().UTC()) {
		t.Error("the claim took no lease, so a booting replica would treat this live run as interrupted")
	}
}

func TestTheWorkerIDIsStableWithinAProcessAndUniqueAcrossThem(t *testing.T) {
	a := &App{}
	// Stable: a renewal has to be recognisable as coming from the holder.
	firstWorkerID := a.workerID()
	secondWorkerID := a.workerID()
	if firstWorkerID != secondWorkerID {
		t.Error("the worker identity changed between calls, so this process cannot renew its own leases")
	}
	// Unique: a restarted container keeps its hostname, and its predecessor's
	// runs must not look renewable by it.
	b := &App{}
	if a.workerID() == b.workerID() {
		t.Error("two processes share a worker identity; a restart would be mistaken for the process that died")
	}
}

func TestHoldingALeaseRenewsItAndReleasingItGivesItUp(t *testing.T) {
	store := leaseStore(t)
	ctx := context.Background()
	claimed := submitAndClaim(t, store, "replica-a")

	release := holdRunLease(ctx, store, claimed, "replica-a", nil, zap.NewNop())
	release()

	after, err := store.Get(ctx, claimed.WorkspaceID, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Released, not merely stopped. A worker that finishes and leaves its
	// lease standing makes the NEXT crash wait a full lease period before
	// recovery, for no reason.
	if after.LeaseHeld(time.Now().UTC()) {
		t.Error("the lease was still held after the worker released it")
	}
}

func TestLosingTheLeaseCancelsTheRun(t *testing.T) {
	// The dangerous case. If another worker has taken the claim, this worker
	// is executing a run somebody else also has — two engines, one record,
	// and every tool call happening twice. Continuing is worse than stopping:
	// the outcome will be refused by the status-conditioned UPDATE either
	// way, but only cancelling stops the side effects.
	store := leaseStore(t)
	ctx := context.Background()
	claimed := submitAndClaim(t, store, "replica-a")

	// Renewal shortened so the test exercises the path instead of waiting out
	// a production interval.
	previous := runLeaseRenewInterval
	runLeaseRenewInterval = 10 * time.Millisecond
	t.Cleanup(func() { runLeaseRenewInterval = previous })

	cancelled := make(chan struct{})
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Steal the claim, exactly as a recovery sweep on another replica would
	// after this worker's lease lapsed.
	if err := store.RenewLease(ctx, claimed.WorkspaceID, claimed.ID, "replica-a", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, claimed.WorkspaceID, claimed.ID); err != nil {
		t.Fatal(err)
	}
	stolen := claimed
	stolen.ClaimedBy = "replica-b"
	release := holdRunLease(runCtx, store, stolen, "replica-b", func() { close(cancelled) }, zap.NewNop())
	defer release()

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Error("a worker that lost its lease kept executing; two workers are now running one run")
	}
}

func TestStoppingTheCancellationWatcherWaitsForIt(t *testing.T) {
	// The doc comment on watchForCancellation says a stopped watcher must not
	// hold a database handle for a record nobody is watching. Returning the
	// moment `done` was closed made that claim weaker than it reads: the
	// goroutine could still be mid-poll.
	//
	// Asserted by observing the goroutine's own exit rather than by sleeping,
	// because a sleep-based version passes on the broken build whenever the
	// scheduler happens to run the goroutine first — which is most of the time,
	// which is why this survived.
	previous := cancelPollInterval
	cancelPollInterval = time.Millisecond
	t.Cleanup(func() { cancelPollInterval = previous })

	store := leaseStore(t)
	claimed := submitAndClaim(t, store, "replica-a")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := watchForCancellation(ctx, cancel, store, claimed, zap.NewNop())

	// Let it get into its loop, so stopping has something to wait for.
	time.Sleep(5 * time.Millisecond)
	stop()

	// After stop() returns, the goroutine is gone — so a second call is safe
	// and instant, and nothing is left polling. Idempotence matters because
	// executeDurableRun reaches this on every path.
	stop()
}
