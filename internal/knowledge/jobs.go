// jobs.go — the durable ingestion job catalog.
//
// Ingestion used to run INSIDE the HTTP request: extract → chunk → embed every
// chunk → write. On a large document that blocks the request for minutes, holds
// the whole file in memory, has no progress, and loses everything on a restart
// or a transient embedder error.
//
// This is the catalog that replaces it. A job row is the SOURCE OF TRUTH for a
// unit of ingestion work:
//
//	queued → running → done
//	              └──→ failed (after the attempt budget is spent)
//
// It deliberately does NOT depend on internal/queue: that backend is an
// at-most-once, in-memory pub/sub that silently DROPS messages when a
// subscriber's buffer overflows and whose Ack() is a no-op. Durability has to
// live in SQLite, so the worker claims rows here and a startup sweep requeues
// anything left mid-flight by a crash.
//
// The shape (status + attempt + failure reason + started/ended) mirrors
// workboard.Run, which is the established job model in this codebase.

package knowledge

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Ingest job statuses.
const (
	JobQueued  = "queued"
	JobRunning = "running"
	JobDone    = "done"
	JobFailed  = "failed"
)

// DefaultMaxAttempts bounds retries of a transient failure (e.g. the embedder
// being briefly unreachable) before the job is parked as failed.
const DefaultMaxAttempts = 3

// DefaultMaxDocumentBytes caps one document ingestion payload. It matches the
// config default and keeps the extractor/embedder path from pinning arbitrary
// large files in process memory.
const DefaultMaxDocumentBytes int64 = 50 << 20

// IngestJob is one queued document waiting to be (or being) ingested.
type IngestJob struct {
	ID       string `json:"id"`
	KBName   string `json:"kb_name"`
	Title    string `json:"title"`
	Source   string `json:"source"`
	MIMEType string `json:"mime_type"`
	// SpoolPath is where the raw bytes live on disk. The content is NEVER held
	// in this row (or in memory across the queue) so a 200 MB PDF costs a path,
	// not a heap allocation.
	SpoolPath string `json:"-"`
	ByteSize  int64  `json:"byte_size"`

	Status   string `json:"status"`
	Attempt  int    `json:"attempt"`
	Progress int    `json:"progress"` // 0..100
	Error    string `json:"error,omitempty"`
	DocID    string `json:"doc_id,omitempty"` // set when Status == done

	CreatedAt time.Time  `json:"created_at"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// Terminal reports whether the job has reached a final state.
func (j IngestJob) Terminal() bool { return j.Status == JobDone || j.Status == JobFailed }

// initJobSchema creates the job catalog. Called from Open; idempotent, so it
// also upgrades an existing knowledge.db in place.
func initJobSchema(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS ingest_jobs (
			id TEXT PRIMARY KEY,
			kb_name TEXT NOT NULL,
			title TEXT NOT NULL,
			source TEXT,
			mime_type TEXT,
			spool_path TEXT NOT NULL,
			byte_size INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL,
			attempt INTEGER NOT NULL DEFAULT 0,
			progress INTEGER NOT NULL DEFAULT 0,
			error TEXT,
			doc_id TEXT,
			created_at DATETIME NOT NULL,
			started_at DATETIME,
			ended_at DATETIME
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ingest_jobs_status ON ingest_jobs(status, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_ingest_jobs_kb ON ingest_jobs(kb_name, created_at)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("knowledge: ingest job schema: %w", err)
		}
	}
	return nil
}

// EnqueueIngest records a new job in the queued state. The caller has already
// spooled the raw bytes to SpoolPath.
func (s *Store) EnqueueIngest(j IngestJob) (IngestJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if j.ID == "" {
		j.ID = uuid.New().String()
	}
	j.Status = JobQueued
	j.Attempt = 0
	j.Progress = 0
	j.CreatedAt = time.Now().UTC()

	_, err := s.db.Exec(
		`INSERT INTO ingest_jobs (id, kb_name, title, source, mime_type, spool_path, byte_size, status, attempt, progress, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.KBName, j.Title, j.Source, j.MIMEType, j.SpoolPath, j.ByteSize,
		j.Status, j.Attempt, j.Progress, j.CreatedAt,
	)
	if err != nil {
		return IngestJob{}, fmt.Errorf("knowledge: enqueue ingest: %w", err)
	}
	return j, nil
}

// ClaimNextIngest atomically takes the oldest queued job and marks it running,
// incrementing its attempt counter. Returns ok=false when nothing is waiting.
// The UPDATE...WHERE id = (SELECT ...) form is what makes this safe against a
// second worker racing for the same row.
func (s *Store) ClaimNextIngest() (IngestJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	var id string
	err := s.db.QueryRow(
		`UPDATE ingest_jobs
		    SET status = ?, attempt = attempt + 1, started_at = ?, error = NULL
		  WHERE id = (
		        SELECT id FROM ingest_jobs
		         WHERE status = ?
		         ORDER BY created_at
		         LIMIT 1
		  )
		 RETURNING id`,
		JobRunning, now, JobQueued,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return IngestJob{}, false, nil
	}
	if err != nil {
		return IngestJob{}, false, fmt.Errorf("knowledge: claim ingest: %w", err)
	}

	j, err := s.getIngestLocked(id)
	if err != nil {
		return IngestJob{}, false, err
	}
	return j, true, nil
}

