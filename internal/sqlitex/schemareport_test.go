// schemareport_test.go — the report has to find what nobody listed.
package sqlitex

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestTheReportDiscoversADatabaseNobodyRegistered(t *testing.T) {
	dir := t.TempDir()

	// A versioned store, as every wired store looks.
	known, err := Open(filepath.Join(dir, "runs.db"), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordSchemaVersion(known, "runs", 2); err != nil {
		t.Fatal(err)
	}
	_ = known.Close()

	// A database with no version table at all: a store added without
	// versioning, or a file a migration left behind. A list-driven report
	// cannot see this, and it is the row an operator most needs.
	stray, err := sql.Open("sqlite3", filepath.Join(dir, "mystery.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stray.Exec(`CREATE TABLE things (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	_ = stray.Close()

	databases, err := ReportDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	byFile := map[string]DatabaseSchemaState{}
	for _, database := range databases {
		byFile[database.File] = database
	}
	if len(byFile) != 2 {
		t.Fatalf("reported %d databases, want 2: %+v", len(byFile), databases)
	}
	runs, ok := byFile["runs.db"]
	if !ok || len(runs.Components) != 1 || runs.Components[0].Version != 2 {
		t.Errorf("runs.db reported as %+v, want one component at v2", runs)
	}
	mystery, ok := byFile["mystery.db"]
	if !ok {
		t.Fatal("an unversioned database is missing from the report — a list of expected databases " +
			"cannot see the one nobody wired, which is the row that matters")
	}
	if len(mystery.Components) != 0 {
		t.Errorf("mystery.db reported components: %+v", mystery.Components)
	}
	if mystery.Error != "" {
		t.Errorf("a database with no version table was reported as an error (%q); that is a "+
			"legitimate state and burying it hides the real failures", mystery.Error)
	}
}

func TestTheReportDoesNotWriteToTheDatabasesItReads(t *testing.T) {
	// A diagnostic with side effects is one operators learn not to run during
	// an incident, which is when they need it.
	//
	// The first version of this test asserted that reporting an EMPTY
	// directory created no files, and it passed with mode=ro removed — because
	// ReportDir only iterates over files that already exist, so opening a
	// plain path never creates anything. The premise was wrong: mode=ro is
	// about not WRITING to a live store, not about not creating one. This
	// asserts the real property.
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE things (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	_ = legacy.Close()

	if _, err := ReportDir(dir); err != nil {
		t.Fatal(err)
	}

	// A store that predates versioning must come back from the report exactly
	// as it went in. SchemaReport's CREATE TABLE IF NOT EXISTS calls would add
	// a version table to somebody else's database as a side effect of looking
	// at it, which is why the read-only path exists separately.
	reopened, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var tables int
	if err := reopened.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name LIKE 'soulacy_%'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Errorf("the report added %d soulacy_* tables to a database it was only reading", tables)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "legacy.db" && filepath.Ext(entry.Name()) == ".db" {
			t.Errorf("the report created %s", entry.Name())
		}
	}
}

func TestTheReportSurfacesAPreMigrationSnapshot(t *testing.T) {
	// The backup gate's whole value depends on this: a snapshot an operator
	// cannot list is a snapshot they will not find.
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "widgets.db"), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE widgets (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := RecordSchemaVersion(db, "widgets", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateSchema(db, "widgets", []SchemaMigration{
		{Version: 2, SQL: `DROP TABLE widgets`, Destructive: true},
	}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	databases, err := ReportDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, database := range databases {
		for _, component := range database.Components {
			for _, backup := range component.Backups {
				if backup.Component == "widgets" && backup.Version == 2 && backup.Path != "" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("the pre-migration snapshot is absent from the report, so nobody can locate it")
	}
}
