package schedules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/wsroot"
)

// claim.go — MU-023 criteria 2 and 3: a database claim prevents duplicate
// firing, and the enqueue carries a deterministic idempotency key per
// occurrence.
//
// THE OCCURRENCE KEY IS THE WHOLE DESIGN. An occurrence is identified by the
// instant it was scheduled for, not by when anyone noticed it was due. Two
// gateway instances evaluating the same cron expression against the same
// clock compute the same instant, therefore the same key, therefore collide
// on the same primary key — and exactly one INSERT wins.
//
// Deriving the key from anything else breaks it in a way that is hard to see:
// time.Now() at fire moment differs by milliseconds between instances, a
// counter needs coordination (which is the problem), and a UUID per attempt
// means every instance always wins. The scheduled instant is the only value
// that is both agreed-upon without communication and unique per occurrence.
//
// WHY A CLAIM AND NOT A LOCK. There is no lock to acquire, release, or leak.
// An instance that dies mid-run leaves a row; the row's lease expires and the
// occurrence becomes available again; and either way the row is the audit
// trail. A lock would have to be released by the holder, which is exactly the
// thing that just crashed.

// OccurrenceKey is the deterministic identity of one scheduled instant.
//
// Second precision in UTC: cron's finest granularity here is seconds, and
// truncating removes the sub-second disagreement two instances would otherwise
// have about "the same" fire.
func OccurrenceKey(scheduledAt time.Time) string {
	return scheduledAt.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// IdempotencyKey is what the enqueued run carries so the run store's own
// per-workspace uniqueness refuses a duplicate submission.
//
// Belt and braces with the claim, deliberately. The claim stops two instances
// deciding to fire; this stops two enqueues landing if one instance somehow
// retries past its own claim — a crash between claiming and enqueueing, then
// a lease steal, is the concrete case. Neither alone covers both.
func IdempotencyKey(scheduleID string, scheduledAt time.Time) string {
	return "sched:" + strings.TrimSpace(scheduleID) + ":" + OccurrenceKey(scheduledAt)
}

// Claim attempts to take one occurrence for this instance.
//
// Returns ErrAlreadyClaimed when another instance owns it and its lease is
// still live. That is not an error condition — exactly one instance is meant
// to lose, and this is how the loser finds out.
func (s *Store) Claim(ctx context.Context, workspaceID, scheduleID string, scheduledAt time.Time, instanceID string, lease time.Duration) (Occurrence, error) {
	workspaceID = wsroot.Normalize(workspaceID)
	scheduleID = strings.TrimSpace(scheduleID)
	instanceID = strings.TrimSpace(instanceID)
	if scheduleID == "" {
		return Occurrence{}, errors.New("schedules: a schedule id is required")
	}
	if instanceID == "" {
		// An anonymous claimant cannot be told apart from another anonymous
		// claimant, which makes a lease steal indistinguishable from a
		// re-entry by the same process. Refuse rather than guess.
		return Occurrence{}, errors.New("schedules: an instance id is required to claim")
	}
	if lease <= 0 {
		lease = DefaultLease
	}
	now := time.Now().UTC()
	occurrence := Occurrence{
		WorkspaceID: workspaceID, ScheduleID: scheduleID,
		Key: OccurrenceKey(scheduledAt), ScheduledAt: scheduledAt.UTC().Truncate(time.Second),
		ClaimedBy: instanceID, ClaimedAt: now, LeaseExpiresAt: now.Add(lease),
		Status: ClaimClaimed, IdempotencyKey: IdempotencyKey(scheduleID, scheduledAt),
	}

	// One statement, two outcomes. The INSERT wins an unclaimed occurrence;
	// the ON CONFLICT branch takes over one whose lease has expired AND which
	// never completed. The WHERE on the update is what keeps a completed
	// occurrence from ever being re-fired, however long ago it ran.
	res, err := s.db.ExecContext(ctx, `
INSERT INTO schedule_occurrences (workspace_id, schedule_id, occurrence_key, scheduled_at,
    claimed_by, claimed_at, lease_expires_at, status, completed_at, failure_reason, idempotency_key)
VALUES (?,?,?,?,?,?,?,?,NULL,'',?)
ON CONFLICT(workspace_id, schedule_id, occurrence_key) DO UPDATE SET
    claimed_by = excluded.claimed_by,
    claimed_at = excluded.claimed_at,
    lease_expires_at = excluded.lease_expires_at,
    status = excluded.status
  WHERE schedule_occurrences.status = ?
    AND schedule_occurrences.lease_expires_at <= ?`,
		occurrence.WorkspaceID, occurrence.ScheduleID, occurrence.Key, occurrence.ScheduledAt,
		occurrence.ClaimedBy, occurrence.ClaimedAt, occurrence.LeaseExpiresAt, occurrence.Status,
		occurrence.IdempotencyKey,
		ClaimClaimed, now)
	if err != nil {
		return Occurrence{}, fmt.Errorf("schedules: claim: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return Occurrence{}, ErrAlreadyClaimed
	}
	return occurrence, nil
}

// Complete records that a claimed occurrence ran.
//
// Conditioned on this instance still holding the claim. An instance whose
// lease expired and was stolen mid-run must not overwrite the new holder's
// record with its own outcome: the run it is reporting on is the one that
// was superseded.
func (s *Store) Complete(ctx context.Context, occurrence Occurrence, runErr error) error {
	status, reason := ClaimCompleted, ""
	if runErr != nil {
		status, reason = ClaimFailed, runErr.Error()
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
UPDATE schedule_occurrences
   SET status = ?, completed_at = ?, failure_reason = ?
 WHERE workspace_id = ? AND schedule_id = ? AND occurrence_key = ? AND claimed_by = ? AND status = ?`,
		status, now, truncateReason(reason),
		wsroot.Normalize(occurrence.WorkspaceID), occurrence.ScheduleID, occurrence.Key,
		occurrence.ClaimedBy, ClaimClaimed)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrAlreadyClaimed
	}
	// last_fire_at is bookkeeping for the UI and for misfire calculation. It
	// follows the completion rather than the claim so a claimed-but-never-run
	// occurrence does not make the schedule look like it fired.
	_, _ = s.db.ExecContext(ctx,
		`UPDATE agent_schedules SET last_fire_at = ?, updated_at = ? WHERE workspace_id = ? AND id = ?`,
		occurrence.ScheduledAt, now, occurrence.WorkspaceID, occurrence.ScheduleID)
	return nil
}

// Occurrences returns one schedule's recent occurrence records, newest first.
func (s *Store) Occurrences(ctx context.Context, workspaceID, scheduleID string, limit int) ([]Occurrence, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT workspace_id, schedule_id, occurrence_key, scheduled_at, claimed_by, claimed_at,
       lease_expires_at, status, completed_at, failure_reason, idempotency_key
  FROM schedule_occurrences
 WHERE workspace_id = ? AND schedule_id = ?
 ORDER BY scheduled_at DESC LIMIT ?`,
		wsroot.Normalize(workspaceID), strings.TrimSpace(scheduleID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Occurrence{}
	for rows.Next() {
		var o Occurrence
		var completed sql.NullTime
		if err := rows.Scan(&o.WorkspaceID, &o.ScheduleID, &o.Key, &o.ScheduledAt, &o.ClaimedBy,
			&o.ClaimedAt, &o.LeaseExpiresAt, &o.Status, &completed, &o.FailureReason, &o.IdempotencyKey); err != nil {
			return nil, err
		}
		if completed.Valid {
			o.CompletedAt = &completed.Time
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// truncateReason bounds a failure string. An agent that fails with a
// megabyte of provider output would otherwise put it in the schedule record,
// once per occurrence, forever.
func truncateReason(reason string) string {
	const max = 500
	reason = strings.TrimSpace(reason)
	if len(reason) <= max {
		return reason
	}
	return reason[:max] + "… [truncated]"
}
