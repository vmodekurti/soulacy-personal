// backupgate_test.go — a destructive migration must leave a restorable copy.
//
// Every assertion opens the backup and reads the dropped data out of it. That
// is the only thing that distinguishes a gate from a file with a plausible
// name: a torn or empty snapshot passes any test that only checks the path
// exists, and it passes right up until somebody needs to restore from it.
package sqlitex

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func migrationDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := Open(path, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO widgets (name) VALUES ('irreplaceable')`); err != nil {
		t.Fatal(err)
	}
	if err := RecordSchemaVersion(db, "widgets", 1); err != nil {
		t.Fatal(err)
	}
	return db, path
}

func TestADestructiveMigrationLeavesARestorableBackup(t *testing.T) {
	db, _ := migrationDB(t)

	applied, err := MigrateSchema(db, "widgets", []SchemaMigration{
		{Version: 2, SQL: `DROP TABLE widgets`, Destructive: true},
	})
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}

	backups, err := SchemaBackups(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("recorded %d backups, want 1 — an operator cannot find what is not recorded", len(backups))
	}
	backup := backups[0]
	if backup.Component != "widgets" || backup.Version != 2 {
		t.Errorf("backup names %s v%d, want widgets v2", backup.Component, backup.Version)
	}

	// THE ASSERTION THAT MATTERS. A backup that opens and still contains the
	// dropped row is a backup; a file of the right size is not. VACUUM INTO
	// rather than a file copy is what makes this pass — a copy taken behind a
	// live connection can miss the WAL and produce a database SQLite refuses
	// to open.
	restored, err := sql.Open("sqlite3", backup.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var name string
	if err := restored.QueryRow(`SELECT name FROM widgets`).Scan(&name); err != nil {
		t.Fatalf("the backup does not contain the dropped table: %v", err)
	}
	if name != "irreplaceable" {
		t.Errorf("restored %q, want the row that was dropped", name)
	}
	// And the live database really did lose it, or the test proves nothing
	// about a destructive migration.
	if err := db.QueryRow(`SELECT name FROM widgets`).Scan(&name); err == nil {
		t.Error("the migration did not actually drop anything")
	}
}

func TestAnAdditiveMigrationTakesNoBackup(t *testing.T) {
	// The gate must not fire on ordinary changes. A snapshot per additive
	// migration would double every store's disk footprint on every boot that
	// applies one, and operators would disable the mechanism to reclaim the
	// space — taking the destructive protection with it.
	db, path := migrationDB(t)

	if _, err := MigrateSchema(db, "widgets", []SchemaMigration{
		{Version: 2, SQL: `ALTER TABLE widgets ADD COLUMN colour TEXT NOT NULL DEFAULT ''`},
	}); err != nil {
		t.Fatal(err)
	}

	backups, err := SchemaBackups(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Errorf("an additive migration took a backup: %+v", backups)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".backup" {
			t.Errorf("an additive migration wrote %s", entry.Name())
		}
	}
}

func TestADestructiveMigrationIsRefusedWhenItCannotBeBackedUp(t *testing.T) {
	// An in-memory database has no file to snapshot. Refused rather than
	// waived, because "cannot be backed up" is exactly the condition the gate
	// exists for — and waiving it would make the gate absent precisely where
	// it is impossible to satisfy, which is a rule that protects only the
	// cases that were already safe.
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE widgets (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := RecordSchemaVersion(db, "widgets", 1); err != nil {
		t.Fatal(err)
	}

	applied, err := MigrateSchema(db, "widgets", []SchemaMigration{
		{Version: 2, SQL: `DROP TABLE widgets`, Destructive: true},
	})
	if err == nil {
		t.Fatal("a destructive migration ran against a database that cannot be backed up")
	}
	if applied != 0 {
		t.Errorf("applied = %d, want 0", applied)
	}
	// And it must not have run anyway.
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'widgets'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Error("the table was dropped despite the refusal")
	}
}

func TestTheBackupPrecedesTheDropRatherThanFollowingIt(t *testing.T) {
	// Ordering, and it is the whole value. A snapshot taken after the
	// destructive step is a snapshot of the damage — same file, same size,
	// same record, and useless. Asserted by dropping a table and then reading
	// it back out of the backup: only a pre-drop snapshot can satisfy that.
	db, _ := migrationDB(t)

	if _, err := MigrateSchema(db, "widgets", []SchemaMigration{
		{Version: 2, SQL: `DROP TABLE widgets`, Destructive: true},
	}); err != nil {
		t.Fatal(err)
	}
	backups, err := SchemaBackups(db)
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups = %+v, err = %v", backups, err)
	}

	restored, err := sql.Open("sqlite3", backups[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var count int
	if err := restored.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'widgets'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Error("the backup was taken after the drop, so it records the damage rather than what preceded it")
	}
}

func TestARetriedDestructiveMigrationOverwritesItsOwnSnapshot(t *testing.T) {
	// A failed migration is retried at the next boot, and each attempt would
	// otherwise leave a snapshot behind. Unbounded accumulation of
	// database-sized files is how a protection becomes an outage.
	db, path := migrationDB(t)

	for i := 0; i < 3; i++ {
		// A statement that always fails AFTER the backup has been taken.
		_, err := MigrateSchema(db, "widgets", []SchemaMigration{
			{Version: 2, SQL: `DROP TABLE widgets; DROP TABLE does_not_exist`, Destructive: true},
		})
		if err == nil {
			t.Fatal("the migration was expected to fail")
		}
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	snapshots := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".backup" {
			snapshots++
		}
	}
	if snapshots != 1 {
		t.Errorf("%d snapshots after three attempts, want 1", snapshots)
	}
}
