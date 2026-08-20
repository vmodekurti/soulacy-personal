package approvals

import (
	"context"
	"fmt"
	"strings"
)

// pendingruns.go — which runs an invalidation is about to strand.
//
// InvalidateAllPending returns a count, which is the right answer for a log
// line and useless for the caller that has to repair what it did. A run
// blocked on an approval that just became `invalidated` cannot be released by
// anybody; the boot path needs the list so it can move those runs out of
// `paused` (see runs.Store.ResolveOrphanedPause), and a count cannot say which.

// RunRef identifies one run that has at least one pending approval.
type RunRef struct {
	WorkspaceID string
	RunID       string
}

// PendingRunRefs lists every run currently blocked on a pending approval,
// across every workspace.
//
// Deployment-wide and named so it cannot be reached by accident, for the same
// reason InvalidateAllPending is: a restart has no request and therefore no
// tenant. Each row carries its own workspace so the caller acts per run.
//
// Approvals with no run_id are skipped: they block a call that was never
// recorded as a durable run, so there is no run record to repair.
func (s *Store) PendingRunRefs(ctx context.Context) ([]RunRef, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT workspace_id, run_id FROM tool_approvals WHERE status = ? AND run_id <> ''`,
		StatusPending)
	if err != nil {
		return nil, fmt.Errorf("approvals: pending run refs: %w", err)
	}
	defer rows.Close()
	var out []RunRef
	for rows.Next() {
		var ref RunRef
		if err := rows.Scan(&ref.WorkspaceID, &ref.RunID); err != nil {
			return nil, fmt.Errorf("approvals: pending run refs: %w", err)
		}
		if strings.TrimSpace(ref.RunID) == "" {
			continue
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("approvals: pending run refs: %w", err)
	}
	return out, nil
}
