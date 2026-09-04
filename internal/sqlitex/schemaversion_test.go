package sqlitex

// Story E22 (1): shared schema-version table + additive-only migration
// helper. Pre-upgrade fixtures prove old databases upgrade cleanly with
// data preserved.

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

var v1v2 = []SchemaMigration{
	{Version: 1, SQL: `CREATE TABLE things (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`},
	{Version: 2, SQL: `ALTER TABLE things ADD COLUMN color TEXT NOT NULL DEFAULT ''`},
}

func TestMigrateSchema_FreshDatabase(t *testing.T) {
	db := openTestDB(t)
	applied, err := MigrateSchema(db, "teststore", v1v2)
	if err != nil {
		t.Fatalf("MigrateSchema: %v", err)
	}
	if applied != 2 {
		t.Errorf("applied = %d, want 2", applied)
	}
	v, err := SchemaVersion(db, "teststore")
	if err != nil || v != 2 {
		t.Errorf("SchemaVersion = %d err=%v, want 2", v, err)
	}
	if _, err := db.Exec(`INSERT INTO things (name, color) VALUES ('a', 'red')`); err != nil {
		t.Errorf("schema unusable after migrate: %v", err)
	}
}

// The pre-upgrade fixture: a database created by an older binary (v1 only,
// with data) upgrades to v2 with rows intact and v1 NOT re-applied.
func TestMigrateSchema_PreUpgradeFixture(t *testing.T) {
	db := openTestDB(t)
	if _, err := MigrateSchema(db, "teststore", v1v2[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO things (name) VALUES ('legacy-row')`); err != nil {
		t.Fatal(err)
	}

	applied, err := MigrateSchema(db, "teststore", v1v2)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if applied != 1 {
		t.Errorf("applied = %d, want 1 (v1 must not re-apply)", applied)
	}
	var name, color string
	if err := db.QueryRow(`SELECT name, color FROM things`).Scan(&name, &color); err != nil {
		t.Fatalf("data lost in upgrade: %v", err)
	}
	if name != "legacy-row" || color != "" {
		t.Errorf("row = (%q, %q), want legacy-row with zero-value color", name, color)
	}

	// Idempotent: nothing to do on the third run.
	if applied, _ := MigrateSchema(db, "teststore", v1v2); applied != 0 {
		t.Errorf("re-run applied = %d, want 0", applied)
	}
}

func TestMigrateSchema_ComponentsIsolated(t *testing.T) {
	db := openTestDB(t)
	if _, err := MigrateSchema(db, "store-a", []SchemaMigration{
		{Version: 1, SQL: `CREATE TABLE a (x INTEGER)`},
	}); err != nil {
		t.Fatal(err)
	}
	v, err := SchemaVersion(db, "store-b")
	if err != nil || v != 0 {
		t.Errorf("unknown component version = %d err=%v, want 0/nil", v, err)
	}
}

// Additive-only guard: destructive statements are refused unless the
// migration explicitly opts in (deliberate deprecation cycle).
func TestMigrateSchema_RefusesDestructive(t *testing.T) {
	db := openTestDB(t)
	if _, err := MigrateSchema(db, "t", v1v2[:1]); err != nil {
		t.Fatal(err)
	}
	_, err := MigrateSchema(db, "t", []SchemaMigration{
		v1v2[0],
		{Version: 2, SQL: `DROP TABLE things`},
	})
	if err == nil || !strings.Contains(err.Error(), "destructive") {
		t.Errorf("DROP must be refused without Destructive flag: %v", err)
	}
	// version must not advance past the refused step
	if v, _ := SchemaVersion(db, "t"); v != 1 {
		t.Errorf("version after refusal = %d, want 1", v)
	}

	// Explicit opt-in is honoured.
	if _, err := MigrateSchema(db, "t", []SchemaMigration{
		v1v2[0],
		{Version: 2, SQL: `DROP TABLE things`, Destructive: true},
	}); err != nil {
		t.Errorf("opted-in destructive migration refused: %v", err)
	}
}

func TestMigrateSchema_FailedStepRollsBack(t *testing.T) {
	db := openTestDB(t)
	_, err := MigrateSchema(db, "t", []SchemaMigration{
		{Version: 1, SQL: `CREATE TABLE ok (x INTEGER)`},
		{Version: 2, SQL: `THIS IS NOT SQL`},
	})
	if err == nil {
		t.Fatal("broken migration must error")
	}
	// v1 applied, v2 not recorded.
	if v, _ := SchemaVersion(db, "t"); v != 1 {
		t.Errorf("version = %d, want 1", v)
	}
	if _, err := db.Exec(`INSERT INTO ok (x) VALUES (1)`); err != nil {
		t.Errorf("v1 table missing after failed v2: %v", err)
	}
}

func TestMigrateSchema_ValidatesOrdering(t *testing.T) {
	db := openTestDB(t)
	if _, err := MigrateSchema(db, "t", []SchemaMigration{
		{Version: 2, SQL: `CREATE TABLE b (x INTEGER)`},
		{Version: 1, SQL: `CREATE TABLE a (x INTEGER)`},
	}); err == nil {
		t.Error("out-of-order versions must be refused")
	}
	if _, err := MigrateSchema(db, "t", []SchemaMigration{
		{Version: 1, SQL: `CREATE TABLE a (x INTEGER)`},
		{Version: 1, SQL: `CREATE TABLE b (x INTEGER)`},
	}); err == nil {
		t.Error("duplicate versions must be refused")
	}
}

func TestRecordSchemaVersion_NeverDowngrades(t *testing.T) {
	db := openTestDB(t)
	if err := RecordSchemaVersion(db, "legacy", 1); err != nil {
		t.Fatal(err)
	}
	if v, _ := SchemaVersion(db, "legacy"); v != 1 {
		t.Fatalf("version = %d, want 1", v)
	}
	// Idempotent re-record.
	if err := RecordSchemaVersion(db, "legacy", 1); err != nil {
		t.Fatal(err)
	}
	// A future MigrateSchema bumps to 3; the boot-time Record(1) must not
	// pull it back down.
	if _, err := MigrateSchema(db, "legacy", []SchemaMigration{
		{Version: 2, SQL: `CREATE TABLE l2 (x INTEGER)`},
		{Version: 3, SQL: `CREATE TABLE l3 (x INTEGER)`},
	}); err != nil {
		t.Fatal(err)
	}
	if err := RecordSchemaVersion(db, "legacy", 1); err != nil {
		t.Fatal(err)
	}
	if v, _ := SchemaVersion(db, "legacy"); v != 3 {
		t.Errorf("version after re-record = %d, want 3 (no downgrade)", v)
	}
}
