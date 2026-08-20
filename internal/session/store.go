// store.go — session resource store backed by SQLite.
//
// ResourceStore persists binary attachments (images, audio clips, documents)
// for the duration of a session.  Resources are keyed by an opaque ID,
// carry a MIME type, and expire automatically after a configurable TTL.
//
// Security mitigations:
//   - Blobs are stored inside the SQLite database, never on the filesystem,
//     so there is no path-traversal risk.
//   - Put rejects payloads larger than MaxAttachmentSize (default 50 MiB).
//   - A background goroutine calls Prune every 24 h to evict expired rows.
package session

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/internal/wsroot"
)

const resourceSchema = `
CREATE TABLE IF NOT EXISTS session_resources (
    id          TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL DEFAULT 'ws_personal',
    session_id  TEXT NOT NULL DEFAULT '',
    agent_id    TEXT NOT NULL DEFAULT '',
    filename    TEXT NOT NULL DEFAULT '',
    mime_type   TEXT NOT NULL,
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    text        TEXT NOT NULL DEFAULT '',
    data        BLOB NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_resources_expires ON session_resources(expires_at);
CREATE INDEX IF NOT EXISTS idx_resources_session ON session_resources(workspace_id, agent_id, session_id, created_at);
`

// ResourceStore is the interface for storing and retrieving binary session
// resources (attachments).  All operations accept a context for cancellation.
type ResourceStore interface {
	// Put stores data under id with the given MIME type.  The resource expires
	// after ttl.  Returns an error if len(data) exceeds the configured
	// MaxAttachmentSize or if the underlying store fails.
	Put(ctx context.Context, workspaceID, id, mimeType string, data []byte, ttl time.Duration) error

	// Get retrieves the data and MIME type for the resource with the given id.
	// Returns sql.ErrNoRows (via errors.Is) when the id is not found.
	Get(ctx context.Context, workspaceID, id string) (data []byte, mimeType string, err error)

	// Delete removes the resource with the given id.  A no-op if the id does
	// not exist.
	Delete(ctx context.Context, workspaceID, id string) error

	// Prune deletes all rows whose expires_at is in the past.  Returns the
	// number of rows deleted.
	Prune(ctx context.Context) (int64, error)

	// Close releases the underlying database connection.
	Close() error
}

