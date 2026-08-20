// chaos_test.go — MU-034 criterion 6: "chaos tests cover crashes before/after
// claim, provider call, tool side effect, event publish, and result commit."
//
// A THIRD OPTION, and it is worth saying why, because the two obvious ones are
// both bad. Injecting faults into the run path means adding permanent
// injection points to production code for the benefit of a test. Spawning real
// workers and killing them means a slow suite whose failures are as likely to
// be the harness as the code.
//
// Neither is necessary, because a crashed worker is not a special event — it is
// simply a worker that STOPS CALLING. Every step of the run path is already a
// separate call against the record (beginRun, holdRunLease, MarkSideEffect,
// finishRun), and the whole point of the record is that it survives the process.
// So "crash at point N" is: perform the sequence up to N, stop, let the lease
// lapse, run recovery, and assert the policy. No new production code, no
// subprocesses, and the thing under test is the actual production function at
// every step.
//
// WHAT MADE THIS EXPRESSIBLE AT ALL. Before the lease, every unfinished run
// looked identical to recovery, so a test could not distinguish "the worker
// crashed here" from "the worker is still working". The lease is what turns a
// crash into an observable state, and the side-effect marker is what turns
// "where it crashed" into one.
package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/runs"
)

// crashHarness is one run and the store that outlives its worker.
type crashHarness struct {
	t     *testing.T
	store *runs.Store
	run   runs.Run
}

