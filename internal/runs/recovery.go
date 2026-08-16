package runs

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/wsroot"
)

// recovery.go — MU-021 criterion 6: "worker loss causes bounded retry only for
// retry-safe stages; side-effecting calls are not repeated without idempotency
// proof or approval."
//
// A process that dies mid-run leaves records behind. There are two wrong ways
// to handle them and they fail in opposite directions:
//
//   - Re-queue everything. One crash becomes two payments, two emails, two
//     deletions. The run had already called out to the world; "retry" repeats
//     the call, and the world does not un-send things.
//   - Re-queue nothing. A run that had not yet done anything is retry-safe by
//     construction. Failing it loses work for no safety gain, and an operator
//     who restarts the gateway learns to expect losses.
//
// Neither is a judgement call the sweep can make from the status alone:
// `running` says a worker claimed the run, not that the run acted. So the
// engine records the first outside-visible call the run makes, and the sweep
// reads that. A run with no marker is re-queued; a run with one is failed with
// a reason naming the tool, because the only correct next step is a human's.
//
// APPROVAL, NOT PROHIBITION. Failing a side-effecting run is not the end of
// the story — the record keeps its payload, its principal and its policy
// snapshot, so an operator who confirms the effect did not land can resubmit
// it. What the sweep must not do is make that decision automatically.

// RecoveryOutcome is what the sweep decided about one run.
type RecoveryOutcome struct {
	Run    Run
	Action string
	Reason string
}

// Recovery actions.
const (
	// RecoveryRequeued: the run may run again. It never acted, or never
	// started.
	RecoveryRequeued = "requeued"
	// RecoveryFailedSideEffects: the run already acted. Repeating it needs a
	// human, so the record is closed with the reason rather than retried.
	RecoveryFailedSideEffects = "failed_side_effects"
	// RecoveryFailedExhausted: the run has used its attempts. A run that
	// reliably kills its worker must stop being handed to workers.
	RecoveryFailedExhausted = "failed_attempts_exhausted"
	// RecoverySkipped: paused runs are somebody's deliberate state, not
	// wreckage. The sweep leaves them alone.
	RecoverySkipped = "skipped"
)

// RecoverPending sweeps every workspace's unfinished runs and applies the
// policy above, returning what it did to each.
//
// Deployment-wide and named so it cannot be reached by accident, for the same
// reason RecoverAcrossWorkspaces is: a restart sweep has no request and
// therefore no tenant. Every decision is taken per run, against that run's own
// workspace — the sweep never inherits one workspace's context for all of them.
func (s *Store) RecoverPending(ctx context.Context) ([]RecoveryOutcome, error) {
	pending, err := s.RecoverAcrossWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	outcomes := make([]RecoveryOutcome, 0, len(pending))
	for _, run := range pending {
		outcomes = append(outcomes, s.recoverOne(ctx, run))
	}
	return outcomes, nil
}

func (s *Store) recoverOne(ctx context.Context, run Run) RecoveryOutcome {
	switch {
	case run.Status == StatusPaused:
		return RecoveryOutcome{Run: run, Action: RecoverySkipped,
			Reason: "paused runs are a deliberate state, not an interrupted one"}

	case run.SideEffectAt != nil:
		reason := fmt.Sprintf("worker lost after the run called %s; retrying could repeat it, so it needs review", sideEffectName(run))
		if updated, err := s.Transition(ctx, run.WorkspaceID, run.ID, StatusFailed,
			TransitionOptions{FailureReason: reason}); err == nil {
			run = updated
		}
		return RecoveryOutcome{Run: run, Action: RecoveryFailedSideEffects, Reason: reason}

	case run.Attempt >= effectiveMaxAttempts(run):
		reason := fmt.Sprintf("worker lost after %d of %d permitted attempts; none remain", run.Attempt, effectiveMaxAttempts(run))
		if updated, err := s.Transition(ctx, run.WorkspaceID, run.ID, StatusFailed,
			TransitionOptions{FailureReason: reason}); err == nil {
			run = updated
		}
		return RecoveryOutcome{Run: run, Action: RecoveryFailedExhausted, Reason: reason}

	default:
		reason := fmt.Sprintf("worker lost before the run acted; re-queued with %d of %d attempts used",
			run.Attempt, effectiveMaxAttempts(run))
		if updated, err := s.requeue(ctx, run); err == nil {
			run = updated
		} else {
			return RecoveryOutcome{Run: run, Action: RecoverySkipped, Reason: err.Error()}
		}
		return RecoveryOutcome{Run: run, Action: RecoveryRequeued, Reason: reason}
	}
}

// requeue returns a run to `queued`.
//
// Deliberately NOT expressed as a transition in the state machine. Adding
// running → queued to that table would make demotion reachable from anywhere,
// including from a live worker's own code path, and "a run went backwards"
// would stop being a contradiction the machine can catch. The sweep is the one
// caller allowed to do it, so it owns the SQL — and the WHERE clause still
// repeats every fact it read, so a worker that comes back to life and claims
// the run first wins and this UPDATE matches nothing.
//
// The attempt counter is NOT touched here. It counts claims, and re-queueing
// is not a claim; incrementing in both places would burn two attempts per
// crash and silently halve the bound.
func (s *Store) requeue(ctx context.Context, run Run) (Run, error) {
	workspaceID := wsroot.Normalize(run.WorkspaceID)
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
UPDATE agent_runs
   SET status = ?, updated_at = ?, started_at = NULL
 WHERE workspace_id = ? AND id = ? AND status = ? AND attempt = ? AND side_effect_at IS NULL`,
		StatusQueued, now, workspaceID, run.ID, run.Status, run.Attempt)
	if err != nil {
		return Run{}, err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return Run{}, fmt.Errorf("runs: %s changed while it was being recovered", run.ID)
	}
	return s.Get(ctx, workspaceID, run.ID)
}

// MarkSideEffect records that a run has made its first outside-visible call.
//
// Idempotent by SQL rather than by a read-then-write: dispatch reports EVERY
// side-effecting call, not only the first, and the first is the one that
// matters. `side_effect_at IS NULL` in the WHERE keeps the earliest timestamp
// and the tool that earned it.
//
// A no-op for an unknown run, deliberately. The engine calls this for every
// side-effecting tool in the process, most of which belong to chat requests
// with no durable record at all; making absence an error would turn the
// ordinary case into a log full of failures.
func (s *Store) MarkSideEffect(ctx context.Context, workspaceID, id, tool string) error {
	workspaceID = wsroot.Normalize(workspaceID)
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE agent_runs
   SET side_effect_at = ?, side_effect_tool = ?, updated_at = ?
 WHERE workspace_id = ? AND id = ? AND side_effect_at IS NULL`,
		time.Now().UTC(), strings.TrimSpace(tool), time.Now().UTC(), workspaceID, id)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	return nil
}

func effectiveMaxAttempts(run Run) int {
	if run.MaxAttempts > 0 {
		return run.MaxAttempts
	}
	return DefaultMaxAttempts
}

func sideEffectName(run Run) string {
	if tool := strings.TrimSpace(run.SideEffectTool); tool != "" {
		return tool
	}
	return "a side-effecting tool"
}
