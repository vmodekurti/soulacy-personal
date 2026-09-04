package workboard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Run statuses. A run starts as running and ends as done or failed.
const (
	RunStatusRunning = "running"
	RunStatusDone    = "done"
	RunStatusFailed  = "failed"
)

// ErrRunActive is returned by StartRun when the task already has an
// unfinished run (duplicate concurrent runs are not allowed).
var ErrRunActive = errors.New("workboard: task already has an active run")

// Run is one execution attempt of a task through an agent. Prior attempts
// are never mutated by retries.
type Run struct {
	ID            int64      `json:"id"`
	TaskID        int64      `json:"task_id"`
	Attempt       int        `json:"attempt"`
	AgentID       string     `json:"agent_id"`
	SessionID     string     `json:"session_id"`
	ActionLogPath string     `json:"action_log_path"`
	Status        string     `json:"status"`
	Result        string     `json:"result"`
	FailureReason string     `json:"failure_reason"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at"`
}

const runsSchema = `
CREATE TABLE IF NOT EXISTS workboard_runs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id         INTEGER NOT NULL,
    attempt         INTEGER NOT NULL,
    agent_id        TEXT NOT NULL DEFAULT '',
    session_id      TEXT NOT NULL DEFAULT '',
    action_log_path TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'running',
    result          TEXT NOT NULL DEFAULT '',
    failure_reason  TEXT NOT NULL DEFAULT '',
    started_at      DATETIME NOT NULL,
    ended_at        DATETIME
);
CREATE INDEX IF NOT EXISTS idx_wbr_task ON workboard_runs(task_id, attempt);
`

// StartRun records a new attempt for the task and returns it. Fails with
// ErrNotFound if the task does not exist and ErrRunActive if another run
// for the task has not finished yet.
func (s *Store) StartRun(ctx context.Context, taskID int64, agentID, sessionID, actionLogPath string) (Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback() //nolint:errcheck

	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workboard_tasks WHERE id = ?`, taskID).Scan(&exists); err != nil {
		return Run{}, err
	}
	if exists == 0 {
		return Run{}, ErrNotFound
	}

	var active int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workboard_runs WHERE task_id = ? AND ended_at IS NULL`, taskID).Scan(&active); err != nil {
		return Run{}, err
	}
	if active > 0 {
		return Run{}, ErrRunActive
	}

	var attempts int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workboard_runs WHERE task_id = ?`, taskID).Scan(&attempts); err != nil {
		return Run{}, err
	}

	now := time.Now().UTC().Truncate(time.Second)
	run := Run{
		TaskID:        taskID,
		Attempt:       attempts + 1,
		AgentID:       agentID,
		SessionID:     sessionID,
		ActionLogPath: actionLogPath,
		Status:        RunStatusRunning,
		StartedAt:     now,
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO workboard_runs (task_id, attempt, agent_id, session_id, action_log_path, status, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		run.TaskID, run.Attempt, run.AgentID, run.SessionID, run.ActionLogPath,
		run.Status, now.Format(timeLayout),
	)
	if err != nil {
		return Run{}, err
	}
	run.ID, err = res.LastInsertId()
	if err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return run, nil
}

// FinishRun marks a running run as done or failed, recording the result
// summary or failure reason. Finishing a run twice is rejected.
func (s *Store) FinishRun(ctx context.Context, runID int64, status, result, failureReason string) (Run, error) {
	if status != RunStatusDone && status != RunStatusFailed {
		return Run{}, fmt.Errorf("%w: run must finish as %q or %q, got %q",
			ErrInvalid, RunStatusDone, RunStatusFailed, status)
	}
	now := time.Now().UTC().Truncate(time.Second)
	res, err := s.db.ExecContext(ctx,
		`UPDATE workboard_runs
		 SET status = ?, result = ?, failure_reason = ?, ended_at = ?
		 WHERE id = ? AND ended_at IS NULL`,
		status, result, failureReason, now.Format(timeLayout), runID,
	)
	if err != nil {
		return Run{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Run{}, err
	}
	if n == 0 {
		// Distinguish missing from already-finished.
		var exists int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM workboard_runs WHERE id = ?`, runID).Scan(&exists); err != nil {
			return Run{}, err
		}
		if exists == 0 {
			return Run{}, ErrNotFound
		}
		return Run{}, fmt.Errorf("%w: run %d has already finished", ErrInvalid, runID)
	}
	return s.GetRun(ctx, runID)
}

// GetRun returns one run by ID.
func (s *Store) GetRun(ctx context.Context, runID int64) (Run, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, attempt, agent_id, session_id, action_log_path,
		        status, result, failure_reason, started_at, ended_at
		 FROM workboard_runs WHERE id = ?`, runID)
	return scanRun(row)
}

// ListRuns returns all attempts for a task, newest first.
func (s *Store) ListRuns(ctx context.Context, taskID int64) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, attempt, agent_id, session_id, action_log_path,
		        status, result, failure_reason, started_at, ended_at
		 FROM workboard_runs WHERE task_id = ?
		 ORDER BY attempt DESC, id DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanRun(row scanner) (Run, error) {
	var (
		r     Run
		ended sql.NullTime
	)
	err := row.Scan(&r.ID, &r.TaskID, &r.Attempt, &r.AgentID, &r.SessionID,
		&r.ActionLogPath, &r.Status, &r.Result, &r.FailureReason, &r.StartedAt, &ended)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, err
	}
	if ended.Valid {
		t := ended.Time
		r.EndedAt = &t
	}
	return r, nil
}
