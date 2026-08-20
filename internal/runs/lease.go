package runs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/wsroot"
)

// lease.go — MU-034 criterion 1: "job claims use renewable leases with
// ownership and expiry."
//
// THE BUG THIS FIXES IS NOT A MISSING FEATURE. Claiming a run was a status
// transition, queued → running, with no owner and no expiry. Recovery
// therefore had no way to tell a worker that DIED from a worker that is BUSY:
// RecoverAcrossWorkspaces selected every unfinished run in the deployment, and
// the startup sweep re-queued or failed each one.
//
// With one gateway that is exactly right — nothing else is running, so every
// unfinished run really was interrupted. With two, replica B booting re-queues
// runs replica A is executing at that moment, and the agent runs twice. A
// rolling restart, which is the ordinary way a second replica appears, is the
// worst case: every deploy duplicates whatever is in flight.
//
// The lease is what makes "interrupted" observable. A claim records WHO holds
// the run and UNTIL WHEN; the holder renews while it works; recovery considers
// only runs whose lease has lapsed. A live worker's runs are invisible to the
// sweep because its lease is current, and a dead worker's become visible when
// its lease is not renewed.
//
// THE SHAPE IS internal/schedules/claim.go's, deliberately. That package
// already solved this for scheduled occurrences — owner column, expiry column,
// steal conditioned in the WHERE clause of one statement — and a second design
// for the same problem in the same deployment would be two things to reason
// about. Where this differs it is because a run is claimed by a state machine
// that already exists and an occurrence is claimed by an upsert.

// DefaultRunLease is how long a claim survives without renewal.
//
// The trade is between duplicate work and recovery latency, and it only binds
// after a CRASH: a clean shutdown releases the lease, so an ordinary restart
// recovers immediately. Sixty seconds means a killed worker's run waits about
// a minute before another may take it — long enough that a stalled-but-alive
// worker (a slow provider call, a paused GC) is not robbed mid-run, short
// enough that a crash is not an outage.
const DefaultRunLease = 60 * time.Second

// RenewInterval is how often a holder should renew.
//
// A third of the lease, so two consecutive renewals can fail — a transient
// database blip, a scheduling delay — without the run being declared abandoned
// while it is still running. Renewing at half the lease leaves no room for the
// second failure, and that is the one that happens under load, which is
// exactly when the machine is busy enough to drop a renewal.
const RenewInterval = DefaultRunLease / 3

// ErrLeaseLost reports that this holder no longer owns the run.
//
// Distinguished from a plain failure because the correct response differs: a
// worker that has lost its lease must STOP rather than retry, since another
// worker may already be executing the same run and the two would race to
// record an outcome.
var ErrLeaseLost = errors.New("runs: the lease on this run is no longer held")

// RenewLease extends this holder's claim.
//
// Conditioned on still holding it. A worker whose lease expired and was taken
// over must not be able to extend the new holder's claim out from under them —
// that would produce two live holders, which is the one state the lease exists
// to make impossible.
func (s *Store) RenewLease(ctx context.Context, workspaceID, id, owner string, lease time.Duration) error {
	workspaceID = wsroot.Normalize(workspaceID)
	id = strings.TrimSpace(id)
	owner = strings.TrimSpace(owner)
	if owner == "" {
		// An anonymous holder cannot be told from another anonymous holder,
		// which makes a renewal indistinguishable from a takeover. Refuse
		// rather than guess — the same reasoning schedules.Claim uses.
		return errors.New("runs: an owner is required to renew a lease")
	}
	if lease <= 0 {
		lease = DefaultRunLease
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_runs SET lease_expires_at = ?, updated_at = ?
		  WHERE workspace_id = ? AND id = ? AND claimed_by = ? AND status = ?`,
		time.Now().UTC().Add(lease), time.Now().UTC(), workspaceID, id, owner, StatusRunning)
	if err != nil {
		return fmt.Errorf("runs: renew lease: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrLeaseLost
	}
	return nil
}

// ReleaseLease gives up a claim without changing the run's status.
//
// Called on a clean shutdown so a restarting deployment recovers its own
// interrupted runs at once instead of waiting out the lease. Without it,
// every ordinary restart would pause in-flight work for a minute — which
// operators would correctly read as the lease making things worse, and would
// respond to by shortening it until it stopped protecting anything.
//
// Not conditioned on ownership failing loudly: releasing a lease somebody else
// now holds should be a no-op, not an error, because the only caller is a
// shutdown path that cannot do anything about it.
func (s *Store) ReleaseLease(ctx context.Context, workspaceID, id, owner string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agent_runs SET lease_expires_at = NULL
		  WHERE workspace_id = ? AND id = ? AND claimed_by = ?`,
		wsroot.Normalize(workspaceID), strings.TrimSpace(id), strings.TrimSpace(owner))
	return err
}

// LeaseHeld reports whether a run is currently claimed by a live holder.
//
// A run with NO lease is not held: that is either a pre-migration row or one
// whose holder released it, and both are recoverable. Treating a missing lease
// as "held forever" would make every run written before this change
// permanently invisible to recovery.
func (r Run) LeaseHeld(now time.Time) bool {
	return r.LeaseExpiresAt != nil && r.LeaseExpiresAt.After(now)
}
