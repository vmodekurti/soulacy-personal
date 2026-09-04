package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/soulacy/soulacy/internal/sqlitex"
)

// CheckpointStatus values.
const (
	CheckpointPending    = "pending"
	CheckpointInProgress = "in_progress"
	CheckpointCompleted  = "completed"
	CheckpointFailed     = "failed"
)

// Checkpoint holds the persisted state of one workflow step.
type Checkpoint struct {
	AgentID   string
	RunID     string
	StepID    string
	State     json.RawMessage // output of the step; nil until completed
	Status    string          // pending | in_progress | completed | failed
	UpdatedAt time.Time
}

// CheckpointStore persists workflow checkpoints to SQLite.
type CheckpointStore struct{ db *sql.DB }

// NewCheckpointStore opens (or creates) the checkpoint database at path.
func NewCheckpointStore(path string) (*CheckpointStore, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, err
	}
	if err := migrateCheckpointSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	// Schema versioning (E22 adoption): v1 = the idempotent bootstrap above;
	// future changes go through sqlitex.MigrateSchema with v2+.
	if err := sqlitex.RecordSchemaVersion(db, "checkpoints", 1); err != nil {
		db.Close()
		return nil, err
	}
	return &CheckpointStore{db: db}, nil
}

func migrateCheckpointSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS workflow_checkpoints (
    agent_id   TEXT NOT NULL,
    run_id     TEXT NOT NULL,
    step_id    TEXT NOT NULL,
    state      TEXT,
    status     TEXT NOT NULL DEFAULT 'pending',
    updated_at DATETIME NOT NULL,
    PRIMARY KEY (agent_id, run_id, step_id)
);
CREATE INDEX IF NOT EXISTS idx_wf_status ON workflow_checkpoints(status);
`)
	return err
}

// Upsert inserts or replaces a checkpoint row.
func (s *CheckpointStore) Upsert(ctx context.Context, cp Checkpoint) error {
	var stateVal interface{}
	if cp.State != nil {
		stateVal = string(cp.State)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO workflow_checkpoints (agent_id, run_id, step_id, state, status, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (agent_id, run_id, step_id) DO UPDATE SET
    state      = excluded.state,
    status     = excluded.status,
    updated_at = excluded.updated_at
`, cp.AgentID, cp.RunID, cp.StepID, stateVal, cp.Status, cp.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

// Get returns the checkpoint for (agentID, runID, stepID).
// Returns (zero, sql.ErrNoRows) if absent.
func (s *CheckpointStore) Get(ctx context.Context, agentID, runID, stepID string) (Checkpoint, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT agent_id, run_id, step_id, state, status, updated_at
FROM workflow_checkpoints
WHERE agent_id = ? AND run_id = ? AND step_id = ?
`, agentID, runID, stepID)

	var cp Checkpoint
	var stateStr sql.NullString
	var updatedAtStr string
	if err := row.Scan(&cp.AgentID, &cp.RunID, &cp.StepID, &stateStr, &cp.Status, &updatedAtStr); err != nil {
		return Checkpoint{}, err
	}
	if stateStr.Valid {
		cp.State = json.RawMessage(stateStr.String)
	}
	t, _ := time.Parse(time.RFC3339Nano, updatedAtStr)
	cp.UpdatedAt = t
	return cp, nil
}

// ListInProgress returns all checkpoints with status = in_progress for recovery.
func (s *CheckpointStore) ListInProgress(ctx context.Context) ([]Checkpoint, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT agent_id, run_id, step_id, state, status, updated_at
FROM workflow_checkpoints
WHERE status = ?
`, CheckpointInProgress)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Checkpoint
	for rows.Next() {
		var cp Checkpoint
		var stateStr sql.NullString
		var updatedAtStr string
		if err := rows.Scan(&cp.AgentID, &cp.RunID, &cp.StepID, &stateStr, &cp.Status, &updatedAtStr); err != nil {
			return nil, err
		}
		if stateStr.Valid {
			cp.State = json.RawMessage(stateStr.String)
		}
		t, _ := time.Parse(time.RFC3339Nano, updatedAtStr)
		cp.UpdatedAt = t
		out = append(out, cp)
	}
	return out, rows.Err()
}

// Close closes the DB.
func (s *CheckpointStore) Close() error {
	return s.db.Close()
}