// SetIngestProgress records how far along a running job is (0..100).
func (s *Store) SetIngestProgress(id string, pct int) error {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE ingest_jobs SET progress = ? WHERE id = ?`, pct, id)
	return err
}

// FinishIngest moves a job to done (with the resulting document id) or failed
// (with the reason).
func (s *Store) FinishIngest(id, status, docID, errMsg string) (IngestJob, error) {
	if status != JobDone && status != JobFailed {
		return IngestJob{}, fmt.Errorf("knowledge: invalid terminal status %q", status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	progress := 100
	if status == JobFailed {
		// Leave the progress where it stalled — it tells the operator how far it got.
		var cur int
		_ = s.db.QueryRow(`SELECT progress FROM ingest_jobs WHERE id = ?`, id).Scan(&cur)
		progress = cur
	}
	if _, err := s.db.Exec(
		`UPDATE ingest_jobs SET status = ?, doc_id = ?, error = ?, progress = ?, ended_at = ? WHERE id = ?`,
		status, docID, errMsg, progress, now, id,
	); err != nil {
		return IngestJob{}, fmt.Errorf("knowledge: finish ingest: %w", err)
	}
	return s.getIngestLocked(id)
}

// RequeueIngest puts a job back in the queue — used to retry a transient
// failure before the attempt budget is spent, and by the operator's Retry button
// on a failed job (which also resets the budget by clearing attempts).
func (s *Store) RequeueIngest(id string, resetAttempts bool) (IngestJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	q := `UPDATE ingest_jobs SET status = ?, error = NULL, progress = 0, started_at = NULL, ended_at = NULL WHERE id = ?`
	args := []any{JobQueued, id}
	if resetAttempts {
		q = `UPDATE ingest_jobs SET status = ?, attempt = 0, error = NULL, progress = 0, started_at = NULL, ended_at = NULL WHERE id = ?`
	}
	if _, err := s.db.Exec(q, args...); err != nil {
		return IngestJob{}, fmt.Errorf("knowledge: requeue ingest: %w", err)
	}
	return s.getIngestLocked(id)
}

// RecoverStaleIngests requeues jobs left in `running` by a crash or restart.
// Called once at startup — this is what makes the pipeline restart-safe given
// the queue backend itself guarantees nothing.
func (s *Store) RecoverStaleIngests() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(
		`UPDATE ingest_jobs SET status = ?, started_at = NULL WHERE status = ?`,
		JobQueued, JobRunning,
	)
	if err != nil {
		return 0, fmt.Errorf("knowledge: recover stale ingests: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// GetIngest returns one job.
func (s *Store) GetIngest(id string) (IngestJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getIngestLocked(id)
}

// ListIngests returns jobs for a KB (or all when kbName is ""), newest first.
func (s *Store) ListIngests(kbName string, limit int) ([]IngestJob, error) {
	if limit <= 0 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	q := `SELECT id, kb_name, title, source, mime_type, spool_path, byte_size,
	             status, attempt, progress, COALESCE(error,''), COALESCE(doc_id,''),
	             created_at, started_at, ended_at
	        FROM ingest_jobs`
	args := []any{}
	if kbName != "" {
		q += ` WHERE kb_name = ?`
		args = append(args, kbName)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("knowledge: list ingests: %w", err)
	}
	defer rows.Close()

	var out []IngestJob
	for rows.Next() {
		j, err := scanIngest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) getIngestLocked(id string) (IngestJob, error) {
	row := s.db.QueryRow(
		`SELECT id, kb_name, title, source, mime_type, spool_path, byte_size,
		        status, attempt, progress, COALESCE(error,''), COALESCE(doc_id,''),
		        created_at, started_at, ended_at
		   FROM ingest_jobs WHERE id = ?`, id)
	return scanIngest(row)
}

// rowScanner unifies *sql.Row and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

func scanIngest(r rowScanner) (IngestJob, error) {
	var j IngestJob
	var started, ended sql.NullTime
	if err := r.Scan(
		&j.ID, &j.KBName, &j.Title, &j.Source, &j.MIMEType, &j.SpoolPath, &j.ByteSize,
		&j.Status, &j.Attempt, &j.Progress, &j.Error, &j.DocID,
		&j.CreatedAt, &started, &ended,
	); err != nil {
		return IngestJob{}, err
	}
	if started.Valid {
		t := started.Time
		j.StartedAt = &t
	}
	if ended.Valid {
		t := ended.Time
		j.EndedAt = &t
	}
	return j, nil
}