// Attachment is a user-supplied file bound to one chat session.
type Attachment struct {
	ID string `json:"id"`
	// WorkspaceID is the tenant that owns the file. Attachment IDs are
	// server-generated UUIDs, but an ID alone was enough to fetch a row: the
	// only thing standing between one tenant and another's uploads was the
	// gateway remembering to call requireSession first. Ownership belongs in
	// the store, not in the caller's discipline.
	WorkspaceID string    `json:"workspace_id,omitempty"`
	SessionID   string    `json:"session_id"`
	AgentID     string    `json:"agent_id"`
	Filename    string    `json:"filename"`
	MIMEType    string    `json:"mime_type"`
	SizeBytes   int64     `json:"size_bytes"`
	Text        string    `json:"text,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type ExportedAttachment struct {
	Attachment
	Data []byte `json:"data"`
}

// SQLiteStore is the SQLite-backed implementation of ResourceStore.
type SQLiteStore struct {
	db     *sql.DB
	maxLen int64
}

// NewSQLiteStore opens (or creates) the SQLite database at path, applies the
// resource schema, and starts a background pruning goroutine.
//
// cfg may be zero-valued; DefaultConfig() values are used for any unset fields.
func NewSQLiteStore(path string, cfg Config) (*SQLiteStore, error) {
	if cfg.AttachmentTTL <= 0 {
		cfg.AttachmentTTL = DefaultAttachmentTTL
	}
	if cfg.MaxAttachmentSize <= 0 {
		cfg.MaxAttachmentSize = DefaultMaxAttachmentSize
	}

	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("session: open sqlite %s: %w", path, err)
	}

	// Apply schema; run each DDL statement separately — mattn/go-sqlite3 is
	// unreliable with multi-statement strings passed to db.Exec.
	for _, stmt := range splitStmts(resourceSchema) {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("session: schema migration (%q): %w",
				truncate(stmt, 60), err)
		}
	}
	for _, stmt := range resourceCompatMigrations() {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			_ = db.Close()
			return nil, fmt.Errorf("session: compat migration (%q): %w", truncate(stmt, 60), err)
		}
	}

	if err := addResourceWorkspaceColumn(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	s := &SQLiteStore{db: db, maxLen: cfg.MaxAttachmentSize}

	// Background pruning: delete expired rows every 24 h.  The goroutine
	// exits when the database is closed (db.QueryContext returns an error).
	go func() {
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for range t.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, _ = s.Prune(ctx)
			cancel()
			// If the DB was closed, the next Prune call will fail and the
			// goroutine will block on the next tick — this is intentional;
			// the process is assumed to be shutting down shortly after Close.
		}
	}()

	return s, nil
}

// Put stores data under id with the given MIME type and TTL.
// Returns an error when len(data) > s.maxLen.
func (s *SQLiteStore) Put(ctx context.Context, workspaceID, id, mimeType string, data []byte, ttl time.Duration) error {
	workspaceID, wsErr := requireResourceWorkspace(workspaceID)
	if wsErr != nil {
		return wsErr
	}
	if int64(len(data)) > s.maxLen {
		return fmt.Errorf("session: attachment too large (%d bytes, max %d)", len(data), s.maxLen)
	}
	if ttl <= 0 {
		ttl = DefaultAttachmentTTL
	}
	expiresAt := time.Now().UTC().Add(ttl)
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO session_resources (id, mime_type, size_bytes, data, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		id, mimeType, len(data), data, expiresAt, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("session: put resource %q: %w", id, err)
	}
	return nil
}

// PutAttachment stores a file with chat-session metadata and extracted text.
func (s *SQLiteStore) PutAttachment(ctx context.Context, att Attachment, data []byte, ttl time.Duration) error {
	workspaceID, err := requireResourceWorkspace(att.WorkspaceID)
	if err != nil {
		return err
	}
	att.WorkspaceID = workspaceID
	if int64(len(data)) > s.maxLen {
		return fmt.Errorf("session: attachment too large (%d bytes, max %d)", len(data), s.maxLen)
	}
	if ttl <= 0 {
		ttl = DefaultAttachmentTTL
	}
	if att.CreatedAt.IsZero() {
		att.CreatedAt = time.Now().UTC()
	}
	att.ExpiresAt = att.CreatedAt.Add(ttl)
	att.SizeBytes = int64(len(data))
	_, err = s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO session_resources
			(id, workspace_id, session_id, agent_id, filename, mime_type, size_bytes, text, data, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		att.ID, att.WorkspaceID, att.SessionID, att.AgentID, att.Filename, att.MIMEType, att.SizeBytes, att.Text, data, att.CreatedAt, att.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("session: put attachment %q: %w", att.ID, err)
	}
	return nil
}

// ListAttachments returns chat attachments for an agent/session, oldest first.
func (s *SQLiteStore) ListAttachments(ctx context.Context, workspaceID, agentID, sessionID string) ([]Attachment, error) {
	workspaceID, err := requireResourceWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	// Expiry is filtered in the query, not left to the prune sweep. An
	// attachment past its TTL must stop being listable and downloadable at the
	// moment it expires, not whenever housekeeping next happens to run.
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, workspace_id, session_id, agent_id, filename, mime_type, size_bytes, text, created_at, expires_at
		FROM session_resources
		WHERE workspace_id = ? AND agent_id = ? AND session_id = ? AND expires_at > ?
		ORDER BY created_at ASC`, workspaceID, agentID, sessionID, time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("session: list attachments: %w", err)
	}
	defer rows.Close()
	out := []Attachment{}
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.ID, &a.WorkspaceID, &a.SessionID, &a.AgentID, &a.Filename, &a.MIMEType, &a.SizeBytes, &a.Text, &a.CreatedAt, &a.ExpiresAt); err != nil {
			return nil, fmt.Errorf("session: scan attachment: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("session: list attachment rows: %w", err)
	}
	return out, nil
}

