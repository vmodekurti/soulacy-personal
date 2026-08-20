package gateway

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/internal/workspacepurge"
)

// workspace_purge_sweep.go — the thing that makes a requested deletion
// actually happen.
//
// Without this the product has a deletion API that records an intention and
// never acts on it: the workspace sits at `deleting`, refusing writes, with
// its data intact, forever. That failure is worse than having no delete
// endpoint at all, because the customer has been told their data will be gone
// on a date, and the only thing that would reveal otherwise is somebody
// inspecting the database.

const (
	// purgeSweepInterval is how often the gateway looks for a closed recovery
	// window. Minutes rather than seconds because a recovery window is
	// measured in days: being five minutes late to a deadline set 168 hours
	// ago changes nothing, and a tight loop against the tenancy database buys
	// nothing for it.
	purgeSweepInterval = 5 * time.Minute
	// purgeLease bounds how long one instance's claim on a workspace lasts.
	//
	// Long enough that a large workspace's purge finishes inside it; short
	// enough that an instance killed mid-purge does not strand the workspace
	// for hours. The cost of it expiring early is a duplicated purge, which is
	// safe because every purger is idempotent — a DELETE of rows already gone
	// and a RemoveAll of a directory already removed both succeed.
	purgeLease = 30 * time.Minute
	// purgeBatch bounds how many workspaces one tick attempts, so a backlog is
	// worked through steadily rather than in one burst that competes with
	// live traffic for the same database.
	purgeBatch = 20
)

// StartWorkspacePurgeSweep runs the purge sweep until ctx is done.
//
// Started by application wiring, not by New: a directly constructed or
// embedded gateway — which is what every test builds — must not acquire a
// goroutine that deletes workspaces.
func (s *Server) StartWorkspacePurgeSweep(ctx context.Context) {
	if s.workspaceLifecycle == nil {
		return
	}
	ticker := time.NewTicker(purgeSweepInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sweepDueWorkspacePurges(ctx, time.Now().UTC())
			}
		}
	}()
}

// sweepDueWorkspacePurges purges every workspace whose window has closed.
//
// Exported behaviour is deliberately reported at INFO on success and ERROR on
// an incomplete purge, rather than the other way round. A completed deletion is
// an ordinary event; a deletion that left data behind is the one somebody has
// to act on, and it is invisible in every other place — the customer sees a
// deleted workspace and the API returns nothing further.
func (s *Server) sweepDueWorkspacePurges(ctx context.Context, now time.Time) int {
	if s.workspaceLifecycle == nil {
		return 0
	}
	// DueForPurge is a SELECTOR, not the correctness guard. It exists so a
	// sweep does not read every workspace on every tick; the guard that a
	// window has actually closed is inside PurgeWorkspaceIfDue, which
	// re-checks `PurgeDue` against the row it reads. Mutation testing makes
	// the asymmetry visible — replacing this query with any list that includes
	// the right workspace leaves every test passing — and saying so here is
	// better than leaving the next reader to assume two independent checks.
	due, err := s.workspaceLifecycle.DueForPurge(ctx, now, purgeBatch)
	if err != nil {
		s.logger().Error("workspace purge sweep could not list due deletions", zap.Error(err))
		return 0
	}
	purged := 0
	for _, record := range due {
		if err := ctx.Err(); err != nil {
			return purged
		}
		mutation := tenancy.Mutation{ActorSubject: "system", RequestID: "purge-sweep:" + record.ID, At: now}
		claimed, err := s.workspaceLifecycle.ClaimPurge(ctx, mutation, record.ID, now.Add(purgeLease))
		if err != nil {
			s.logger().Error("workspace purge claim failed",
				zap.String("workspace", record.ID), zap.Error(err))
			continue
		}
		if !claimed {
			// Another instance owns this one. Not an error and not logged at
			// Warn: on a two-instance deployment this is the normal outcome
			// for half of them, and a line that appears every time nothing is
			// wrong trains people to ignore the log.
			continue
		}
		report, err := s.PurgeWorkspaceIfDue(ctx, record.ID, now)
		switch {
		case err == workspacepurge.ErrIncomplete:
			s.logger().Error("workspace deleted with resource classes still present — the customer was told this data is gone",
				zap.String("workspace", record.ID),
				zap.Strings("survivors", report.Survivors))
			purged++
		case err != nil:
			s.logger().Error("workspace purge failed",
				zap.String("workspace", record.ID), zap.Error(err))
		default:
			s.logger().Info("workspace purged",
				zap.String("workspace", record.ID),
				zap.Int("classes", len(report.Entries)))
			purged++
		}
	}
	return purged
}
