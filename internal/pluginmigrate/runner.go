package pluginmigrate

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
	sdkstorage "github.com/soulacy/soulacy/sdk/storage"
)

// Runner applies plugin migrations to the dedicated plugin database.
type Runner struct {
	db *sql.DB
}

// Open opens (creating if needed) the plugin database and its bookkeeping
// table.
func Open(path string) (*Runner, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("pluginmigrate: open %s: %w", path, err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS plugin_schema_migrations (
		plugin_id  TEXT NOT NULL,
		name       TEXT NOT NULL,
		checksum   TEXT NOT NULL,
		applied_at DATETIME NOT NULL,
		PRIMARY KEY (plugin_id, name)
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("pluginmigrate: bookkeeping schema: %w", err)
	}
	return &Runner{db: db}, nil
}

// DB exposes the plugin database (plugin-facing query surfaces, tests).
func (r *Runner) DB() *sql.DB { return r.db }

// Close releases the database handle.
func (r *Runner) Close() error { return r.db.Close() }

// Apply runs every pending migration, one transaction per step, in the
// given order. Semantics:
//
//   - already-applied (plugin_id, name) pairs are skipped; a checksum
//     mismatch on an applied step is an error (history must not be rewritten);
//   - a failed or refused step rolls back atomically, is NOT recorded, and
//     stops that plugin's remaining steps (later steps depend on earlier ones);
//   - other plugins continue — one broken plugin never blocks the rest
//     (matching the loader's warn-and-skip policy).
//
// Returns the number of migrations applied and the per-plugin errors.
func (r *Runner) Apply(migrations []sdkstorage.Migration) (applied int, errs []error) {
	skipPlugin := map[string]bool{}
	for _, m := range migrations {
		if skipPlugin[m.PluginID] {
			continue
		}
		ok, err := r.applyOne(m)
		if err != nil {
			errs = append(errs, fmt.Errorf("plugin %s migration %s: %w", m.PluginID, m.Name, err))
			skipPlugin[m.PluginID] = true
			continue
		}
		if ok {
			applied++
		}
	}
	return applied, errs
}

func (r *Runner) applyOne(m sdkstorage.Migration) (applied bool, err error) {
	sum := checksum(m.UpSQL)

	var existing string
	row := r.db.QueryRow(`SELECT checksum FROM plugin_schema_migrations WHERE plugin_id = ? AND name = ?`, m.PluginID, m.Name)
	switch scanErr := row.Scan(&existing); scanErr {
	case nil:
		if existing != sum {
			return false, fmt.Errorf("already applied with different SQL (checksum %s ≠ %s) — register a new migration name instead of editing history", existing[:8], sum[:8])
		}
		return false, nil // applied previously — skip
	case sql.ErrNoRows:
		// pending — fall through
	default:
		return false, fmt.Errorf("bookkeeping query: %w", scanErr)
	}

	// Namespace + statement-kind enforcement BEFORE any SQL executes.
	if verr := Validate(m.PluginID, m.UpSQL); verr != nil {
		return false, verr
	}

	tx, err := r.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, stmt := range splitStatements(m.UpSQL) {
		if _, err = tx.Exec(stmt); err != nil {
			return false, fmt.Errorf("exec %.60q: %w", stmt, err)
		}
	}
	if _, err = tx.Exec(`INSERT INTO plugin_schema_migrations (plugin_id, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		m.PluginID, m.Name, sum, time.Now().UTC().Truncate(time.Second)); err != nil {
		return false, fmt.Errorf("record: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

func checksum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
