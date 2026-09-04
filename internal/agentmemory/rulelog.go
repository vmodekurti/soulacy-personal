package agentmemory

// RuleLog — versioned agent rulebooks (Story E23, audit response).
//
// The auditor's strongest finding: auto-updating rulebooks overwrote
// procedural.md with no history, making behavioural drift invisible and
// irreversible. RuleLog gives every rule write an immutable version row in
// SQLite (<memory>/rulebook.db) keyed (agent_id, version) with provenance
// ('auto_update' | 'manual' | 'rollback'); locks freeze an agent's rules
// entirely (auto AND manual writes refused) until an operator unlocks;
// rollback re-applies an old version AS A NEW VERSION — history is never
// rewritten. The .md file remains the serving copy so every existing read
// path (context injection, GUI GET) is untouched.

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3" // sqlite driver

	"github.com/soulacy/soulacy/internal/sqlitex"
)

// ErrRulebookLocked is returned for any write against a locked agent.
var ErrRulebookLocked = errors.New("agentmemory: rulebook is locked — unlock it before changing rules")

// RuleVersion is one rulebook history entry (rules text fetched separately).
type RuleVersion struct {
	Version   int       `json:"version"`
	Source    string    `json:"source"` // auto_update | manual | rollback
	Size      int       `json:"size"`   // bytes of the rules text
	CreatedAt time.Time `json:"created_at"`
}

// RuleLog is the SQLite-backed version store.
type RuleLog struct {
	db *sql.DB
}

// OpenRuleLog opens (creating if needed) the rulebook database at path.
func OpenRuleLog(path string) (*RuleLog, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("agentmemory: open rulebook db: %w", err)
	}
	if _, err := sqlitex.MigrateSchema(db, "rulebook", []sqlitex.SchemaMigration{
		{Version: 1, SQL: `CREATE TABLE IF NOT EXISTS rulebook_versions (
			agent_id   TEXT NOT NULL,
			version    INTEGER NOT NULL,
			rules      TEXT NOT NULL,
			source     TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY (agent_id, version)
		)`},
		{Version: 2, SQL: `CREATE TABLE IF NOT EXISTS rulebook_locks (
			agent_id   TEXT PRIMARY KEY,
			locked     INTEGER NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`},
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &RuleLog{db: db}, nil
}

// Close releases the database.
func (rl *RuleLog) Close() error { return rl.db.Close() }

// Append records a new rulebook version for agentID and returns its number.
func (rl *RuleLog) Append(agentID, rules, source string) (int, error) {
	tx, err := rl.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	var next int
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(version), 0) + 1 FROM rulebook_versions WHERE agent_id = ?`,
		agentID).Scan(&next); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(
		`INSERT INTO rulebook_versions (agent_id, version, rules, source, created_at) VALUES (?, ?, ?, ?, ?)`,
		agentID, next, rules, source, time.Now().UTC()); err != nil {
		return 0, err
	}
	return next, tx.Commit()
}

// Versions lists an agent's history, newest first.
func (rl *RuleLog) Versions(agentID string) ([]RuleVersion, error) {
	rows, err := rl.db.Query(
		`SELECT version, source, LENGTH(rules), created_at FROM rulebook_versions
		 WHERE agent_id = ? ORDER BY version DESC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RuleVersion
	for rows.Next() {
		var v RuleVersion
		if err := rows.Scan(&v.Version, &v.Source, &v.Size, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Get returns the rules text of one version.
func (rl *RuleLog) Get(agentID string, version int) (string, error) {
	var rules string
	err := rl.db.QueryRow(
		`SELECT rules FROM rulebook_versions WHERE agent_id = ? AND version = ?`,
		agentID, version).Scan(&rules)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("agentmemory: rulebook %s has no version %d", agentID, version)
	}
	return rules, err
}

// Locked reports whether agentID's rulebook is frozen.
func (rl *RuleLog) Locked(agentID string) bool {
	var locked int
	if err := rl.db.QueryRow(
		`SELECT locked FROM rulebook_locks WHERE agent_id = ?`, agentID).Scan(&locked); err != nil {
		return false
	}
	return locked != 0
}

// SetLocked freezes or unfreezes an agent's rulebook.
func (rl *RuleLog) SetLocked(agentID string, locked bool) error {
	v := 0
	if locked {
		v = 1
	}
	_, err := rl.db.Exec(
		`INSERT INTO rulebook_locks (agent_id, locked, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(agent_id) DO UPDATE SET locked = excluded.locked, updated_at = excluded.updated_at`,
		agentID, v, time.Now().UTC())
	return err
}
