// Package dlq provides a dead-letter queue store for failed executor jobs.
package dlq

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/internal/workspacepurge"
	"github.com/soulacy/soulacy/internal/wsroot"
)

const tsLayout = "2006-01-02 15:04:05"

// ErrNotFound is returned when a requested entry does not exist in the store.
var ErrNotFound = fmt.Errorf("dlq: entry not found")

// DeadLetter is a failed job entry.
type DeadLetter struct {
	ID string `json:"id"`
	// WorkspaceID is the tenant whose run failed. It is captured from the
	// run's principal at push time, not inferred when the entry is read: a
	// dead letter outlives the request that produced it by design, and the
	// whole point of the row is that nobody was there to catch it.
	//
	// Payload is the original job — a chat message with its prompt, its agent,
	// and whatever the user typed — so a dead letter is tenant content, not
	// deployment telemetry, even though it is served from an admin route.
	WorkspaceID   string    `json:"workspace_id"`
	Queue         string    `json:"queue"`    // source queue name
	Payload       []byte    `json:"payload"`  // original job payload (JSON)
	ErrorMsg      string    `json:"error"`    // last error message
	Attempts      int       `json:"attempts"` // how many times it was tried
	CreatedAt     time.Time `json:"created_at"`
	LastAttemptAt time.Time `json:"last_attempt_at"`
}

// Store is the interface for the dead-letter queue backend.
//
// Every read takes the workspace as a positional argument rather than reading
// it from the context. That is deliberate: the dead-letter queue is one
// database shared by the whole deployment, so the tenant is the only thing
// separating one team's parked jobs from another's, and a parameter a caller
// must supply is harder to forget than a filter it may omit.
type Store interface {
	// Push adds a failed job to the dead-letter queue. item.WorkspaceID names
	// the tenant whose run failed; an empty value is recorded as the personal
	// workspace rather than rejected (see SQLiteStore.Push).
	Push(ctx context.Context, item DeadLetter) error

	// List returns the workspace's dead-letter entries, newest first.
	// If queue is non-empty, filters to that queue only.
	List(ctx context.Context, workspaceID, queue string) ([]DeadLetter, error)

	// Get returns a single entry by ID within the workspace. An entry that
	// belongs to another tenant is reported as ErrNotFound, which is also what
	// a caller sees for an ID that does not exist — the two are deliberately
	// indistinguishable.
	Get(ctx context.Context, workspaceID, id string) (DeadLetter, error)

	// Delete permanently removes an entry within the workspace (use after
	// successful manual retry or when the entry is no longer needed).
	Delete(ctx context.Context, workspaceID, id string) error

	// Close releases resources.
	Close() error
}

// NewID generates a random hex ID for a DeadLetter entry.
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// NoopStore
// ---------------------------------------------------------------------------

// NoopStore is a no-op implementation of Store. Use it when DLQ is not
// configured; all operations succeed silently.
type NoopStore struct{}

func (NoopStore) Push(_ context.Context, _ DeadLetter) error { return nil }

func (NoopStore) List(_ context.Context, _, _ string) ([]DeadLetter, error) { return nil, nil }

func (NoopStore) Get(_ context.Context, _, _ string) (DeadLetter, error) {
	return DeadLetter{}, ErrNotFound
}

func (NoopStore) Delete(_ context.Context, _, _ string) error { return nil }

func (NoopStore) Close() error { return nil }

// ---------------------------------------------------------------------------
// SQLiteStore
// ---------------------------------------------------------------------------

const schema = `
CREATE TABLE IF NOT EXISTS dead_letters (
    id              TEXT PRIMARY KEY,
    queue           TEXT NOT NULL,
    payload         BLOB NOT NULL,
    error_msg       TEXT NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 1,
    created_at      DATETIME NOT NULL,
    last_attempt_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_dlq_queue   ON dead_letters(queue);
CREATE INDEX IF NOT EXISTS idx_dlq_created ON dead_letters(created_at DESC);
`

// schemaV2 adds the tenant column and makes it the leading key of every index
// a read uses. Leading, not trailing: a workspace-first index means the busiest
// tenant's backlog does not lengthen another tenant's scan, so isolation and
// fairness land in the same change.
const schemaV2 = `
ALTER TABLE dead_letters ADD COLUMN workspace_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_dlq_ws_created ON dead_letters(workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_dlq_ws_queue   ON dead_letters(workspace_id, queue, created_at DESC);
`

// SQLiteStore is a SQLite-backed implementation of Store.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (or creates) the dead-letter SQLite database at path
// and ensures the schema is present.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}

	// Schema versioning (E22 adoption): v1 = the idempotent bootstrap above;
	// future changes go through sqlitex.MigrateSchema with v2+.
	if err := sqlitex.RecordSchemaVersion(db, "dlq", 1); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := sqlitex.MigrateSchema(db, "dlq", []sqlitex.SchemaMigration{
		{Version: 2, SQL: schemaV2},
	}); err != nil {
		db.Close()
		return nil, err
	}
	if err := backfillWorkspace(db); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

// backfillWorkspace assigns pre-tenant dead letters to the personal workspace,
// which is whose jobs they were.
//
// It runs on every open rather than only inside the versioned migration. A row
// with an empty workspace matches no scoped read, so it is not a leak — it is
// a parked job that no longer appears in anybody's queue, which is worse. The
// operator would see an empty dead-letter list and conclude nothing failed.
// Reachable by an interrupted upgrade or a database restored from a backup
// taken mid-migration, so the repair is unconditional and idempotent.
func backfillWorkspace(db *sql.DB) error {
	_, err := db.Exec(
		`UPDATE dead_letters SET workspace_id = ? WHERE workspace_id IS NULL OR TRIM(workspace_id) = ''`,
		wsroot.PersonalWorkspaceID)
	if err != nil {
		return fmt.Errorf("dlq: backfill workspace: %w", err)
	}
	return nil
}