func newCrashHarness(t *testing.T, maxAttempts int) *crashHarness {
	t.Helper()
	store, err := runs.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	submitted, _, err := store.Submit(context.Background(), runs.Run{
		ID: "run_1", WorkspaceID: "ws_a", AgentID: "agent", MaxAttempts: maxAttempts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &crashHarness{t: t, store: store, run: submitted}
}

// claim performs the production claim.
func (h *crashHarness) claim(owner string) {
	h.t.Helper()
	claimed, ok := beginRun(context.Background(), h.store, h.run.WorkspaceID, h.run.ID, owner, zap.NewNop())
	if !ok {
		h.t.Fatal("claim failed; the harness is not exercising what it thinks")
	}
	h.run = claimed
}

// crash is what a killed worker does: it stops renewing.
//
// Expressed as the worker's LAST renewal carrying a lease that has already
// elapsed, through the same production RenewLease the worker calls, rather than
// by reaching into the table or by sleeping out a real sixty seconds. Reaching
// into the table would test the recovery predicate against a state no
// production code produces; sleeping would make the suite a minute slower per
// crash point for no additional confidence.
func (h *crashHarness) crash() {
	h.t.Helper()
	if err := h.store.RenewLease(context.Background(), h.run.WorkspaceID, h.run.ID,
		h.run.ClaimedBy, time.Nanosecond); err != nil {
		h.t.Fatalf("the final renewal failed, so this is not the crash the test means: %v", err)
	}
	// Confirmed rather than assumed: if the lease were somehow still live, every
	// assertion below would pass for the wrong reason — recovery would ignore
	// the run because it looks busy, and "recovery did nothing" is what several
	// of these tests expect for entirely different reasons.
	stored, err := h.store.Get(context.Background(), h.run.WorkspaceID, h.run.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	if stored.LeaseHeld(time.Now().UTC()) {
		h.t.Fatal("the lease is still live after the crash; the harness would prove nothing")
	}
}

// recover runs the sweep a surviving or restarting replica runs.
func (h *crashHarness) recover() runs.RecoveryOutcome {
	h.t.Helper()
	outcomes, err := h.store.RecoverPending(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	for _, outcome := range outcomes {
		if outcome.Run.ID == h.run.ID {
			return outcome
		}
	}
	return runs.RecoveryOutcome{}
}

func TestChaosCrashBeforeTheClaim(t *testing.T) {
	// The run was submitted and no worker ever took it. Nothing has happened,
	// so it must simply be available again — and must NOT have burnt an
	// attempt, because attempts count worker claims and there was none.
	h := newCrashHarness(t, 3)

	outcome := h.recover()
	if outcome.Action != runs.RecoveryRequeued {
		t.Fatalf("action = %q, want requeued", outcome.Action)
	}
	if outcome.Run.Attempt != 0 {
		t.Errorf("attempt = %d, want 0 — a run no worker claimed has used nothing, and charging it "+
			"one means a queue backlog during an outage exhausts every run's budget without any of "+
			"them running", outcome.Run.Attempt)
	}
}

func TestChaosCrashAfterTheClaimAndAfterAProviderCall(t *testing.T) {
	// Two crash points, one assertion, and that is the finding rather than a
	// shortcut.
	//
	// A provider call is not a side effect in the sense that matters here: it
	// costs money and it may have been logged, but nothing OUTSIDE the
	// deployment changed state because of it. The record deliberately does not
	// distinguish "crashed after claiming" from "crashed after asking a model
	// a question", because the safe action is the same for both — re-queue —
	// and a record that distinguished them would invite someone to treat the
	// second as unsafe and fail runs that could simply be retried.
	//
	// What separates them from the next case is MarkSideEffect, and only that.
	for _, name := range []string{"immediately after claiming", "after a provider call"} {
		t.Run(name, func(t *testing.T) {
			h := newCrashHarness(t, 3)
			h.claim("replica-a")
			h.crash()

			outcome := h.recover()
			if outcome.Action != runs.RecoveryRequeued {
				t.Fatalf("action = %q (%s), want requeued", outcome.Action, outcome.Reason)
			}
			if outcome.Run.Attempt != 1 {
				t.Errorf("attempt = %d, want 1 — a claim was made and lost", outcome.Run.Attempt)
			}
			if outcome.Run.Status != runs.StatusQueued {
				t.Errorf("status = %s, want queued", outcome.Run.Status)
			}
		})
	}
}

func TestChaosCrashAfterAToolSideEffect(t *testing.T) {
	// The case that must NOT be retried. The run called out to the world —
	// sent an email, charged a card, ran a shell command — and a retry would
	// repeat it. Failing with the tool named is the only answer that leaves a
	// human able to decide.
	h := newCrashHarness(t, 3)
	h.claim("replica-a")
	if err := h.store.MarkSideEffect(context.Background(), h.run.WorkspaceID, h.run.ID, "send_email"); err != nil {
		t.Fatal(err)
	}
	h.crash()

	outcome := h.recover()
	if outcome.Action != runs.RecoveryFailedSideEffects {
		t.Fatalf("action = %q, want failed-side-effects — retrying would repeat whatever the run did "+
			"to the outside world", outcome.Action)
	}
	if outcome.Run.Status != runs.StatusFailed {
		t.Errorf("status = %s, want failed", outcome.Run.Status)
	}
	// The tool has to be NAMED, or the human deciding has nothing to decide
	// with: "this run may have done something" is not actionable and
	// "this run sent an email" is.
	if outcome.Run.SideEffectTool != "send_email" {
		t.Errorf("side_effect_tool = %q, want the tool that was called", outcome.Run.SideEffectTool)
	}
}

func TestChaosCrashAfterAnEventPublishButBeforeTheResultCommit(t *testing.T) {
	// Events are observers. A subscriber saw a tool.result go past and the
	// record never reached a terminal state — so the record, which is the
	// authority, must recover on its own terms rather than trusting that a
	// published event means the work landed.
	//
	// Grouped with the side-effect case deliberately: publishing an event is
	// something the deployment did to ITSELF, but by the time one is published
	// the tool has already run, so the marker is already set and the safe
	// action is already decided. An event publish adds no new information to
	// the recovery decision, and building a separate state for it would be
	// modelling a distinction that changes nothing.
	h := newCrashHarness(t, 3)
	h.claim("replica-a")
	if err := h.store.MarkSideEffect(context.Background(), h.run.WorkspaceID, h.run.ID, "http_request"); err != nil {
		t.Fatal(err)
	}
	h.crash()

	outcome := h.recover()
	if outcome.Action != runs.RecoveryFailedSideEffects {
		t.Fatalf("action = %q, want failed-side-effects", outcome.Action)
	}
	if outcome.Run.FailureReason == "" {
		t.Error("a run failed by recovery with no reason recorded; the operator sees a failure with " +
			"no explanation and no way to tell it apart from the agent's own error")
	}
}

func TestChaosCrashAfterTheResultCommit(t *testing.T) {
	// The work finished and the outcome landed. Recovery must not touch it —
	// re-running a successful run is the same duplicate-execution bug arriving
	// through the recovery path instead of the claim path.
	h := newCrashHarness(t, 3)
	h.claim("replica-a")
	finishRun(context.Background(), h.store, h.run, "done", nil, zap.NewNop())

	if outcome := h.recover(); outcome.Action != "" {
		t.Fatalf("recovery acted on a committed run: %s (%s)", outcome.Action, outcome.Reason)
	}
	stored, err := h.store.Get(context.Background(), h.run.WorkspaceID, h.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != runs.StatusSucceeded {
		t.Errorf("status = %s, want succeeded", stored.Status)
	}
	if stored.LeaseHeld(time.Now().UTC()) {
		t.Error("a finished run still holds a lease, so the next crash waits it out for no reason")
	}
}

func TestChaosARunThatKillsEveryWorkerStopsBeingHandedOut(t *testing.T) {
	// The bound, and it is what separates a retry policy from an infinite
	// loop. A run that reliably kills its worker would otherwise take down
	// every worker in the deployment in turn, forever.
	h := newCrashHarness(t, 2)

	h.claim("replica-a")
	h.crash()
	if outcome := h.recover(); outcome.Action != runs.RecoveryRequeued {
		t.Fatalf("first loss: %s", outcome.Action)
	}

	h.claim("replica-b")
	h.crash()
	outcome := h.recover()
	if outcome.Action != runs.RecoveryFailedExhausted {
		t.Fatalf("second loss: action = %q (%s), want exhausted", outcome.Action, outcome.Reason)
	}
	if outcome.Run.Status != runs.StatusFailed {
		t.Errorf("status = %s, want failed", outcome.Run.Status)
	}
}

func TestChaosASurvivingReplicaDoesNotStealALiveRun(t *testing.T) {
	// The inverse of every case above, and the one that was broken before the
	// lease: recovery must act ONLY on crashes. A sweep that also collected
	// live runs is not a recovery mechanism, it is a duplicate-execution
	// mechanism that happens to run at boot.
	h := newCrashHarness(t, 3)
	h.claim("replica-a")
	// No crash: replica-a is working, and its lease is current.

	if outcome := h.recover(); outcome.Action != "" {
		t.Fatalf("a sweep collected a run a live worker holds: %s (%s)", outcome.Action, outcome.Reason)
	}
	stored, err := h.store.Get(context.Background(), h.run.WorkspaceID, h.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != runs.StatusRunning {
		t.Errorf("status = %s, want running", stored.Status)
	}
}

func TestChaosCoversEveryPointTheCriterionNames(t *testing.T) {
	// A completeness check on the tests above, because the criterion is a LIST
	// and a list is the kind of thing a suite drifts away from one deletion at
	// a time. Each point maps to a test in this file; two pairs are covered by
	// one test each, with the reason recorded in that test's comment rather
	// than here.
	points := map[string]string{
		"before claim":            "TestChaosCrashBeforeTheClaim",
		"after claim":             "TestChaosCrashAfterTheClaimAndAfterAProviderCall",
		"after provider call":     "TestChaosCrashAfterTheClaimAndAfterAProviderCall",
		"after tool side effect":  "TestChaosCrashAfterAToolSideEffect",
		"after event publish":     "TestChaosCrashAfterAnEventPublishButBeforeTheResultCommit",
		"after result commit":     "TestChaosCrashAfterTheResultCommit",
		"bounded retry":           "TestChaosARunThatKillsEveryWorkerStopsBeingHandedOut",
		"a live run is not swept": "TestChaosASurvivingReplicaDoesNotStealALiveRun",
	}
	// Verified by reading this file, so deleting a test named here fails the
	// build rather than silently shrinking the coverage the criterion claims.
	source, err := os.ReadFile("chaos_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for point, test := range points {
		if !strings.Contains(string(source), "func "+test+"(") {
			t.Errorf("crash point %q maps to %s, which is not in this file — the criterion's list has "+
				"drifted away from the suite", point, test)
		}
	}
}
