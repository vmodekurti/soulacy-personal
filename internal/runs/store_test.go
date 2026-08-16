// store_test.go — MU-020: a run is a durable, addressable, workspace-owned
// record with a validated state machine and idempotent submission.
package runs

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/soulacy/soulacy/internal/wsroot"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func submit(t *testing.T, store *Store, id, workspaceID, agentID string) Run {
	t.Helper()
	run, _, err := store.Submit(context.Background(), Run{
		ID: id, WorkspaceID: workspaceID, AgentID: agentID,
		AgentVersion: "v1", Subject: "usr_a", PrincipalKind: "user",
	})
	if err != nil {
		t.Fatalf("submit %s: %v", id, err)
	}
	return run
}

// A submission records the facts of admission, and hands back the two things a
// client needs to come back later: the run's own ID and where its events start.
func TestASubmittedRunRecordsTheFactsOfItsAdmission(t *testing.T) {
	store := newStore(t)
	policy := json.RawMessage(`{"role":"developer","scopes":["agents:run"]}`)
	run, replayed, err := store.Submit(context.Background(), Run{
		ID: "run_1", WorkspaceID: "ws_a", AgentID: "researcher", AgentVersion: "v7",
		Subject: "usr_a", PrincipalKind: "user", CredentialID: "cred_a",
		PolicySnapshot: policy, ReservationID: "res_1",
		Payload: json.RawMessage(`{"prompt":"go"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed {
		t.Fatal("a first submission was reported as a replay")
	}
	if run.Status != StatusQueued {
		t.Fatalf("status = %q, want queued", run.Status)
	}
	if run.Cursor == "" {
		t.Fatal("no event cursor was issued, so a client that disconnects cannot resume")
	}
	got, err := store.Get(context.Background(), "ws_a", "run_1")
	if err != nil {
		t.Fatal(err)
	}
	// Each of these is a fact about the moment of admission that cannot be
	// reconstructed later: the agent may be edited, the grant revoked, the
	// budget spent.
	if got.AgentVersion != "v7" {
		t.Errorf("agent version = %q — a later edit would change what this run means", got.AgentVersion)
	}
	if got.Subject != "usr_a" || got.PrincipalKind != "user" || got.CredentialID != "cred_a" {
		t.Errorf("principal not recorded: %+v", got)
	}
	if string(got.PolicySnapshot) != string(policy) {
		t.Errorf("policy snapshot = %s", got.PolicySnapshot)
	}
	if got.ReservationID != "res_1" {
		t.Errorf("budget reservation not recorded: %q", got.ReservationID)
	}
}

// A client that retries because it never saw the response must end up with one
// run and its ID — not a conflict it has to interpret.
func TestRetryingAnIdempotencyKeyReturnsTheSameRun(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	first, replayed, err := store.Submit(ctx, Run{
		ID: "run_1", WorkspaceID: "ws_a", AgentID: "researcher", IdempotencyKey: "daily-report",
	})
	if err != nil || replayed {
		t.Fatalf("first submit: replayed=%v err=%v", replayed, err)
	}
	second, replayed, err := store.Submit(ctx, Run{
		ID: "run_2", WorkspaceID: "ws_a", AgentID: "researcher", IdempotencyKey: "daily-report",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !replayed {
		t.Fatal("a repeated idempotency key was not reported as a replay")
	}
	if second.ID != first.ID {
		t.Fatalf("the retry created a second run: %s vs %s", second.ID, first.ID)
	}
	all, err := store.List(ctx, "ws_a", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("workspace has %d runs, want 1", len(all))
	}
}

// Idempotency keys are client-chosen, so two tenants picking "daily-report" is
// ordinary. A global constraint would hand the second one the first one's run —
// both a leak and a lost submission.
func TestIdempotencyKeysDoNotCollideAcrossWorkspaces(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	a, _, err := store.Submit(ctx, Run{ID: "run_a", WorkspaceID: "ws_a", AgentID: "bot", IdempotencyKey: "daily-report"})
	if err != nil {
		t.Fatal(err)
	}
	b, replayed, err := store.Submit(ctx, Run{ID: "run_b", WorkspaceID: "ws_b", AgentID: "bot", IdempotencyKey: "daily-report"})
	if err != nil {
		t.Fatal(err)
	}
	if replayed {
		t.Fatal("another workspace's idempotency key was treated as this one's")
	}
	if b.ID == a.ID {
		t.Fatal("two workspaces were given the same run")
	}
}

// A run belongs to one workspace, and a neighbour's run is reported exactly as
// a nonexistent one (product invariant 8).
func TestARunIsOnlyVisibleToItsOwnWorkspace(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	submit(t, store, "run_a", "ws_a", "bot")
	submit(t, store, "run_b", "ws_b", "bot")

	if _, err := store.Get(ctx, "ws_b", "run_a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace Get returned %v, want ErrNotFound", err)
	}
	if _, err := store.Get(ctx, "ws_b", "no-such-run"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing run returned %v, want ErrNotFound", err)
	}
	listed, err := store.List(ctx, "ws_a", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != "run_a" {
		t.Fatalf("listing crossed the tenant boundary: %+v", listed)
	}
	// A transition from the wrong workspace must also miss.
	if _, err := store.Transition(ctx, "ws_b", "run_a", StatusCancelled, TransitionOptions{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a neighbour cancelled another workspace's run: %v", err)
	}
}

// The state machine is closed, and terminal means terminal. A cancelled run
// resurrected into running would execute work a person explicitly stopped.
func TestAFinishedRunNeverRunsAgain(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	submit(t, store, "run_1", "ws_a", "bot")

	if _, err := store.Transition(ctx, "ws_a", "run_1", StatusRunning, TransitionOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, "ws_a", "run_1", StatusCancelled, TransitionOptions{FailureReason: "user cancelled"}); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{StatusRunning, StatusQueued, StatusSucceeded, StatusPaused} {
		if _, err := store.Transition(ctx, "ws_a", "run_1", to, TransitionOptions{}); !errors.Is(err, ErrTerminal) {
			t.Errorf("cancelled → %s returned %v, want ErrTerminal", to, err)
		}
	}
	final, err := store.Get(ctx, "ws_a", "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != StatusCancelled || final.EndedAt == nil {
		t.Fatalf("terminal run was mutated: %+v", final)
	}
}

// Skipping states is refused rather than applied: a run that reports success
// without ever running is a lie an audit cannot detect afterwards.
func TestInvalidTransitionsAreRefused(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	submit(t, store, "run_1", "ws_a", "bot")

	if _, err := store.Transition(ctx, "ws_a", "run_1", StatusSucceeded, TransitionOptions{}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("queued → succeeded returned %v, want ErrInvalidTransition", err)
	}
	if _, err := store.Transition(ctx, "ws_a", "run_1", StatusPaused, TransitionOptions{}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("queued → paused returned %v, want ErrInvalidTransition", err)
	}
	if _, err := store.Transition(ctx, "ws_a", "run_1", "invented", TransitionOptions{}); err == nil {
		t.Fatal("an unknown status was accepted")
	}
	got, err := store.Get(ctx, "ws_a", "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusQueued {
		t.Fatalf("a refused transition still changed the status to %q", got.Status)
	}
}

// Two workers racing to finish the same run: exactly one wins, and the loser is
// told rather than silently overwriting the winner's result.
func TestConcurrentTransitionsElectOneWinner(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	for round := 0; round < 8; round++ {
		id := "run_race_" + string(rune('a'+round))
		submit(t, store, id, "ws_a", "bot")
		if _, err := store.Transition(ctx, "ws_a", id, StatusRunning, TransitionOptions{}); err != nil {
			t.Fatal(err)
		}

		const racers = 6
		var wg sync.WaitGroup
		results := make([]error, racers)
		start := make(chan struct{})
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				<-start
				_, results[index] = store.Transition(ctx, "ws_a", id, StatusSucceeded,
					TransitionOptions{Result: "from racer"})
			}(i)
		}
		close(start)
		wg.Wait()

		winners := 0
		for _, err := range results {
			if err == nil {
				winners++
				continue
			}
			if !errors.Is(err, ErrTerminal) && !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("round %d: a loser failed for the wrong reason: %v", round, err)
			}
		}
		if winners != 1 {
			t.Fatalf("round %d: %d racers won the same transition", round, winners)
		}
	}
}

// A restart has no request and therefore no tenant, so the recovery sweep is
// deployment-wide — and every row carries its own workspace so the resumer acts
// per run instead of inheriting one for all of them.
func TestRecoverySeesEveryTenantsUnfinishedRuns(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	submit(t, store, "run_a", "ws_a", "bot")
	submit(t, store, "run_b", "ws_b", "bot")
	submit(t, store, "run_done", wsroot.PersonalWorkspaceID, "bot")
	if _, err := store.Transition(ctx, wsroot.PersonalWorkspaceID, "run_done", StatusRunning, TransitionOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, wsroot.PersonalWorkspaceID, "run_done", StatusSucceeded, TransitionOptions{Result: "ok"}); err != nil {
		t.Fatal(err)
	}

	pending, err := store.RecoverAcrossWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, run := range pending {
		seen[run.ID] = run.WorkspaceID
	}
	if len(pending) != 2 {
		t.Fatalf("recovery returned %d runs, want 2: %v", len(pending), seen)
	}
	if seen["run_a"] != "ws_a" || seen["run_b"] != "ws_b" {
		t.Fatalf("recovered runs do not carry their own workspace: %v", seen)
	}
	if _, resurrected := seen["run_done"]; resurrected {
		t.Fatal("a finished run was offered for recovery")
	}
}

// The record survives the process. That is the whole point of MU-020: a
// restart used to lose every queued and running job.
func TestRunsSurviveAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, _, err := first.Submit(ctx, Run{
		ID: "run_1", WorkspaceID: "ws_a", AgentID: "bot", IdempotencyKey: "k",
		Payload: json.RawMessage(`{"prompt":"go"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Transition(ctx, "ws_a", "run_1", StatusRunning, TransitionOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	got, err := second.Get(ctx, "ws_a", "run_1")
	if err != nil {
		t.Fatalf("a run did not survive the restart: %v", err)
	}
	if got.Status != StatusRunning || string(got.Payload) != `{"prompt":"go"}` {
		t.Fatalf("run state changed across the restart: %+v", got)
	}
	// The idempotency record survives too, so a client retrying after the
	// restart still does not start the work twice.
	_, replayed, err := second.Submit(ctx, Run{ID: "run_2", WorkspaceID: "ws_a", AgentID: "bot", IdempotencyKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if !replayed {
		t.Fatal("idempotency did not survive the restart")
	}
}
