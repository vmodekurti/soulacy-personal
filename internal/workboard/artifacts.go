package workboard

// Artifact tracking (Story 13): files produced during a workboard run,
// detected from the run's tool-call trail and attached to the task. Prior
// runs' artifacts are immutable history; the same path written twice in one
// run is a single artifact (latest metadata wins).

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Artifact is one file produced by a task run.
type Artifact struct {
	ID        int64     `json:"id"`
	TaskID    int64     `json:"task_id"`
	RunID     int64     `json:"run_id"`
	Path      string    `json:"path"`
	SizeBytes int64     `json:"size_bytes"`
	Tool      string    `json:"tool"`
	CreatedAt time.Time `json:"created_at"`
}

const artifactsSchema = `
CREATE TABLE IF NOT EXISTS workboard_artifacts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id    INTEGER NOT NULL,
    run_id     INTEGER NOT NULL,
    path       TEXT NOT NULL,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    tool       TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    UNIQUE(run_id, path)
);
CREATE INDEX IF NOT EXISTS idx_wba_task ON workboard_artifacts(task_id);
`

// AddArtifacts upserts artifacts for one run. The (run, path) pair is
// unique: re-writing the same file later in a run updates size/tool rather
// than duplicating the row. Empty input is a no-op.
func (s *Store) AddArtifacts(ctx context.Context, workspaceID string, taskID, runID int64, arts []Artifact) error {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return err
	}
	if len(arts) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	// Attaching to a task this workspace does not own is ErrNotFound, checked
	// inside the transaction so the ownership answer cannot change between the
	// check and the writes.
	var owned int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workboard_tasks WHERE id = ? AND workspace_id = ?`, taskID, workspaceID).Scan(&owned); err != nil {
		return err
	}
	if owned == 0 {
		return ErrNotFound
	}

	now := time.Now().UTC().Truncate(time.Second)
	for _, a := range arts {
		if a.Path == "" {
			continue
		}
		created := a.CreatedAt.UTC().Truncate(time.Second)
		if created.IsZero() || created.Unix() <= 0 {
			created = now
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO workboard_artifacts (task_id, run_id, path, size_bytes, tool, created_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(run_id, path) DO UPDATE SET
				size_bytes = excluded.size_bytes,
				tool       = excluded.tool,
				created_at = excluded.created_at`,
			taskID, runID, a.Path, a.SizeBytes, a.Tool, created); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListArtifacts returns all artifacts attached to a task, newest first.
func (s *Store) ListArtifacts(ctx context.Context, workspaceID string, taskID int64) ([]Artifact, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	return s.queryArtifacts(ctx,
		`SELECT a.id, a.task_id, a.run_id, a.path, a.size_bytes, a.tool, a.created_at
		 FROM workboard_artifacts a
		 JOIN workboard_tasks t ON t.id = a.task_id
		 WHERE a.task_id = ? AND t.workspace_id = ?
		 ORDER BY a.created_at DESC, a.id DESC`, taskID, workspaceID)
}

// ListRunArtifacts returns the artifacts of one run, newest first.
func (s *Store) ListRunArtifacts(ctx context.Context, workspaceID string, runID int64) ([]Artifact, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	return s.queryArtifacts(ctx,
		`SELECT a.id, a.task_id, a.run_id, a.path, a.size_bytes, a.tool, a.created_at
		 FROM workboard_artifacts a
		 JOIN workboard_tasks t ON t.id = a.task_id
		 WHERE a.run_id = ? AND t.workspace_id = ?
		 ORDER BY a.created_at DESC, a.id DESC`, runID, workspaceID)
}

// GetArtifact fetches one artifact by ID (ErrNotFound when absent or owned by
// another workspace).
//
// This is the read the gateway turns into a file download, so an unscoped
// lookup handed one tenant both the existence and the on-disk path of
// another's output — and then streamed it.
func (s *Store) GetArtifact(ctx context.Context, workspaceID string, id int64) (Artifact, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return Artifact{}, err
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT a.id, a.task_id, a.run_id, a.path, a.size_bytes, a.tool, a.created_at
		 FROM workboard_artifacts a
		 JOIN workboard_tasks t ON t.id = a.task_id
		 WHERE a.id = ? AND t.workspace_id = ?`, id, workspaceID)
	var a Artifact
	if err := row.Scan(&a.ID, &a.TaskID, &a.RunID, &a.Path, &a.SizeBytes, &a.Tool, &a.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Artifact{}, ErrNotFound
		}
		return Artifact{}, err
	}
	a.CreatedAt = a.CreatedAt.UTC()
	return a, nil
}

func (s *Store) queryArtifacts(ctx context.Context, q string, args ...any) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Artifact{}
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.TaskID, &a.RunID, &a.Path, &a.SizeBytes, &a.Tool, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.CreatedAt = a.CreatedAt.UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}
