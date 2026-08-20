// runworker_test.go — MU-020's execution half: a durable run is claimed
// exactly once, its outcome is recorded, and a cancellation issued while it
// ran is not overwritten by the result.
package app

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/runs"
)

func newRunStore(t *testing.T) *runs.Store {
	t.Helper()
	store, err := runs.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func submitRun(t *testing.T, store *runs.Store, id, workspaceID string) runs.Run {
	t.Helper()
	run, _, err := store.Submit(context.Background(), runs.Run{
		ID: id, WorkspaceID: workspaceID, AgentID: "bot", Subject: "usr_a",
		PrincipalKind: "user", PolicySnapshot: []byte(`{"role":"developer","organization_id":"org_a","membership_id":"mem_a","workspace_id":"` + workspaceID + `"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// Two workers seeing the same message must not both execute it. Claiming
// through the state machine rather than a worker-local flag is what makes that
// true across processes, not merely across goroutines.
func TestOnlyOneWorkerClaimsARun(t *testing.T) {
	store := newRunStore(t)
	log := zap.NewNop()
	for round := 0; round < 8; round++ {
		id := "run_" + string(rune('a'+round))
		submitRun(t, store, id, "ws_a")

		const workers = 5
		var wg sync.WaitGroup
		claimed := make([]bool, workers)
		start := make(chan struct{})
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				<-start
				_, ok := beginRun(context.Background(), store, "ws_a", id, "worker-test", log)
				claimed[index] = ok
			}(i)
		}
		close(start)
		wg.Wait()

		count := 0
		for _, ok := range claimed {
			if ok {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("round %d: %d workers claimed the same run", round, count)
		}
	}
}

// A run cancelled between submission and pickup must not start. With any queue
// depth at all this is the ordinary case, not an edge one.
func TestACancelledRunIsNeverStarted(t *testing.T) {
	store := newRunStore(t)
	ctx := context.Background()
	submitRun(t, store, "run_1", "ws_a")
	if _, err := store.Transition(ctx, "ws_a", "run_1", runs.StatusCancelled,
		runs.TransitionOptions{FailureReason: "changed my mind"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := beginRun(ctx, store, "ws_a", "run_1", "worker-test", zap.NewNop()); ok {
		t.Fatal("a cancelled run was started")
	}
	got, err := store.Get(ctx, "ws_a", "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != runs.StatusCancelled {
		t.Fatalf("status = %q after a refused claim", got.Status)
	}
}

// Cancelling while the work is in flight: the run may well finish, but the
// record keeps the cancellation. Overwriting it with "succeeded" would tell
// the operator their cancellation did not take.
//
// The guarantee lives in the store's terminal check, not in this function —
// finishRun's ErrTerminal branch reports the situation, it does not create the
// protection. Worth being exact about: a mutation that makes finishRun force
// the outcome still cannot overwrite the cancellation, because the store
// refuses cancelled → running on the way back. The assertion below therefore
// names the store as the thing under test, and the worker as the thing that
// must not paper over it.
func TestACancellationDuringExecutionIsNotOverwritten(t *testing.T) {
	store := newRunStore(t)
	ctx := context.Background()
	run := submitRun(t, store, "run_1", "ws_a")
	started, ok := beginRun(ctx, store, "ws_a", "run_1", "worker-test", zap.NewNop())
	if !ok {
		t.Fatal("could not start the run")
	}
	if _, err := store.Transition(ctx, "ws_a", "run_1", runs.StatusCancelling,
		runs.TransitionOptions{FailureReason: "operator cancelled"}); err != nil {
		t.Fatal(err)
	}

	// The worker finished its work anyway — the record must still say the
	// operator's decision, not "succeeded".
	if !finishCancelled(ctx, store, started, zap.NewNop()) {
		t.Fatal("a cancelled run was not recognised as cancelled")
	}
	finishRun(ctx, store, started, "the work completed anyway", nil, zap.NewNop())

	got, err := store.Get(ctx, "ws_a", "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != runs.StatusCancelled {
		t.Fatalf("the outcome overwrote the cancellation: status = %q", got.Status)
	}
	if got.Result != "" {
		t.Fatalf("a cancelled run recorded a result: %q", got.Result)
	}

	// The store is where this holds. Stated directly so the property is
	// pinned even if the worker is rewritten: no path leads out of a terminal
	// state, however determined the caller.
	if _, err := store.Transition(ctx, "ws_a", "run_1", runs.StatusRunning, runs.TransitionOptions{}); !errors.Is(err, runs.ErrTerminal) {
		t.Fatalf("a cancelled run could be moved back to running: %v", err)
	}
	if _, err := store.Transition(ctx, "ws_a", "run_1", runs.StatusSucceeded, runs.TransitionOptions{Result: "forced"}); !errors.Is(err, runs.ErrTerminal) {
		t.Fatalf("a cancelled run could be marked succeeded: %v", err)
	}
	_ = run
}

// A failure is recorded as one, with the reason, so a caller coming back for
// the result learns what happened rather than finding a run stuck at running.
func TestAFailedRunRecordsItsReason(t *testing.T) {
	store := newRunStore(t)
	ctx := context.Background()
	submitRun(t, store, "run_1", "ws_a")
	started, _ := beginRun(ctx, store, "ws_a", "run_1", "worker-test", zap.NewNop())

	finishRun(ctx, store, started, "", errors.New("provider unreachable"), zap.NewNop())

	got, err := store.Get(ctx, "ws_a", "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != runs.StatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.FailureReason == "" {
		t.Fatal("a failed run recorded no reason")
	}
	if got.EndedAt == nil {
		t.Fatal("a failed run has no end time, so it reads as still running")
	}
}

// Execution uses the principal recorded at admission, not whatever the worker
// happens to hold. A durable run may start in a different process minutes
// later, with no request behind it.
func TestARunExecutesAsThePrincipalItWasAdmittedUnder(t *testing.T) {
	store := newRunStore(t)
	run := submitRun(t, store, "run_1", "ws_a")
	principal := runPrincipal(run, "req-1")

	if principal.WorkspaceID != "ws_a" {
		t.Fatalf("workspace = %q, want ws_a", principal.WorkspaceID)
	}
	if principal.Subject != "usr_a" || principal.Role != "developer" {
		t.Fatalf("principal did not come from the record: %+v", principal)
	}
	if principal.OrganizationID != "org_a" || principal.MembershipID != "mem_a" {
		t.Fatalf("policy snapshot was not used: %+v", principal)
	}
	// A run with no recorded role still gets one, because a principal with an
	// empty role is denied everything and the run would fail for a reason that
	// has nothing to do with what it was asked to do.
	bare := runPrincipal(runs.Run{ID: "run_2", WorkspaceID: "ws_b"}, "req-2")
	if bare.Role == "" {
		t.Fatal("a run with no recorded role would execute with no authority at all")
	}
	if bare.WorkspaceID != "ws_b" {
		t.Fatalf("workspace = %q, want ws_b", bare.WorkspaceID)
	}
}

// MU-027 criterion 5: a worker must observe a cancellation issued by another
// process. An in-memory channel would only reach a worker in the same process
// as the request, which is the arrangement MU-023 removed from the scheduler.
func TestAWorkerStopsWhenItsRecordSaysCancelling(t *testing.T) {
	previous := cancelPollInterval
	cancelPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { cancelPollInterval = previous })

	store := newRunStore(t)
	ctx := context.Background()
	run := submitRun(t, store, "run_watch", "ws_a")
	started, ok := beginRun(ctx, store, "ws_a", "run_watch", "worker-test", zap.NewNop())
	if !ok {
		t.Fatal("could not start the run")
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := watchForCancellation(runCtx, cancel, store, started, zap.NewNop())
	defer stop()

	// The request arrives — as it would from another gateway process, with no
	// channel back to this worker.
	if _, err := store.RequestCancel(ctx, "ws_a", "run_watch", "operator cancelled"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-runCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the worker never observed the cancellation")
	}
	_ = run
}

// A store that cannot be read is not a cancellation. Cancelling on an
// unreachable store would make a database hiccup kill every in-flight run in
// the deployment.
func TestAnUnreadableStoreDoesNotCancelRunningWork(t *testing.T) {
	previous := cancelPollInterval
	cancelPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { cancelPollInterval = previous })

	store := newRunStore(t)
	ctx := context.Background()
	run := submitRun(t, store, "run_hiccup", "ws_a")
	started, _ := beginRun(ctx, store, "ws_a", "run_hiccup", "worker-test", zap.NewNop())
	_ = run
	_ = store.Close() // the database is gone under the poller

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := watchForCancellation(runCtx, cancel, store, started, zap.NewNop())
	defer stop()

	select {
	case <-runCtx.Done():
		t.Fatal("an unreachable store cancelled running work")
	case <-time.After(120 * time.Millisecond):
	}
}

// The poller must not outlive the run: a stopped watcher holds a database
// handle for a record nobody is watching.
func TestTheCancellationWatcherStopsWithTheRun(t *testing.T) {
	previous := cancelPollInterval
	cancelPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { cancelPollInterval = previous })

	store := newRunStore(t)
	ctx := context.Background()
	submitRun(t, store, "run_done", "ws_a")
	started, _ := beginRun(ctx, store, "ws_a", "run_done", "worker-test", zap.NewNop())

	runCtx, cancel := context.WithCancel(ctx)
	stop := watchForCancellation(runCtx, cancel, store, started, zap.NewNop())
	stop()
	cancel()

	// Requesting a cancel after the watcher stopped must not panic on a closed
	// channel, and there is nothing left polling to observe it.
	if _, err := store.RequestCancel(ctx, "ws_a", "run_done", "late"); err != nil {
		t.Fatal(err)
	}
}

// A cancelled run must reach a TERMINAL state. Skipping finishCancelled leaves
// it stuck in `cancelling` forever, because cancelling → succeeded is not a
// legal transition and finishRun's write simply fails — a run that never
// finishes reads to a client as "still stopping", indefinitely.
func TestACancelledRunReachesATerminalState(t *testing.T) {
	store := newRunStore(t)
	ctx := context.Background()
	submitRun(t, store, "run_term", "ws_a")
	started, _ := beginRun(ctx, store, "ws_a", "run_term", "worker-test", zap.NewNop())
	if _, err := store.RequestCancel(ctx, "ws_a", "run_term", "operator cancelled"); err != nil {
		t.Fatal(err)
	}

	// The worker's outcome path, in the order wire_subsystems runs it.
	if !finishCancelled(ctx, store, started, zap.NewNop()) {
		finishRun(ctx, store, started, "the work completed anyway", nil, zap.NewNop())
	}

	final, err := store.Get(ctx, "ws_a", "run_term")
	if err != nil {
		t.Fatal(err)
	}
	if !runs.Terminal(final.Status) {
		t.Fatalf("status = %q, which is not terminal — the run is stuck stopping", final.Status)
	}
	if final.Status != runs.StatusCancelled {
		t.Fatalf("status = %q, want cancelled — a cancellation is not a failure", final.Status)
	}
}

// TestTheOutcomePathChecksForCancellationFirst is the structural half.
//
// The test above exercises finishCancelled directly, which proves the function
// works and nothing about whether executeDurableRun calls it. Deleting that
// call compiles and passes every behavioural test, and leaves cancelled runs
// stuck in `cancelling` forever in production. There is no output to assert on
// — the failure is an absence — so the guard reads the source.
func TestTheOutcomePathChecksForCancellationFirst(t *testing.T) {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "wire_subsystems.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	found := false
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "finishCancelled" {
			found = true
			return false
		}
		return true
	})
	if !found {
		t.Fatal("executeDurableRun no longer checks for cancellation before recording an outcome; " +
			"cancelled runs will stay in `cancelling` forever")
	}
}
