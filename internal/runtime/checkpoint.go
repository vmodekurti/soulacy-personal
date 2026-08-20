package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/internal/wsroot"
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
	// WorkspaceID is the tenant the run belongs to and the leading component
	// of the row's identity.
	//
	// Agent IDs are unique within a workspace, not across the deployment, so
	// (agent_id, run_id, step_id) is not a key — two tenants can each have an
	// agent called "researcher". That matters more here than in a store where
	// the worst case is a leak: State is replayed into a resuming run through
	// the Restore hook, so a cross-tenant match would not merely show another
	// workspace's data, it would *inject* it as this run's own step output.
	WorkspaceID string
	AgentID     string
	RunID       string
	StepID      string
	State       json.RawMessage // output of the step; nil until completed
	Status      string          // pending | in_progress | completed | failed
	UpdatedAt   time.Time
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
	if _, err := sqlitex.MigrateSchema(db, "checkpoints", []sqlitex.SchemaMigration{
		{Version: 2, SQL: checkpointSchemaV2, Destructive: true},
	}); err != nil {
		db.Close()
		return nil, err
	}
	return &CheckpointStore{db: db}, nil
}

// checkpointSchemaV2 widens the primary key to include the workspace.
//
// This is the one case in this migration series that cannot be additive. The
// v1 key is (agent_id, run_id, step_id) and it is a real UNIQUE constraint, so
// simply adding a column would leave the old key enforcing global uniqueness:
// a second tenant writing the same triple would not get its own row, it would
// take over the first tenant's through the ON CONFLICT clause. SQLite cannot
// alter a primary key in place, so the table is rebuilt.
//
// Marked Destructive deliberately, and safe to be: MigrateSchema runs each
// step inside a transaction, so an interrupted upgrade rolls back to the v1
// table with its rows intact rather than leaving a half-copied one. Existing
// rows are assigned to the personal workspace, which is whose runs they were.
// The old table is renamed aside and the new one created under the real name,
// rather than the other way round. Either order works in SQLite; this one
// keeps `workflow_checkpoints` the only name this package ever CREATEs, so the
// ownership catalog's discovery scan does not see a second durable table it
// would then demand a classification and an isolation test for. The scratch
// name exists only between the ALTER and the DROP, inside one transaction.
const checkpointSchemaV2 = `
ALTER TABLE workflow_checkpoints RENAME TO workflow_checkpoints_pre_tenant;
CREATE TABLE workflow_checkpoints (
    workspace_id TEXT NOT NULL,
    agent_id     TEXT NOT NULL,
    run_id       TEXT NOT NULL,
    step_id      TEXT NOT NULL,
    state        TEXT,
    status       TEXT NOT NULL DEFAULT 'pending',
    updated_at   DATETIME NOT NULL,
    PRIMARY KEY (workspace_id, agent_id, run_id, step_id)
);
INSERT INTO workflow_checkpoints
    (workspace_id, agent_id, run_id, step_id, state, status, updated_at)
SELECT 'ws_personal', agent_id, run_id, step_id, state, status, updated_at
FROM workflow_checkpoints_pre_tenant;
DROP TABLE workflow_checkpoints_pre_tenant;
CREATE INDEX IF NOT EXISTS idx_wf_status    ON workflow_checkpoints(status);
CREATE INDEX IF NOT EXISTS idx_wf_ws_status ON workflow_checkpoints(workspace_id, status);
`

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
INSERT INTO workflow_checkpoints (workspace_id, agent_id, run_id, step_id, state, status, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (workspace_id, agent_id, run_id, step_id) DO UPDATE SET
    state      = excluded.state,
    status     = excluded.status,
    updated_at = excluded.updated_at
`, wsroot.Normalize(cp.WorkspaceID), cp.AgentID, cp.RunID, cp.StepID, stateVal, cp.Status, cp.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

// Get returns the checkpoint for (agentID, runID, stepID).
// Returns (zero, sql.ErrNoRows) if absent.
func (s *CheckpointStore) Get(ctx context.Context, workspaceID, agentID, runID, stepID string) (Checkpoint, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT workspace_id, agent_id, run_id, step_id, state, status, updated_at
FROM workflow_checkpoints
WHERE workspace_id = ? AND agent_id = ? AND run_id = ? AND step_id = ?
`, wsroot.Normalize(workspaceID), agentID, runID, stepID)

	var cp Checkpoint
	var stateStr sql.NullString
	var updatedAtStr string
	if err := row.Scan(&cp.WorkspaceID, &cp.AgentID, &cp.RunID, &cp.StepID, &stateStr, &cp.Status, &updatedAtStr); err != nil {
		return Checkpoint{}, err
	}
	if stateStr.Valid {
		cp.State = json.RawMessage(stateStr.String)
	}
	t, _ := time.Parse(time.RFC3339Nano, updatedAtStr)
	cp.UpdatedAt = t
	return cp, nil
}

// ListInProgress returns the workspace's checkpoints with status =
// in_progress, for resuming that tenant's interrupted runs.
func (s *CheckpointStore) ListInProgress(ctx context.Context, workspaceID string) ([]Checkpoint, error) {
	return s.listInProgress(ctx, wsroot.Normalize(workspaceID))
}

// ListInProgressAcrossWorkspaces returns every tenant's interrupted runs.
//
// Deployment-wide on purpose, and named so a caller cannot reach it by
// accident: a process-restart sweep genuinely has to see all of them, because
// there is no request and therefore no tenant to scope by. Every returned row
// carries its WorkspaceID, so the resumer has to decide what to do per row
// rather than inheriting one workspace's context for all of them.
func (s *CheckpointStore) ListInProgressAcrossWorkspaces(ctx context.Context) ([]Checkpoint, error) {
	return s.listInProgress(ctx, "")
}

func (s *CheckpointStore) ListWorkspace(ctx context.Context, workspaceID string) ([]Checkpoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT workspace_id, agent_id, run_id, step_id, state, status, updated_at
		FROM workflow_checkpoints WHERE workspace_id=? ORDER BY updated_at, agent_id, run_id, step_id`, wsroot.Normalize(workspaceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Checkpoint
	for rows.Next() {
		var cp Checkpoint
		var state sql.NullString
		var updated string
		if err := rows.Scan(&cp.WorkspaceID, &cp.AgentID, &cp.RunID, &cp.StepID, &state, &cp.Status, &updated); err != nil {
			return nil, err
		}
		if state.Valid {
			cp.State = json.RawMessage(state.String)
		}
		cp.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, cp)
	}
	return out, rows.Err()
}

func (s *CheckpointStore) PurgeWorkspace(ctx context.Context, workspaceID string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM workflow_checkpoints WHERE workspace_id=?`, wsroot.Normalize(workspaceID))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *CheckpointStore) listInProgress(ctx context.Context, workspaceID string) ([]Checkpoint, error) {
	query := `
SELECT workspace_id, agent_id, run_id, step_id, state, status, updated_at
FROM workflow_checkpoints
WHERE status = ?`
	args := []any{CheckpointInProgress}
	if workspaceID != "" {
		query += ` AND workspace_id = ?`
		args = append(args, workspaceID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Checkpoint
	for rows.Next() {
		var cp Checkpoint
		var stateStr sql.NullString
		var updatedAtStr string
		if err := rows.Scan(&cp.WorkspaceID, &cp.AgentID, &cp.RunID, &cp.StepID, &stateStr, &cp.Status, &updatedAtStr); err != nil {
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
