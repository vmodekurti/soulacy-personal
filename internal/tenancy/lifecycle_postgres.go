package tenancy

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// lifecycle_postgres.go — the durable workspace lifecycle.
//
// Every transition takes an ADVISORY LOCK on the workspace and re-reads its
// row `FOR UPDATE` inside the transaction, for the reason the membership
// mutations do the same: the decision is made from what the row says NOW, not
// from what a caller read a moment ago. Without it, two owners clicking delete
// at once both see `active`, both proceed, and the second one's recovery
// window silently replaces the first's — so the deletion happens at a time
// neither of them chose.

func (s *PostgresStore) lockWorkspace(ctx context.Context, tx pgx.Tx, workspaceID string) (WorkspaceRecord, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "workspace:"+workspaceID); err != nil {
		return WorkspaceRecord{}, err
	}
	return scanWorkspace(tx.QueryRow(ctx, `SELECT id, organization_id, name, COALESCE(status,'active'),
		COALESCE(deletion_requested_at, 'epoch'::timestamptz), COALESCE(recoverable_until, 'epoch'::timestamptz)
		FROM workspaces WHERE id=$1 FOR UPDATE`, workspaceID))
}

func scanWorkspace(row rowScanner) (WorkspaceRecord, error) {
	var record WorkspaceRecord
	var requested, recover time.Time
	if err := row.Scan(&record.ID, &record.OrganizationID, &record.Name, &record.Status, &requested, &recover); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WorkspaceRecord{}, ErrMembershipNotFound
		}
		return WorkspaceRecord{}, err
	}
	// `epoch` is the COALESCE sentinel for NULL, mapped back to a zero time so
	// callers test `IsZero()` rather than comparing against a magic instant.
	if !requested.IsZero() && requested.Year() > 1970 {
		record.RequestedAt = requested.UTC()
	}
	if !recover.IsZero() && recover.Year() > 1970 {
		record.RecoverUntil = recover.UTC()
	}
	return record, nil
}

// Workspace reads the lifecycle record.
func (s *PostgresStore) Workspace(ctx context.Context, workspaceID string) (WorkspaceRecord, error) {
	if s == nil || s.pool == nil {
		return WorkspaceRecord{}, errors.New("tenancy store is unavailable")
	}
	return scanWorkspace(s.pool.QueryRow(ctx, `SELECT id, organization_id, name, COALESCE(status,'active'),
		COALESCE(deletion_requested_at, 'epoch'::timestamptz), COALESCE(recoverable_until, 'epoch'::timestamptz)
		FROM workspaces WHERE id=$1`, workspaceID))
}

