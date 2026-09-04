// sqlite.go — long-term memory archive backed by SQLite.
// Uses plain indexed columns for search (no FTS5 required) so it works
// with any system libsqlite3, including the macOS built-in.
// WAL mode gives safe concurrent reads from the file store + CLI.
package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
)

var archiveSQLiteVecAuto sync.Once

const schema = `
CREATE TABLE IF NOT EXISTS memories (
    id          TEXT PRIMARY KEY,
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

CREATE INDEX IF NOT EXISTS idx_memories_agent   ON memories(agent_id);
CREATE INDEX IF NOT EXISTS idx_memories_session ON memories(session_id);
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

	// Schema versioning (E22 adoption): v1 = the idempotent bootstrap above;
	// future changes go through sqlitex.MigrateSchema with v2+.
	if err := sqlitex.RecordSchemaVersion(db, "memory_archive", 1); err != nil {
		return nil, err
	}

	return &SQLiteArchive{db: db}, nil
}

// Archive writes an entry to the SQLite archive. Duplicate IDs are silently ignored.
func (a *SQLiteArchive) Archive(e Entry) error {
	meta, _ := json.Marshal(e.Metadata)
	_, err := a.db.Exec(`
		INSERT OR IGNORE INTO memories
			(id, agent_id, session_id, scope, provenance, key, content, metadata, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.AgentID, e.SessionID, e.Scope, "", // provenance column kept for schema compat, no longer used
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
func (a *SQLiteArchive) Search(agentID, query string, limit int) ([]Entry, error) {
	rows, err := a.db.Query(`
		SELECT id, agent_id, session_id, scope, provenance, key,
		       content, metadata, created_at, expires_at
		FROM memories
		WHERE agent_id = ? AND content LIKE ?
		ORDER BY created_at DESC
		LIMIT ?`,
		agentID, "%"+query+"%", limit,
	)
	if err != nil {
		return nil, fmt.Errorf("memory: search query: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// ReadByScope returns archived entries filtered by scope and session, newest first.
func (a *SQLiteArchive) ReadByScope(agentID, sessionID string, scope Scope, limit int) ([]Entry, error) {
	rows, err := a.db.Query(`
		SELECT id, agent_id, session_id, scope, provenance, key,
		       content, metadata, created_at, expires_at
		FROM memories
		WHERE agent_id = ? AND session_id = ? AND scope = ?
		ORDER BY created_at DESC
		LIMIT ?`,
		agentID, sessionID, scope, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

// ReadGlobal returns the most recent entries across all sessions for an agent.
func (a *SQLiteArchive) ReadGlobal(agentID string, limit int) ([]Entry, error) {
	rows, err := a.db.Query(`
		SELECT id, agent_id, session_id, scope, provenance, key,
		       content, metadata, created_at, expires_at
		FROM memories
		WHERE agent_id = ?
		ORDER BY created_at DESC
		LIMIT ?`,
		agentID, limit,
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
			&e.ID, &e.AgentID, &e.SessionID, &e.Scope, &ignoredProvenance,
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