func (s *SQLiteStore) ListWorkspaceAttachments(ctx context.Context, workspaceID string) ([]ExportedAttachment, error) {
	workspaceID, err := requireResourceWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, session_id, agent_id, filename,
		mime_type, size_bytes, text, data, created_at, expires_at FROM session_resources
		WHERE workspace_id=? AND expires_at > ? ORDER BY created_at, id`, workspaceID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExportedAttachment
	for rows.Next() {
		var item ExportedAttachment
		if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.SessionID, &item.AgentID, &item.Filename,
			&item.MIMEType, &item.SizeBytes, &item.Text, &item.Data, &item.CreatedAt, &item.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) PurgeWorkspace(ctx context.Context, workspaceID string) (int64, error) {
	workspaceID, err := requireResourceWorkspace(workspaceID)
	if err != nil {
		return 0, err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM session_resources WHERE workspace_id=?`, workspaceID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// GetAttachment returns metadata and blob for one attachment id.
func (s *SQLiteStore) GetAttachment(ctx context.Context, workspaceID, id string) (Attachment, []byte, error) {
	workspaceID, err := requireResourceWorkspace(workspaceID)
	if err != nil {
		return Attachment{}, nil, err
	}
	var a Attachment
	var data []byte
	// The workspace and the expiry are both in the predicate, so an attachment
	// belonging to another tenant and one that has expired are the same answer
	// as an ID that never existed: sql.ErrNoRows. IDs cannot be probed, and a
	// previously-issued download link stops working the moment it expires
	// rather than when the prune sweep next runs.
	err = s.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, session_id, agent_id, filename, mime_type, size_bytes, text, data, created_at, expires_at
		FROM session_resources WHERE id = ? AND workspace_id = ? AND expires_at > ?`, id, workspaceID, time.Now().UTC()).
		Scan(&a.ID, &a.WorkspaceID, &a.SessionID, &a.AgentID, &a.Filename, &a.MIMEType, &a.SizeBytes, &a.Text, &data, &a.CreatedAt, &a.ExpiresAt)
	if err != nil {
		return Attachment{}, nil, fmt.Errorf("session: get attachment %q: %w", id, err)
	}
	return a, data, nil
}

// Get retrieves the blob and MIME type for id.
// Returns sql.ErrNoRows when the resource does not exist.
func (s *SQLiteStore) Get(ctx context.Context, workspaceID, id string) ([]byte, string, error) {
	workspaceID, err := requireResourceWorkspace(workspaceID)
	if err != nil {
		return nil, "", err
	}
	var data []byte
	var mimeType string
	err = s.db.QueryRowContext(ctx,
		`SELECT data, mime_type FROM session_resources WHERE id = ? AND workspace_id = ? AND expires_at > ?`,
		id, workspaceID, time.Now().UTC(),
	).Scan(&data, &mimeType)
	if err != nil {
		return nil, "", fmt.Errorf("session: get resource %q: %w", id, err)
	}
	return data, mimeType, nil
}

// Delete removes the resource with id.  Safe to call for non-existent ids.
func (s *SQLiteStore) Delete(ctx context.Context, workspaceID, id string) error {
	workspaceID, err := requireResourceWorkspace(workspaceID)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`DELETE FROM session_resources WHERE id = ? AND workspace_id = ?`, id, workspaceID,
	)
	if err != nil {
		return fmt.Errorf("session: delete resource %q: %w", id, err)
	}
	return nil
}

// Prune deletes all resources whose expires_at is before now.
// Returns the number of rows deleted.
func (s *SQLiteStore) Prune(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM session_resources WHERE expires_at < ?`, time.Now().UTC(),
	)
	if err != nil {
		return 0, fmt.Errorf("session: prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("session: prune rows affected: %w", err)
	}
	return n, nil
}

// Close releases the database connection.
func (s *SQLiteStore) Close() error { return s.db.Close() }

// --- helpers ----------------------------------------------------------------

// splitStmts splits a multi-statement SQL string on semicolons, returning
// only non-empty trimmed statements.  Mirrors the same helper in
// internal/memory/sqlite.go.
func splitStmts(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ';' {
			stmt := trim(s[start:i])
			if stmt != "" {
				out = append(out, stmt)
			}
			start = i + 1
		}
	}
	if stmt := trim(s[start:]); stmt != "" {
		out = append(out, stmt)
	}
	return out
}

func trim(s string) string {
	// Manual trim to avoid importing strings just for this helper.
	b := []byte(s)
	lo, hi := 0, len(b)
	for lo < hi && isSpace(b[lo]) {
		lo++
	}
	for hi > lo && isSpace(b[hi-1]) {
		hi--
	}
	return string(b[lo:hi])
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func resourceCompatMigrations() []string {
	return []string{
		`ALTER TABLE session_resources ADD COLUMN session_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE session_resources ADD COLUMN agent_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE session_resources ADD COLUMN filename TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE session_resources ADD COLUMN size_bytes INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE session_resources ADD COLUMN text TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE session_resources ADD COLUMN created_at DATETIME NOT NULL DEFAULT '1970-01-01T00:00:00Z'`,
		`CREATE INDEX IF NOT EXISTS idx_resources_session ON session_resources(agent_id, session_id, created_at)`,
	}
}

// requireResourceWorkspace normalizes a tenant and refuses an absent one. An
// attachment with no owner is a file every tenant can download.
func requireResourceWorkspace(workspaceID string) (string, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		// Shares the package's ErrWorkspaceRequired (declared in history.go):
		// an attachment with no owner is a file every tenant can download, the
		// same failure a conversation turn with no owner is.
		return "", ErrWorkspaceRequired
	}
	return wsroot.Normalize(workspaceID), nil
}

// addResourceWorkspaceColumn brings a pre-tenant resource store up to the
// current schema and assigns its rows to the personal workspace.
//
// The backfill re-runs on every open. A row with an empty workspace matches no
// scoped query, so it is not a leak — it is an attachment that silently became
// undownloadable while still occupying disk until its TTL expired.
func addResourceWorkspaceColumn(db *sql.DB) error {
	if _, err := db.Exec(
		`ALTER TABLE session_resources ADD COLUMN workspace_id TEXT NOT NULL DEFAULT 'ws_personal'`,
	); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("session: add resource workspace column: %w", err)
	}
	if _, err := db.Exec(
		`UPDATE session_resources SET workspace_id = ? WHERE workspace_id IS NULL OR workspace_id = ''`,
		wsroot.PersonalWorkspaceID,
	); err != nil {
		return fmt.Errorf("session: backfill resource workspace: %w", err)
	}
	return nil
}
