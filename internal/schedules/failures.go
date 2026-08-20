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

// failures.go — the consecutive-failure counter has to be shared.
//
// THE BUG. A cron agent that fails repeatedly is auto-disabled after N
// back-to-back failures, so a permanently broken agent (dead provider, bad
// model) stops firing on a loop nobody is watching. The counter lived in one
// scheduler's memory.
//
// Schedules are claimed durably, so exactly one replica fires each occurrence —
// but not the SAME replica each time. Replica A fires and fails: its count is
// 1. Replica B claims the next occurrence and fails: ITS count is 1. With two
// replicas the count reaches N/2 at best, with three N/3, and a chronically
// failing agent is never disabled at all. It just keeps firing, once per
// replica, forever. Nothing errors; the safety valve is simply absent, which
// is the seventh entry in config.ScaleReplicationBlockers.
//
// A restart has the same effect on one replica: the counter resets, so an
// agent failing once an hour on a gateway that restarts daily never
// accumulates anything either. That is a single-process bug, not a scale one.
//
// The counter belongs on the row every replica already reads.

// consecutiveFailuresColumn is added to an existing table rather than shipped
// in the CREATE, because installations upgrading into this already have the
// table and would never run the CREATE again.
const consecutiveFailuresColumn = `ALTER TABLE agent_schedules ADD COLUMN consecutive_failures INTEGER NOT NULL DEFAULT 0`

func ensureConsecutiveFailuresColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(agent_schedules)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == "consecutive_failures" {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := db.Exec(consecutiveFailuresColumn); err != nil {
		return fmt.Errorf("schedules: add consecutive_failures: %w", err)
	}
	return nil
}

// RecordFailure increments a schedule's consecutive-failure count and returns
// the new value.
//
// The read and the write are ONE statement. Two replicas failing the same
// agent close together would otherwise both read N and both write N+1, and the
// counter would advance by one for two failures — which is the same
// undercounting this replaces, in a smaller window.
//
// A schedule with no row returns 0 and no error: an agent can be fired from a
// SOUL.yaml without a durable schedule row, and refusing there would turn a
// missing row into a failed run.
func (s *Store) RecordFailure(ctx context.Context, workspaceID, agentID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("schedules: store is unavailable")
	}
	workspaceID = wsroot.Normalize(workspaceID)
	agentID = strings.TrimSpace(agentID)
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_schedules SET consecutive_failures = consecutive_failures + 1, updated_at = ?
		 WHERE workspace_id = ? AND agent_id = ?`, time.Now().UTC(), workspaceID, agentID)
	if err != nil {
		return 0, err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return 0, nil
	}
	var count int
	err = s.db.QueryRowContext(ctx,
		`SELECT consecutive_failures FROM agent_schedules WHERE workspace_id = ? AND agent_id = ?`,
		workspaceID, agentID).Scan(&count)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return count, err
}

// ClearFailures resets the counter after a successful run.
func (s *Store) ClearFailures(ctx context.Context, workspaceID, agentID string) error {
	if s == nil || s.db == nil {
		return errors.New("schedules: store is unavailable")
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE agent_schedules SET consecutive_failures = 0 WHERE workspace_id = ? AND agent_id = ?
		 AND consecutive_failures <> 0`,
		wsroot.Normalize(workspaceID), strings.TrimSpace(agentID))
	return err
}

// ConsecutiveFailures reports the stored count, for diagnostics.
func (s *Store) ConsecutiveFailures(ctx context.Context, workspaceID, agentID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("schedules: store is unavailable")
	}
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT consecutive_failures FROM agent_schedules WHERE workspace_id = ? AND agent_id = ?`,
		wsroot.Normalize(workspaceID), strings.TrimSpace(agentID)).Scan(&count)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return count, err
}
