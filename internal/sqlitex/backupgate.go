package sqlitex

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// backupgate.go — MU-035 criterion 4: "destructive migrations require an
// explicit backup gate."
//
// WHAT `Destructive: true` MEANT BEFORE. It was an opt-out from the
// additive-only check and nothing else. Any migration could set it, and the
// only thing standing between a mistaken DROP and permanent data loss was that
// somebody had typed a word acknowledging the risk. A field that says "I know
// this is dangerous" is a comment with a compiler behind it; it is not a gate.
//
// WHAT IT MEANS NOW. Before a destructive step runs, the migrator takes a real
// snapshot of the database and records where it put it. If the snapshot cannot
// be taken, the migration does not run. That is what makes it a gate rather
// than an acknowledgement: the protection is a side effect the step cannot skip
// without failing.
//
// VACUUM INTO, NOT A FILE COPY. Copying the file behind a live connection can
// capture a torn page or miss the WAL entirely, producing a backup that
// restores to a database SQLite refuses to open — the worst possible outcome,
// because it looks like a backup right up to the moment somebody needs it.
// VACUUM INTO writes a consistent snapshot through SQLite itself, at a
// transaction boundary, WAL included. It needs SQLite 3.27+, which every
// version this project builds against has.
//
// WHY THE RECORD MATTERS AS MUCH AS THE FILE. An operator recovering from a bad
// migration has to find the backup, and "look next to the database for
// something with a plausible name" is not a recovery procedure. The row names
// the component, the version it was taken before, the path, the size, and when
// — enough to answer "is this the right backup" without opening it.

const schemaBackupTable = `
CREATE TABLE IF NOT EXISTS soulacy_schema_backups (
    component   TEXT NOT NULL,
    version     INTEGER NOT NULL,
    path        TEXT NOT NULL,
    bytes       INTEGER NOT NULL,
    taken_at    TIMESTAMP NOT NULL,
    PRIMARY KEY (component, version)
)`

// SchemaBackup is one recorded pre-migration snapshot.
type SchemaBackup struct {
	Component string    `json:"component"`
	Version   int       `json:"version"`
	Path      string    `json:"path"`
	Bytes     int64     `json:"bytes"`
	TakenAt   time.Time `json:"taken_at"`
}

// backupBeforeDestructive snapshots db and records the snapshot.
//
// Returns an error the caller must treat as fatal to the migration. There is
// deliberately no "best effort" path: a destructive step that proceeded after a
// failed backup would be indistinguishable, from the outside, from one that
// backed up successfully — and the difference only becomes visible when
// somebody needs to restore.
func backupBeforeDestructive(db *sql.DB, component string, version int) (SchemaBackup, error) {
	source, err := databaseFilePath(db)
	if err != nil {
		return SchemaBackup{}, err
	}
	if source == "" {
		// An in-memory or temporary database has no file to snapshot. Refused
		// rather than waived: a destructive migration against a database that
		// cannot be backed up is exactly the case the gate is for, and the
		// only caller who legitimately hits this is a test — which should say
		// so by not marking its migration destructive.
		return SchemaBackup{}, fmt.Errorf(
			"sqlitex: %s v%d is destructive but this database has no file to back up (in-memory or temporary)",
			component, version)
	}

	// Named for the version it precedes, not for the time. An operator asking
	// "what did this look like before v7" should not have to correlate
	// timestamps, and a fixed name makes a re-run of the same migration
	// overwrite its own snapshot rather than accumulate one per attempt.
	destination := fmt.Sprintf("%s.pre-%s-v%d.backup", source, sanitizeComponent(component), version)
	// Removed first: VACUUM INTO refuses an existing destination, and a failed
	// earlier attempt would otherwise block every retry with an error that
	// says nothing about the real problem.
	if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
		return SchemaBackup{}, fmt.Errorf("sqlitex: clear stale backup %s: %w", destination, err)
	}
	if _, err := db.Exec(`VACUUM INTO ?`, destination); err != nil {
		return SchemaBackup{}, fmt.Errorf("sqlitex: back up %s before v%d: %w", component, version, err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		return SchemaBackup{}, fmt.Errorf("sqlitex: backup %s is not readable after writing: %w", destination, err)
	}
	if info.Size() == 0 {
		// A zero-byte backup is a file that will be mistaken for one.
		return SchemaBackup{}, fmt.Errorf("sqlitex: backup %s is empty", destination)
	}

	backup := SchemaBackup{
		Component: component, Version: version, Path: destination,
		Bytes: info.Size(), TakenAt: time.Now().UTC(),
	}
	if _, err := db.Exec(schemaBackupTable); err != nil {
		return SchemaBackup{}, fmt.Errorf("sqlitex: backup table: %w", err)
	}
	if _, err := db.Exec(
		`INSERT INTO soulacy_schema_backups (component, version, path, bytes, taken_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(component, version) DO UPDATE SET
		   path = excluded.path, bytes = excluded.bytes, taken_at = excluded.taken_at`,
		backup.Component, backup.Version, backup.Path, backup.Bytes, backup.TakenAt); err != nil {
		return SchemaBackup{}, fmt.Errorf("sqlitex: record backup: %w", err)
	}
	return backup, nil
}

// SchemaBackups returns every recorded pre-migration snapshot, newest first.
//
// The read surface for the record: an operator deciding whether to restore
// needs the list, and reconstructing it by globbing a directory would miss
// exactly the case that matters — a backup somebody has since moved.
func SchemaBackups(db *sql.DB) ([]SchemaBackup, error) {
	if _, err := db.Exec(schemaBackupTable); err != nil {
		return nil, err
	}
	rows, err := db.Query(
		`SELECT component, version, path, bytes, taken_at FROM soulacy_schema_backups ORDER BY taken_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SchemaBackup
	for rows.Next() {
		var backup SchemaBackup
		if err := rows.Scan(&backup.Component, &backup.Version, &backup.Path, &backup.Bytes, &backup.TakenAt); err != nil {
			return nil, err
		}
		out = append(out, backup)
	}
	return out, rows.Err()
}

// databaseFilePath returns the main database's file, or "" for an in-memory or
// temporary one.
//
// Read from SQLite rather than threaded down from Open: the migrator is handed
// a *sql.DB and nothing else, and adding a path parameter to MigrateSchema
// would make every caller supply something only this one code path needs — and
// supply it correctly, which is a second source of truth about where the
// database is.
func databaseFilePath(db *sql.DB) (string, error) {
	rows, err := db.Query(`PRAGMA database_list`)
	if err != nil {
		return "", fmt.Errorf("sqlitex: database_list: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return "", err
		}
		if name == "main" {
			return strings.TrimSpace(file), nil
		}
	}
	return "", rows.Err()
}

// sanitizeComponent makes a component name safe in a filename.
func sanitizeComponent(component string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, component)
	if safe == "" {
		return "component"
	}
	return filepath.Base(safe)
}
