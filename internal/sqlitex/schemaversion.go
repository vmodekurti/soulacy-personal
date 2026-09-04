package sqlitex

// Schema versioning (Story E22). Every SQLite store can record its schema
// version in ONE shared table and apply migrations through one helper with
// uniform guarantees:
//
//   - one transaction per migration step — a failed step rolls back and
//     leaves the recorded version at the last good step;
//   - additive-only by default — DROP/RENAME statements are refused unless
//     the migration sets Destructive (the explicit deprecation-cycle
//     opt-in);
//   - idempotent — versions ≤ the recorded version are skipped, so calling
//     MigrateSchema at every boot is the intended usage.
//
// The version table is per-component so several stores can share a database
// file without coordinating version numbers.

import (
	"database/sql"
	"fmt"
	"regexp"
	"time"
)

// SchemaMigration is one versioned schema step for MigrateSchema.
type SchemaMigration struct {
	// Version is the schema version this step brings the component to.
	// Steps must be listed in strictly ascending order.
	Version int
	// SQL is the migration statement(s).
	SQL string
	// Destructive opts this step out of the additive-only guard. Use only
	// for deliberate deprecation cycles (documented in the store's package
	// comment), never for routine changes.
	Destructive bool
}

const schemaVersionTable = `
CREATE TABLE IF NOT EXISTS soulacy_schema_version (
    component  TEXT PRIMARY KEY,
    version    INTEGER NOT NULL,
    updated_at TIMESTAMP NOT NULL
)`

// destructiveRe flags statements that remove or rename schema objects.
var destructiveRe = regexp.MustCompile(`(?i)\b(DROP\s+(TABLE|COLUMN|INDEX|VIEW|TRIGGER)|RENAME\s+(TO|COLUMN))\b`)

// RecordSchemaVersion marks component as being at LEAST version — used by
// stores whose v1 bootstrap predates schema versioning (idempotent
// CREATE TABLE IF NOT EXISTS schemas). It never downgrades: a database
// already migrated past version keeps its higher number, so calling this
// at every boot right after the legacy bootstrap is safe. Future schema
// changes then go through MigrateSchema with versions > 1.
func RecordSchemaVersion(db *sql.DB, component string, version int) error {
	current, err := SchemaVersion(db, component)
	if err != nil {
		return err
	}
	if current >= version {
		return nil
	}
	_, err = db.Exec(`INSERT INTO soulacy_schema_version (component, version, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(component) DO UPDATE SET version = excluded.version, updated_at = excluded.updated_at`,
		component, version, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("sqlitex: record %s v%d: %w", component, version, err)
	}
	return nil
}

// SchemaVersion returns the recorded schema version for component
// (0 when the component has never migrated).
func SchemaVersion(db *sql.DB, component string) (int, error) {
	if _, err := db.Exec(schemaVersionTable); err != nil {
		return 0, fmt.Errorf("sqlitex: ensure version table: %w", err)
	}
	var v int
	err := db.QueryRow(`SELECT version FROM soulacy_schema_version WHERE component = ?`, component).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("sqlitex: read schema version: %w", err)
	}
	return v, nil
}

// MigrateSchema applies every migration with Version greater than the
// component's recorded version, in order, one transaction per step.
// Returns the number of steps applied.
func MigrateSchema(db *sql.DB, component string, migrations []SchemaMigration) (int, error) {
	if component == "" {
		return 0, fmt.Errorf("sqlitex: component must not be empty")
	}
	// Validate ordering before touching the database.
	last := 0
	for i, m := range migrations {
		if m.Version <= 0 {
			return 0, fmt.Errorf("sqlitex: migration %d: version must be positive", i)
		}
		if m.Version <= last {
			return 0, fmt.Errorf("sqlitex: migrations must be strictly ascending (version %d after %d)", m.Version, last)
		}
		last = m.Version
	}

	current, err := SchemaVersion(db, component)
	if err != nil {
		return 0, err
	}

	applied := 0
	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		if !m.Destructive && destructiveRe.MatchString(m.SQL) {
			return applied, fmt.Errorf(
				"sqlitex: %s v%d is destructive (DROP/RENAME) — additive-only by default; set Destructive for a deliberate deprecation cycle",
				component, m.Version)
		}
		tx, err := db.Begin()
		if err != nil {
			return applied, fmt.Errorf("sqlitex: begin %s v%d: %w", component, m.Version, err)
		}
		if _, err := tx.Exec(m.SQL); err != nil {
			_ = tx.Rollback()
			return applied, fmt.Errorf("sqlitex: %s v%d failed (rolled back): %w", component, m.Version, err)
		}
		if _, err := tx.Exec(`INSERT INTO soulacy_schema_version (component, version, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(component) DO UPDATE SET version = excluded.version, updated_at = excluded.updated_at`,
			component, m.Version, time.Now().UTC()); err != nil {
			_ = tx.Rollback()
			return applied, fmt.Errorf("sqlitex: record %s v%d: %w", component, m.Version, err)
		}
		if err := tx.Commit(); err != nil {
			return applied, fmt.Errorf("sqlitex: commit %s v%d: %w", component, m.Version, err)
		}
		current = m.Version
		applied++
	}
	return applied, nil
}
