package session

import (
	"context"
	"fmt"
	"time"
)

// Fork copies the conversation entries of srcSessionID up to and including
// uptoEntryID into newSessionID (Story 8 — chat checkpoints & branching).
// Returns the number of entries copied.
//
// Invariants protecting branch isolation:
//   - the source session is never modified;
//   - the target session must not already exist (no mixing histories);
//   - copied entries keep their role/content/agent/tokens but get fresh IDs
//     and insertion order matching the source order.
//
// Forking an unknown source copies zero entries and is not an error — the
// caller decides how to report it (the gateway returns 404).
func (s *SQLiteHistoryStore) Fork(ctx context.Context, srcSessionID, newSessionID string, uptoEntryID int64) (int, error) {
	if srcSessionID == newSessionID {
		return 0, fmt.Errorf("session/history: fork target equals source %q", srcSessionID)
	}
	if newSessionID == "" {
		return 0, fmt.Errorf("session/history: fork target session id is empty")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("session/history: fork begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Branch-mixing guard: target must be empty.
	var existing int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM conversation_history WHERE session_id = ?`, newSessionID).
		Scan(&existing); err != nil {
		return 0, fmt.Errorf("session/history: fork target check: %w", err)
	}
	if existing > 0 {
		return 0, fmt.Errorf("session/history: fork target %q already has %d entries", newSessionID, existing)
	}

	// Copy in source order. created_at is re-stamped (the fork is a new
	// timeline); ordering inside the fork is preserved by insertion order
	// and a strictly increasing timestamp offset.
	rows, err := tx.QueryContext(ctx,
		`SELECT agent_id, role, content, tokens FROM conversation_history
		 WHERE session_id = ? AND id <= ?
		 ORDER BY id ASC`, srcSessionID, uptoEntryID)
	if err != nil {
		return 0, fmt.Errorf("session/history: fork select: %w", err)
	}
	type row struct {
		agentID, role, content string
		tokens                 int
	}
	var src []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.agentID, &r.role, &r.content, &r.tokens); err != nil {
			rows.Close()
			return 0, fmt.Errorf("session/history: fork scan: %w", err)
		}
		src = append(src, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(src) == 0 {
		return 0, nil
	}

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO conversation_history
		    (session_id, agent_id, role, content, tokens, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("session/history: fork prepare: %w", err)
	}
	defer stmt.Close()

	base := time.Now().UTC()
	for i, r := range src {
		createdAt := base.Add(time.Duration(i) * time.Millisecond).Format("2006-01-02 15:04:05.000")
		if _, err := stmt.ExecContext(ctx, newSessionID, r.agentID, r.role, r.content, r.tokens, createdAt); err != nil {
			return 0, fmt.Errorf("session/history: fork insert: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("session/history: fork commit: %w", err)
	}
	return len(src), nil
}
