// sqlite.go — long-term memory archive backed by SQLite.
// Uses plain indexed columns for search (no FTS5 required) so it works
// with any system libsqlite3, including the macOS built-in.
// WAL mode gives safe concurrent reads from the file store + CLI.
package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/internal/workspacepurge"
	"github.com/soulacy/soulacy/internal/wsroot"
)

var archiveSQLiteVecAuto sync.Once

const schema = `
CREATE TABLE IF NOT EXISTS memories (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL DEFAULT 'ws_personal',
    agent_id    TEXT NOT NULL,
    session_id  TEXT NOT NULL,
    scope       TEXT NOT NULL,
    provenance  TEXT NOT NULL,
    key         TEXT,
    content     TEXT NOT NULL,
    metadata    TEXT,
    created_at  DATETIME NOT NULL,
    expires_at  DATETIME
);

CREATE INDEX IF NOT EXISTS idx_memories_agent   ON memories(workspace_id, agent_id);
CREATE INDEX IF NOT EXISTS idx_memories_session ON memories(workspace_id, session_id);
CREATE INDEX IF NOT EXISTS idx_memories_scope   ON memories(scope);
CREATE INDEX IF NOT EXISTS idx_memories_created ON memories(created_at DESC);
`

// SQLiteArchive is the long-term memory backend.
// All writes to FileStore are mirrored here for durable, searchable storage.
type SQLiteArchive struct {
	db *sql.DB
}

// NewSQLiteArchive opens (or creates) the SQLite database at path.
func NewSQLiteArchive(path string) (*SQLiteArchive, error) {
	// Register sqlite-vec before sqlitex opens its first SQLite connection.
	// Previously this happened incidentally only when the Knowledge subsystem
	// was enabled, leaving native vector memory unavailable in valid setups
	// that used agent memory without a knowledge base.
	archiveSQLiteVecAuto.Do(sqlite_vec.Auto)
	// PRODUCTION_AUDIT → F3 (2026-05-27): WAL + NORMAL synchronous + 30s
	// busy_timeout + tuned pool via internal/sqlitex. Lets CLI/GUI readers
	// coexist with gateway archive writes without lock contention.
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("memory: open sqlite %s: %w", path, err)
	}

	// Run each DDL statement separately — db.Exec with multi-statement strings
	// is unreliable with mattn/go-sqlite3.
	stmts := splitSQL(schema)
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return nil, fmt.Errorf("memory: schema migration (%q): %w", stmt[:minInt(len(stmt), 60)], err)
		}
	}

	// An existing archive predates the tenant column. Add it in place and
	// backfill to the personal workspace, which is what those rows were: a
	// single-user installation's memory. Doing this rather than defaulting the
	// column at read time means the predicate below is never optional.
	if err := addWorkspaceColumn(db); err != nil {
		return nil, err
	}

	// Schema versioning (E22 adoption): v1 = the idempotent bootstrap above;
	// v2 adds the workspace boundary.
	if err := sqlitex.RecordSchemaVersion(db, "memory_archive", 2); err != nil {
		return nil, err
	}

	return &SQLiteArchive{db: db}, nil
}

// Archive writes an entry to the SQLite archive. Duplicate IDs are silently ignored.
func (a *SQLiteArchive) Archive(e Entry) error {
	if strings.TrimSpace(e.WorkspaceID) == "" {
		return ErrWorkspaceRequired
	}
	meta, _ := json.Marshal(e.Metadata)
	_, err := a.db.Exec(`
		INSERT OR IGNORE INTO memories
			(id, workspace_id, agent_id, session_id, scope, provenance, key, content, metadata, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.WorkspaceID, e.AgentID, e.SessionID, e.Scope, "", // provenance column kept for schema compat, no longer used
		e.Key, e.Content, string(meta), e.CreatedAt, e.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("memory: archive insert: %w", err)
	}
	return nil
}

// Search performs a LIKE-based substring search across memory content for an agent.
// Results are ordered newest-first. For large datasets consider adding a vector
// DB backend (set memory.vector_db in config.yaml).
func (a *SQLiteArchive) Search(workspaceID, agentID, query string, limit int) ([]Entry, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, ErrWorkspaceRequired
	}
	rows, err := a.db.Query(`
		SELECT id, workspace_id, agent_id, session_id, scope, provenance, key,
		       content, metadata, created_at, expires_at
		FROM memories
		WHERE workspace_id = ? AND agent_id = ? AND content LIKE ?
		ORDER BY created_at DESC
		LIMIT ?`,
		workspaceID, agentID, "%"+query+"%", limit,
	)
	if err != nil {
		return nil, fmt.Errorf("memory: search query: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// ReadByScope returns archived entries filtered by scope and session, newest first.
func (a *SQLiteArchive) ReadByScope(workspaceID, agentID, sessionID string, scope Scope, limit int) ([]Entry, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, ErrWorkspaceRequired
	}
	rows, err := a.db.Query(`
		SELECT id, workspace_id, agent_id, session_id, scope, provenance, key,
		       content, metadata, created_at, expires_at
		FROM memories
		WHERE workspace_id = ? AND agent_id = ? AND session_id = ? AND scope = ?
		ORDER BY created_at DESC
		LIMIT ?`,
		workspaceID, agentID, sessionID, scope, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

// ReadGlobal returns the most recent entries across all sessions for an agent.
func (a *SQLiteArchive) ReadGlobal(workspaceID, agentID string, limit int) ([]Entry, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, ErrWorkspaceRequired
	}
	rows, err := a.db.Query(`
		SELECT id, workspace_id, agent_id, session_id, scope, provenance, key,
		       content, metadata, created_at, expires_at
		FROM memories
		WHERE workspace_id = ? AND agent_id = ?
		ORDER BY created_at DESC
		LIMIT ?`,
		workspaceID, agentID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

// Prune deletes entries older than before for a given agent.
// Returns the number of rows deleted.
func (a *SQLiteArchive) Prune(agentID string, before time.Time) (int64, error) {
	res, err := a.db.Exec(
		`DELETE FROM memories WHERE agent_id = ? AND created_at < ?`, agentID, before,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Stats returns a row count and approximate size for a given agent's memories.
func (a *SQLiteArchive) Stats(agentID string) (count int64, err error) {
	err = a.db.QueryRow(
		`SELECT COUNT(*) FROM memories WHERE agent_id = ?`, agentID,
	).Scan(&count)
	return
}

func (a *SQLiteArchive) Close() error { return a.db.Close() }

func (a *SQLiteArchive) ExportWorkspaceJSONL(ctx context.Context, workspaceID string, w io.Writer) (int64, error) {
	if err := wsroot.Validate(strings.TrimSpace(workspaceID)); err != nil {
		return 0, err
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT id, workspace_id, agent_id, session_id, scope, provenance, key,
		       content, metadata, created_at, expires_at
		FROM memories WHERE workspace_id = ? ORDER BY created_at ASC, id ASC`, wsroot.Normalize(workspaceID))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	entries, err := scanEntries(rows)
	if err != nil {
		return 0, err
	}
	encoder := json.NewEncoder(w)
	for i, entry := range entries {
		if err := encoder.Encode(struct {
			Tier  string `json:"tier"`
			Entry Entry  `json:"entry"`
		}{Tier: "archive", Entry: entry}); err != nil {
			return int64(i), err
		}
	}
	return int64(len(entries)), nil
}

