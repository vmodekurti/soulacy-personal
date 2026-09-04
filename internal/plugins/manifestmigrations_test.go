package plugins

// Story 17: manifest-declared plugin migrations — declared in plugin.yaml,
// validated at load (refusal = warn+skip with diagnostic), exposed for the
// E16 runner.

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestLoader_ManifestMigrationsValidLoad(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "mig", `id: mig
name: Mig
manifest_schema: 2
migrations:
  - name: 001_items
    up_sql: CREATE TABLE plugin_mig_items (id INTEGER PRIMARY KEY, name TEXT)
  - name: 002_index
    up_sql: CREATE INDEX plugin_mig_items_name ON plugin_mig_items(name)
`)
	l := New([]string{root}, zap.NewNop())
	if l.Count() != 1 {
		t.Fatalf("count = %d (diags=%+v)", l.Count(), l.Diagnostics())
	}

	pending := ManifestMigrations(l.All())
	if len(pending) != 2 {
		t.Fatalf("ManifestMigrations = %d, want 2", len(pending))
	}
	if pending[0].PluginID != "mig" || pending[0].Name != "001_items" {
		t.Errorf("pending[0] = %+v", pending[0])
	}
	if !strings.Contains(pending[1].UpSQL, "CREATE INDEX") {
		t.Errorf("pending[1] = %+v", pending[1])
	}
}

func TestLoader_ManifestMigrationForeignTableRefused(t *testing.T) {
	root := t.TempDir()
	// Targets a core table outside the plugin_<id>_ namespace.
	writePlugin(t, root, "evil", `id: evil
name: Evil
migrations:
  - name: 001_smash
    up_sql: DROP TABLE agents
`)
	l := New([]string{root}, zap.NewNop())
	if l.Count() != 0 {
		t.Fatalf("namespace-violating migration must refuse the plugin (count=%d)", l.Count())
	}
	diags := l.Diagnostics()
	if len(diags) != 1 || !strings.Contains(diags[0].Reason, "migrations[0]") {
		t.Errorf("diagnostics = %+v", diags)
	}
}

func TestLoader_ManifestMigrationMissingFieldsRefused(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "incomplete", `id: incomplete
name: Incomplete
migrations:
  - name: ""
    up_sql: CREATE TABLE plugin_incomplete_x (id INTEGER)
`)
	l := New([]string{root}, zap.NewNop())
	if l.Count() != 0 {
		t.Fatal("migration without a name must refuse the plugin")
	}
}

func TestLoader_ManifestMigrationDuplicateNameRefused(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "dup", `id: dup
name: Dup
migrations:
  - name: 001_a
    up_sql: CREATE TABLE plugin_dup_a (id INTEGER)
  - name: 001_a
    up_sql: CREATE TABLE plugin_dup_b (id INTEGER)
`)
	l := New([]string{root}, zap.NewNop())
	if l.Count() != 0 {
		t.Fatal("duplicate migration names must refuse the plugin")
	}
}
