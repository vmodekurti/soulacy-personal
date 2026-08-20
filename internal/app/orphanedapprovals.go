package app

import (
	"context"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/approvals"
	"github.com/soulacy/soulacy/internal/runs"
)

// resolveOrphanedPauses moves runs out of `paused` after the approvals holding
// them were invalidated by this restart.
//
// Returns the outcomes so requeued runs join the same re-enqueue loop the crash
// sweep's runs use: setting the record back to `queued` without re-delivering a
// message would leave it queued forever, which reads as "waiting its turn" and
// means "abandoned" — the same trap the crash path already documents.
func (a *App) resolveOrphanedPauses(ctx context.Context, store *runs.Store, blocked []approvals.RunRef) []runs.RecoveryOutcome {
	if store == nil || len(blocked) == 0 {
		return nil
	}
	outcomes := make([]runs.RecoveryOutcome, 0, len(blocked))
	counts := map[string]int{}
	for _, ref := range blocked {
		outcome, err := store.ResolveOrphanedPause(ctx, ref.WorkspaceID, ref.RunID,
			"the approval holding this run was closed when the process restarted")
		if err != nil {
			// A missing run record is the common case: an approval can gate a
			// call that was never a durable run. Logged at debug rather than
			// warn so a normal boot is not noisy, but logged, because the
			// alternative is a silent skip.
			a.log.Debug("run blocked on a stale approval could not be resolved",
				zap.String("workspace_id", ref.WorkspaceID),
				zap.String("run_id", ref.RunID),
				zap.Error(err))
			continue
		}
		counts[outcome.Action]++
		if outcome.Action == runs.RecoveryFailedSideEffects {
			// Named individually for the same reason the crash sweep names
			// them: somebody has to check whether the effect landed, and a
			// count does not say where to look.
			a.log.Warn("run needs review after its approval was invalidated by a restart",
				zap.String("run_id", outcome.Run.ID),
				zap.String("workspace_id", outcome.Run.WorkspaceID),
				zap.String("agent_id", outcome.Run.AgentID),
				zap.String("reason", outcome.Reason))
		}
		outcomes = append(outcomes, outcome)
	}
	if len(outcomes) > 0 {
		a.log.Info("runs left paused on approvals this restart closed were resolved",
			zap.Int("requeued", counts[runs.RecoveryRequeued]),
			zap.Int("needs_review", counts[runs.RecoveryFailedSideEffects]),
			zap.Int("attempts_exhausted", counts[runs.RecoveryFailedExhausted]),
			zap.Int("skipped", counts[runs.RecoverySkipped]))
	}
	return outcomes
}