func (a *SQLiteArchive) PurgeWorkspace(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
	if err := wsroot.Validate(strings.TrimSpace(workspaceID)); err != nil {
		return workspacepurge.Removed{}, err
	}
	result, err := a.db.ExecContext(ctx, `DELETE FROM memories WHERE workspace_id = ?`, wsroot.Normalize(workspaceID))
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	rows, err := result.RowsAffected()
	return workspacepurge.Removed{Rows: rows, Note: "durable memory archive rows"}, err
}

// DB returns the underlying *sql.DB for callers (e.g. VectorStore) that need
// to share the same connection pool. Only use for tables NOT managed by
// SQLiteArchive itself.
func (a *SQLiteArchive) DB() *sql.DB { return a.db }

// --- helpers ---

func scanEntries(rows *sql.Rows) ([]Entry, error) {
	var entries []Entry
	for rows.Next() {
		var e Entry
		var meta string
		var expiresAt sql.NullTime
		var ignoredProvenance string // retained in schema for compat, discarded on read
		if err := rows.Scan(
			&e.ID, &e.WorkspaceID, &e.AgentID, &e.SessionID, &e.Scope, &ignoredProvenance,
			&e.Key, &e.Content, &meta, &e.CreatedAt, &expiresAt,
		); err != nil {
			return nil, err
		}
		if expiresAt.Valid {
			t := expiresAt.Time
			e.ExpiresAt = &t
		}
		if meta != "" {
			_ = json.Unmarshal([]byte(meta), &e.Metadata)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// splitSQL splits a multi-statement SQL string on semicolons,
// returning only non-empty trimmed statements.
func splitSQL(s string) []string {
	parts := strings.Split(s, ";")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// addWorkspaceColumn brings an archive created before tenants existed up to
// the current schema. It is idempotent: PRAGMA table_info is the check, and a
// duplicate-column error from a concurrent process is treated as success.
func addWorkspaceColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(memories)`)
	if err != nil {
		return fmt.Errorf("memory: inspect schema: %w", err)
	}
	present := false
	for rows.Next() {
		var (
			cid                 int
			name, columnType    string
			notNull, primaryKey int
			defaultValue        sql.NullString
		)
		if scanErr := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); scanErr != nil {
			rows.Close()
			return scanErr
		}
		if name == "workspace_id" {
			present = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if !present {
		if _, err := db.Exec(`ALTER TABLE memories ADD COLUMN workspace_id TEXT NOT NULL DEFAULT 'ws_personal'`); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				return fmt.Errorf("memory: add workspace column: %w", err)
			}
		}
	}
	// Backfill unconditionally, not only when the column was just added. A row
	// with an empty workspace matches no scoped query, so it is not a leak —
	// it is memory that has silently disappeared, which is worse. This can
	// happen to a database whose migration was interrupted, or one written to
	// directly. The statement is cheap and idempotent.
	if _, err := db.Exec(`UPDATE memories SET workspace_id='ws_personal' WHERE workspace_id IS NULL OR workspace_id=''`); err != nil {
		return fmt.Errorf("memory: backfill workspace: %w", err)
	}
	return nil
}
