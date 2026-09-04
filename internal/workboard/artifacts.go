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
func (s *Store) AddArtifacts(ctx context.Context, taskID, runID int64, arts []Artifact) error {
	if len(arts) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

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
func (s *Store) ListArtifacts(ctx context.Context, taskID int64) ([]Artifact, error) {
	return s.queryArtifacts(ctx,
		`SELECT id, task_id, run_id, path, size_bytes, tool, created_at
		 FROM workboard_artifacts WHERE task_id = ? ORDER BY created_at DESC, id DESC`, taskID)
}

// ListRunArtifacts returns the artifacts of one run, newest first.
func (s *Store) ListRunArtifacts(ctx context.Context, runID int64) ([]Artifact, error) {
	return s.queryArtifacts(ctx,
		`SELECT id, task_id, run_id, path, size_bytes, tool, created_at
		 FROM workboard_artifacts WHERE run_id = ? ORDER BY created_at DESC, id DESC`, runID)
}

// GetArtifact fetches one artifact by ID (ErrNotFound when absent).
func (s *Store) GetArtifact(ctx context.Context, id int64) (Artifact, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, run_id, path, size_bytes, tool, created_at
		 FROM workboard_artifacts WHERE id = ?`, id)
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

func (s *Store) queryArtifacts(ctx context.Context, q string, arg any) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, q, arg)
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