// BeginDeletion moves an ACTIVE workspace to `deleting`.
//
// Refusing anything but active is what makes the transition idempotent in the
// only direction that matters. A second request cannot extend or shorten the
// window a first one set — which would mean the deletion fires at a moment
// neither owner chose — and cannot restart a workspace already purged.
func (s *PostgresStore) BeginDeletion(ctx context.Context, mutation Mutation, workspaceID string, recoverUntil time.Time) (WorkspaceRecord, error) {
	tx, err := s.beginMutation(ctx, mutation)
	if err != nil {
		return WorkspaceRecord{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	before, err := s.lockWorkspace(ctx, tx, workspaceID)
	if err != nil {
		return WorkspaceRecord{}, err
	}
	if !strings.EqualFold(before.Status, WorkspaceActive) {
		return WorkspaceRecord{}, ErrWorkspaceNotActive
	}
	at := mutationTime(mutation)
	after := before
	after.Status = WorkspaceDeleting
	after.RequestedAt = at.UTC()
	after.RecoverUntil = recoverUntil.UTC()

	if _, err := tx.Exec(ctx, `UPDATE workspaces SET status=$2, deletion_requested_at=$3, recoverable_until=$4 WHERE id=$1`,
		workspaceID, after.Status, after.RequestedAt, after.RecoverUntil); err != nil {
		return WorkspaceRecord{}, err
	}
	if err := insertAudit(ctx, tx, mutation, "workspace.deletion_requested", "workspace", workspaceID, before, after); err != nil {
		return WorkspaceRecord{}, err
	}
	return after, tx.Commit(ctx)
}

// CancelDeletion returns a workspace to active during its recovery window.
func (s *PostgresStore) CancelDeletion(ctx context.Context, mutation Mutation, workspaceID string) (WorkspaceRecord, error) {
	tx, err := s.beginMutation(ctx, mutation)
	if err != nil {
		return WorkspaceRecord{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	before, err := s.lockWorkspace(ctx, tx, workspaceID)
	if err != nil {
		return WorkspaceRecord{}, err
	}
	if !strings.EqualFold(before.Status, WorkspaceDeleting) {
		return WorkspaceRecord{}, ErrWorkspaceNotDeleting
	}
	// The deadline is compared inside the transaction, against the row this
	// statement holds a lock on. Checked before the lock, a cancellation and
	// the purge that starts at the deadline could both believe they won.
	if !before.Recoverable(mutationTime(mutation)) {
		return WorkspaceRecord{}, ErrRecoveryWindowClosed
	}
	after := before
	after.Status = WorkspaceActive
	after.RequestedAt = time.Time{}
	after.RecoverUntil = time.Time{}

	if _, err := tx.Exec(ctx, `UPDATE workspaces SET status=$2, deletion_requested_at=NULL, recoverable_until=NULL WHERE id=$1`,
		workspaceID, after.Status); err != nil {
		return WorkspaceRecord{}, err
	}
	if err := insertAudit(ctx, tx, mutation, "workspace.deletion_cancelled", "workspace", workspaceID, before, after); err != nil {
		return WorkspaceRecord{}, err
	}
	return after, tx.Commit(ctx)
}

// CompleteDeletion marks a purged workspace `deleted`.
//
// AFTER the purge, never before. `deleted` is the state that makes a workspace
// unreadable to its own members (see gateway.workspaceAdmission), so setting
// it first would hide the removal in progress from exactly the people entitled
// to watch it — and would make a purge that then failed indistinguishable from
// one that succeeded.
func (s *PostgresStore) CompleteDeletion(ctx context.Context, mutation Mutation, workspaceID string) (WorkspaceRecord, error) {
	tx, err := s.beginMutation(ctx, mutation)
	if err != nil {
		return WorkspaceRecord{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	before, err := s.lockWorkspace(ctx, tx, workspaceID)
	if err != nil {
		return WorkspaceRecord{}, err
	}
	if !strings.EqualFold(before.Status, WorkspaceDeleting) {
		return WorkspaceRecord{}, ErrWorkspaceNotDeleting
	}
	after := before
	after.Status = WorkspaceDeleted

	if _, err := tx.Exec(ctx, `UPDATE workspaces SET status=$2 WHERE id=$1`, workspaceID, after.Status); err != nil {
		return WorkspaceRecord{}, err
	}
	if err := insertAudit(ctx, tx, mutation, "workspace.deleted", "workspace", workspaceID, before, after); err != nil {
		return WorkspaceRecord{}, err
	}
	return after, tx.Commit(ctx)
}

// ErrWorkspaceNotActive is returned when a deletion is requested on a
// workspace that is already being deleted, suspended, or gone.
var ErrWorkspaceNotActive = errors.New("tenancy: this workspace is not active")

// DueForPurge lists workspaces whose recovery window has closed.
//
// Ordered by deadline so the longest-overdue is purged first. A sweep that
// picked arbitrarily could starve one workspace indefinitely behind a steady
// arrival of newer ones — and the starved one is, by construction, the one
// whose customer has been waiting longest to be told their data is gone.
func (s *PostgresStore) DueForPurge(ctx context.Context, now time.Time, limit int) ([]WorkspaceRecord, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("tenancy store is unavailable")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT id, organization_id, name, COALESCE(status,'active'),
		COALESCE(deletion_requested_at, 'epoch'::timestamptz), COALESCE(recoverable_until, 'epoch'::timestamptz)
		FROM workspaces
		WHERE status=$1 AND recoverable_until IS NOT NULL AND recoverable_until <= $2
		  AND (purge_claimed_until IS NULL OR purge_claimed_until <= $2)
		ORDER BY recoverable_until ASC LIMIT $3`, WorkspaceDeleting, now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkspaceRecord
	for rows.Next() {
		record, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// ClaimPurge takes an expiring lease on purging one workspace.
//
// One UPDATE with the condition in the WHERE clause, so the check and the
// claim are the same statement. Read-then-claim in two statements is how two
// instances both see an unclaimed workspace and both proceed — the same
// mistake the token quota's read-then-allow made, in a place where the cost is
// two concurrent purges of the same data rather than an overspend.
func (s *PostgresStore) ClaimPurge(ctx context.Context, mutation Mutation, workspaceID string, leaseUntil time.Time) (bool, error) {
	if s == nil || s.pool == nil {
		return false, errors.New("tenancy store is unavailable")
	}
	now := mutationTime(mutation)
	tag, err := s.pool.Exec(ctx, `UPDATE workspaces SET purge_claimed_by=$2, purge_claimed_until=$3
		WHERE id=$1 AND status=$4 AND recoverable_until IS NOT NULL AND recoverable_until <= $5
		  AND (purge_claimed_until IS NULL OR purge_claimed_until <= $5)`,
		workspaceID, mutation.ActorSubject, leaseUntil.UTC(), WorkspaceDeleting, now.UTC())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
