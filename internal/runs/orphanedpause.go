package runs

import (
	"context"
	"fmt"

	"github.com/soulacy/soulacy/internal/wsroot"
)

// orphanedpause.go — a paused run whose approval was closed under it.
//
// THE BUG THIS CLOSES. Two boot-time sweeps each behaved correctly on their
// own and produced, together, a state neither would have chosen.
//
// The run sweep skips `paused` runs, on the reasoning that a pause is somebody's
// deliberate state rather than wreckage — see recoverOne. The approvals sweep
// closes every pending approval, on the reasoning that a restart severs the
// channel the paused run was waiting on, so the question is one nobody is
// listening for the answer to — see approvals.InvalidateAllPending.
//
// Both are right. The run stays `paused`, its approval becomes `invalidated`,
// and nothing is left that can move either: the approvals page shows nothing to
// decide, the runs page shows a run waiting on a decision that can never be
// made, and no error is logged anywhere because both halves succeeded. It sits
// there until somebody deletes the row.
//
// The premise the run sweep relies on — that a pause is deliberate and someone
// can still release it — stops holding at the exact moment the approval is
// invalidated. So the approval sweep tells this function which runs it
// orphaned, and they are recovered as what they now are: interrupted.

// ResolveOrphanedPause moves one run out of `paused` after the approval that
// was holding it was invalidated.
//
// It applies the SAME policy as the crash sweep rather than a special one,
// because the questions are identical: a run that already acted on the world
// must not be repeated without a human, a run that has burnt its attempts must
// stop being handed to workers, and anything else may run again. Duplicating
// that decision here with different thresholds is how two recovery paths come
// to disagree about the same run.
//
// A run that is no longer paused — released, cancelled, or claimed by a live
// worker between the two sweeps — is left exactly as it is and reported as
// skipped.
func (s *Store) ResolveOrphanedPause(ctx context.Context, workspaceID, runID, reason string) (RecoveryOutcome, error) {
	run, err := s.Get(ctx, wsroot.Normalize(workspaceID), runID)
	if err != nil {
		return RecoveryOutcome{}, err
	}
	if run.Status != StatusPaused {
		return RecoveryOutcome{Run: run, Action: RecoverySkipped,
			Reason: "no longer paused when the orphaned-approval sweep reached it"}, nil
	}
	if reason == "" {
		reason = "the approval holding this run was invalidated"
	}

	switch {
	case run.SideEffectAt != nil:
		failure := fmt.Sprintf("%s; the run had already called %s, so repeating it needs review",
			reason, sideEffectName(run))
		if updated, terr := s.Transition(ctx, run.WorkspaceID, run.ID, StatusFailed,
			TransitionOptions{FailureReason: failure}); terr == nil {
			run = updated
		}
		return RecoveryOutcome{Run: run, Action: RecoveryFailedSideEffects, Reason: failure}, nil

	case run.Attempt >= effectiveMaxAttempts(run):
		failure := fmt.Sprintf("%s, and %d of %d permitted attempts are used; none remain",
			reason, run.Attempt, effectiveMaxAttempts(run))
		if updated, terr := s.Transition(ctx, run.WorkspaceID, run.ID, StatusFailed,
			TransitionOptions{FailureReason: failure}); terr == nil {
			run = updated
		}
		return RecoveryOutcome{Run: run, Action: RecoveryFailedExhausted, Reason: failure}, nil

	default:
		requeued, rerr := s.requeue(ctx, run)
		if rerr != nil {
			// Lost a race with something that moved the run. Leaving it alone
			// is correct: whatever won knows more about it than this sweep.
			return RecoveryOutcome{Run: run, Action: RecoverySkipped, Reason: rerr.Error()}, nil
		}
		return RecoveryOutcome{Run: requeued, Action: RecoveryRequeued,
			Reason: reason + "; re-queued so the approval is asked again"}, nil
	}
}