// Push inserts or replaces a dead-letter entry. If an entry with the same ID
// already exists, it is overwritten (upsert semantics).
func (s *SQLiteStore) Push(ctx context.Context, item DeadLetter) error {
	createdAt := item.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	lastAttemptAt := item.LastAttemptAt
	if lastAttemptAt.IsZero() {
		lastAttemptAt = time.Now().UTC()
	}
	if item.Attempts < 1 {
		item.Attempts = 1
	}
	// An empty workspace is normalised, not rejected. Push is the failure path
	// already — the run it describes has run out of retries — so refusing the
	// insert would turn "we could not attribute this job" into "this job never
	// existed". Personal is the safe destination: it is the tenant a
	// single-user deployment's rows already carry, it is where an operator
	// looks, and it is nobody else's workspace, so a misattributed entry is
	// visible rather than leaked.
	//
	// The upsert is an explicit ON CONFLICT ... WHERE rather than INSERT OR
	// REPLACE. The primary key is the ID alone, so a REPLACE whose ID happened
	// to match a row in another workspace would overwrite that row — a
	// cross-tenant *write* through a store whose reads are all scoped. IDs are
	// server-generated today and colliding is not something a caller can
	// arrange, but the guard belongs in the statement rather than in the habits
	// of every future caller. Same-workspace re-pushes keep their overwrite
	// semantics; a cross-workspace collision updates nothing and is reported.
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO dead_letters
		    (id, workspace_id, queue, payload, error_msg, attempts, created_at, last_attempt_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		    queue = excluded.queue,
		    payload = excluded.payload,
		    error_msg = excluded.error_msg,
		    attempts = excluded.attempts,
		    created_at = excluded.created_at,
		    last_attempt_at = excluded.last_attempt_at
		 WHERE dead_letters.workspace_id = excluded.workspace_id`,
		item.ID,
		wsroot.Normalize(item.WorkspaceID),
		item.Queue,
		item.Payload,
		item.ErrorMsg,
		item.Attempts,
		createdAt.UTC().Format(tsLayout),
		lastAttemptAt.UTC().Format(tsLayout),
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("dlq: entry %q belongs to another workspace", item.ID)
	}
	return nil
}

// List returns all dead-letter entries ordered newest first. If queue is
// non-empty only entries from that queue are returned.
func (s *SQLiteStore) List(ctx context.Context, workspaceID, queue string) ([]DeadLetter, error) {
	var (
		rows *sql.Rows
		err  error
	)
	workspace := wsroot.Normalize(workspaceID)
	if queue == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, workspace_id, queue, payload, error_msg, attempts, created_at, last_attempt_at
			 FROM dead_letters
			 WHERE workspace_id = ?
			 ORDER BY created_at DESC`,
			workspace)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, workspace_id, queue, payload, error_msg, attempts, created_at, last_attempt_at
			 FROM dead_letters
			 WHERE workspace_id = ? AND queue = ?
			 ORDER BY created_at DESC`,
			workspace, queue)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DeadLetter
	for rows.Next() {
		dl, err := scanDeadLetter(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dl)
	}
	return out, rows.Err()
}

// Get returns the dead-letter entry with the given ID, or ErrNotFound.
func (s *SQLiteStore) Get(ctx context.Context, workspaceID, id string) (DeadLetter, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, workspace_id, queue, payload, error_msg, attempts, created_at, last_attempt_at
		 FROM dead_letters
		 WHERE workspace_id = ? AND id = ?`,
		wsroot.Normalize(workspaceID), id)
	dl, err := scanDeadLetterRow(row)
	if err == sql.ErrNoRows {
		return DeadLetter{}, ErrNotFound
	}
	return dl, err
}

// Delete removes a dead-letter entry by ID. Returns ErrNotFound if no row
// was deleted.
// The workspace is part of the DELETE predicate rather than checked with a
// preceding Get: a read-then-delete would be two statements a concurrent write
// can slip between, and here the thing being raced is the authority check.
func (s *SQLiteStore) Delete(ctx context.Context, workspaceID, id string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM dead_letters WHERE workspace_id = ? AND id = ?`,
		wsroot.Normalize(workspaceID), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Close closes the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// ---------------------------------------------------------------------------
// scan helpers
// ---------------------------------------------------------------------------

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanDeadLetter(r rowScanner) (DeadLetter, error) {
	var dl DeadLetter
	// Scan time.Time directly so the mattn/go-sqlite3 driver handles the
	// format conversion (same fix as apikeys, memory, session).
	if err := r.Scan(
		&dl.ID,
		&dl.WorkspaceID,
		&dl.Queue,
		&dl.Payload,
		&dl.ErrorMsg,
		&dl.Attempts,
		&dl.CreatedAt,
		&dl.LastAttemptAt,
	); err != nil {
		return DeadLetter{}, err
	}
	return dl, nil
}

func scanDeadLetterRow(r *sql.Row) (DeadLetter, error) {
	return scanDeadLetter(r)
}

// PurgeWorkspace removes every row this store holds for one workspace.
//
// MU-032 criterion 4. The TABLE LIST comes from the ownership catalog rather
// than from a literal here, so a table added to the "queue-dlq" resource is
// purged the day it is classified — one edit, not two. A hand-written list is
// the same second-inventory mistake the exporter avoids, and here the
// consequence of drift is data outliving a deletion somebody was told
// completed.
func (s *SQLiteStore) PurgeWorkspace(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
	return workspacepurge.PurgeCatalogTables(ctx, s.db, "queue-dlq", workspaceID)
}
