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
)

const tsLayout = "2006-01-02 15:04:05"

// ErrNotFound is returned when a requested entry does not exist in the store.
var ErrNotFound = fmt.Errorf("dlq: entry not found")

// DeadLetter is a failed job entry.
type DeadLetter struct {
	ID            string    `json:"id"`
	Queue         string    `json:"queue"`    // source queue name
	Payload       []byte    `json:"payload"`  // original job payload (JSON)
	ErrorMsg      string    `json:"error"`    // last error message
	Attempts      int       `json:"attempts"` // how many times it was tried
	CreatedAt     time.Time `json:"created_at"`
	LastAttemptAt time.Time `json:"last_attempt_at"`
}

// Store is the interface for the dead-letter queue backend.
type Store interface {
	// Push adds a failed job to the dead-letter queue.
	Push(ctx context.Context, item DeadLetter) error

	// List returns all dead-letter entries, newest first.
	// If queue is non-empty, filters to that queue only.
	List(ctx context.Context, queue string) ([]DeadLetter, error)

	// Get returns a single entry by ID. Returns ErrNotFound if absent.
	Get(ctx context.Context, id string) (DeadLetter, error)

	// Delete permanently removes an entry (use after successful manual retry
	// or when the entry is no longer needed).
	Delete(ctx context.Context, id string) error

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

func (NoopStore) List(_ context.Context, _ string) ([]DeadLetter, error) { return nil, nil }

func (NoopStore) Get(_ context.Context, _ string) (DeadLetter, error) {
	return DeadLetter{}, ErrNotFound
}

func (NoopStore) Delete(_ context.Context, _ string) error { return nil }

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
	return &SQLiteStore{db: db}, nil
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
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO dead_letters
		    (id, queue, payload, error_msg, attempts, created_at, last_attempt_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		item.ID,
		item.Queue,
		item.Payload,
		item.ErrorMsg,
		item.Attempts,
		createdAt.UTC().Format(tsLayout),
		lastAttemptAt.UTC().Format(tsLayout),
	)
	return err
}

// List returns all dead-letter entries ordered newest first. If queue is
// non-empty only entries from that queue are returned.
func (s *SQLiteStore) List(ctx context.Context, queue string) ([]DeadLetter, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if queue == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, queue, payload, error_msg, attempts, created_at, last_attempt_at
			 FROM dead_letters
			 ORDER BY created_at DESC`)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, queue, payload, error_msg, attempts, created_at, last_attempt_at
			 FROM dead_letters
			 WHERE queue = ?
			 ORDER BY created_at DESC`,
			queue)
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
func (s *SQLiteStore) Get(ctx context.Context, id string) (DeadLetter, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, queue, payload, error_msg, attempts, created_at, last_attempt_at
		 FROM dead_letters
		 WHERE id = ?`,
		id)
	dl, err := scanDeadLetterRow(row)
	if err == sql.ErrNoRows {
		return DeadLetter{}, ErrNotFound
	}
	return dl, err
}

// Delete removes a dead-letter entry by ID. Returns ErrNotFound if no row
// was deleted.
func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM dead_letters WHERE id = ?`, id)
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
