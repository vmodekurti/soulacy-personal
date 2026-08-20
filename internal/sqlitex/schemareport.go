package sqlitex

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
)

// schemareport.go — MU-035 criterion 3: migrations must be "observable".
//
// Every store recorded its schema version and NOTHING read it back. The
// versions were correct, the migrations were transactional, the additive-only
// guard held — and an operator asking "did the upgrade actually apply" had no
// way to find out short of opening each database file with a SQLite client.
//
// That matters most during the exact event the versioning exists for. A rolling
// upgrade puts two binaries against one set of databases, and the question an
// operator needs answered in that window — has every component reached the
// version this binary expects — was unanswerable from the running system.
//
// It is also what makes the backup gate usable. A destructive migration now
// leaves a snapshot; a snapshot nobody can list is a snapshot nobody will find.

// SchemaState is one component's recorded version plus any snapshot taken
// before a destructive step.
type SchemaState struct {
	Component string         `json:"component"`
	Version   int            `json:"version"`
	Backups   []SchemaBackup `json:"backups,omitempty"`
}

// SchemaReport lists every component in this database with its version and
// backups, ordered by component name.
//
// Reads the version table rather than taking a list of expected components,
// deliberately: a component that stopped recording its version would be
// INVISIBLE to a list-driven report, and "the component is missing" is the
// most interesting thing the report can say. Whether the set is the expected
// one is the caller's question, not this function's.
func SchemaReport(db *sql.DB) ([]SchemaState, error) {
	if _, err := db.Exec(schemaVersionTable); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT component, version FROM soulacy_schema_version`)
	if err != nil {
		return nil, err
	}
	states := map[string]*SchemaState{}
	for rows.Next() {
		var state SchemaState
		if err := rows.Scan(&state.Component, &state.Version); err != nil {
			rows.Close()
			return nil, err
		}
		copied := state
		states[state.Component] = &copied
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	backups, err := SchemaBackups(db)
	if err != nil {
		return nil, err
	}
	for _, backup := range backups {
		state, known := states[backup.Component]
		if !known {
			// A snapshot for a component with no recorded version. Kept rather
			// than dropped: it means a destructive migration ran and the
			// version record did not survive, which is the single most
			// alarming state this report can describe, and dropping the row
			// would make it look like nothing happened.
			states[backup.Component] = &SchemaState{Component: backup.Component, Version: 0}
			state = states[backup.Component]
		}
		state.Backups = append(state.Backups, backup)
	}

	out := make([]SchemaState, 0, len(states))
	for _, state := range states {
		out = append(out, *state)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Component < out[b].Component })
	return out, nil
}

// DatabaseSchemaState is one database file's report.
type DatabaseSchemaState struct {
	// File is the database's base name, not its full path. An operator needs
	// to know WHICH database; the directory it sits in is deployment topology
	// and this report is served over HTTP.
	File       string        `json:"file"`
	Components []SchemaState `json:"components"`
	Error      string        `json:"error,omitempty"`
}

// ReportDir reports the schema state of every SQLite database in dir.
//
// DISCOVERS rather than enumerates, and that is the point. A list of expected
// databases cannot report the one nobody wired — a store added without
// versioning, or a file left behind by a migration — and those are the rows an
// operator most wants to see. It is the same choice the ownership catalog's
// discovery scan makes.
//
// Opened READ ONLY. A report is a read, and a reporting endpoint that could
// create a database, apply a pending migration, or take a write lock on a live
// store would be a diagnostic with side effects — the kind operators learn not
// to run during an incident, which is when they need it.
//
// A file that cannot be opened or read is reported WITH its error rather than
// skipped. A corrupt database silently absent from the report reads as a
// database that is fine.
func ReportDir(dir string) ([]DatabaseSchemaState, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []DatabaseSchemaState
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".db" {
			continue
		}
		state := DatabaseSchemaState{File: entry.Name()}
		// mode=ro rather than a plain path: a plain path CREATES the file when
		// it is missing, which would make a report of a directory quietly add
		// databases to it.
		db, openErr := sql.Open("sqlite3", "file:"+filepath.Join(dir, entry.Name())+"?mode=ro")
		if openErr != nil {
			state.Error = openErr.Error()
			out = append(out, state)
			continue
		}
		components, reportErr := schemaReportReadOnly(db)
		_ = db.Close()
		if reportErr != nil {
			state.Error = reportErr.Error()
		} else {
			state.Components = components
		}
		out = append(out, state)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].File < out[b].File })
	return out, nil
}

// schemaReportReadOnly is SchemaReport without the CREATE TABLE IF NOT EXISTS
// calls, which a read-only connection cannot make.
//
// A database with no version table reports NO components rather than an error:
// that is a legitimate state — a store that predates versioning entirely — and
// an error there would bury the interesting failures under expected ones.
func schemaReportReadOnly(db *sql.DB) ([]SchemaState, error) {
	states := map[string]*SchemaState{}
	rows, err := db.Query(`SELECT component, version FROM soulacy_schema_version`)
	if err == nil {
		for rows.Next() {
			var state SchemaState
			if scanErr := rows.Scan(&state.Component, &state.Version); scanErr != nil {
				rows.Close()
				return nil, scanErr
			}
			copied := state
			states[state.Component] = &copied
		}
		rows.Close()
	}

	backupRows, err := db.Query(`SELECT component, version, path, bytes, taken_at FROM soulacy_schema_backups`)
	if err == nil {
		for backupRows.Next() {
			var backup SchemaBackup
			if scanErr := backupRows.Scan(&backup.Component, &backup.Version, &backup.Path,
				&backup.Bytes, &backup.TakenAt); scanErr != nil {
				backupRows.Close()
				return nil, scanErr
			}
			if _, known := states[backup.Component]; !known {
				states[backup.Component] = &SchemaState{Component: backup.Component}
			}
			states[backup.Component].Backups = append(states[backup.Component].Backups, backup)
		}
		backupRows.Close()
	}

	out := make([]SchemaState, 0, len(states))
	for _, state := range states {
		out = append(out, *state)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Component < out[b].Component })
	return out, nil
}
