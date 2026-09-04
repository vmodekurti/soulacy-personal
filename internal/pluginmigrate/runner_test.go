package pluginmigrate

import (
	"path/filepath"
	"strings"
	"testing"

	sdkstorage "github.com/soulacy/soulacy/sdk/storage"
)

func newTestRunner(t *testing.T) *Runner {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "plugins.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// E16 integration: a plugin migrates and queries its own tables.
func TestApplyAndQueryOwnTables(t *testing.T) {
	r := newTestRunner(t)
	ms := []sdkstorage.Migration{
		{PluginID: "demo", Name: "001_create", UpSQL: "CREATE TABLE plugin_demo_items (id TEXT PRIMARY KEY, body TEXT)"},
		{PluginID: "demo", Name: "002_seed", UpSQL: "INSERT INTO plugin_demo_items (id, body) VALUES ('a', 'hello')"},
	}
	applied, errs := r.Apply(ms)
	if len(errs) > 0 {
		t.Fatalf("apply errors: %v", errs)
	}
	if applied != 2 {
		t.Fatalf("applied = %d, want 2", applied)
	}

	var body string
	if err := r.DB().QueryRow("SELECT body FROM plugin_demo_items WHERE id = 'a'").Scan(&body); err != nil {
		t.Fatalf("query: %v", err)
	}
	if body != "hello" {
		t.Fatalf("body = %q", body)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	r := newTestRunner(t)
	ms := []sdkstorage.Migration{
		{PluginID: "demo", Name: "001_create", UpSQL: "CREATE TABLE plugin_demo_t (id TEXT)"},
	}
	if applied, errs := r.Apply(ms); applied != 1 || len(errs) > 0 {
		t.Fatalf("first apply: %d %v", applied, errs)
	}
	// second run: already applied → skipped, no error (CREATE TABLE would fail
	// if it actually re-ran)
	if applied, errs := r.Apply(ms); applied != 0 || len(errs) > 0 {
		t.Fatalf("second apply: %d %v", applied, errs)
	}
}

func TestApplyRefusesNamespaceViolation(t *testing.T) {
	r := newTestRunner(t)
	applied, errs := r.Apply([]sdkstorage.Migration{
		{PluginID: "demo", Name: "001_evil", UpSQL: "CREATE TABLE workboard_tasks (id TEXT)"},
	})
	if applied != 0 || len(errs) == 0 {
		t.Fatalf("namespace violation must refuse: applied=%d errs=%v", applied, errs)
	}
	// the violation must not be recorded as applied — fixing the SQL re-runs it
	var n int
	if err := r.DB().QueryRow("SELECT COUNT(*) FROM plugin_schema_migrations").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("refused migration recorded, count = %d", n)
	}
}

func TestApplyRollsBackFailedStep(t *testing.T) {
	r := newTestRunner(t)
	// second statement fails (duplicate table) → whole migration rolls back,
	// including the first statement
	applied, errs := r.Apply([]sdkstorage.Migration{
		{PluginID: "demo", Name: "001_bad", UpSQL: "CREATE TABLE plugin_demo_x (id TEXT); CREATE TABLE plugin_demo_x (id TEXT)"},
	})
	if applied != 0 || len(errs) == 0 {
		t.Fatalf("failed step must report: applied=%d errs=%v", applied, errs)
	}
	var n int
	if err := r.DB().QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = 'plugin_demo_x'").Scan(&n); err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	if n != 0 {
		t.Fatal("failed migration left partial schema behind (no rollback)")
	}
}

func TestApplyOnePluginFailureDoesNotBlockOthers(t *testing.T) {
	r := newTestRunner(t)
	applied, errs := r.Apply([]sdkstorage.Migration{
		{PluginID: "bad", Name: "001", UpSQL: "DROP TABLE token_usage"},
		{PluginID: "good", Name: "001", UpSQL: "CREATE TABLE plugin_good_t (id TEXT)"},
	})
	if applied != 1 {
		t.Fatalf("applied = %d, want 1 (good plugin)", applied)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "bad") {
		t.Fatalf("errs = %v", errs)
	}
}

func TestApplySkipsRemainingStepsOfFailedPlugin(t *testing.T) {
	r := newTestRunner(t)
	// step 002 depends on 001; when 001 fails, 002 must NOT run out of order
	applied, errs := r.Apply([]sdkstorage.Migration{
		{PluginID: "demo", Name: "001_evil", UpSQL: "DROP TABLE rbac_grants"},
		{PluginID: "demo", Name: "002_uses_001", UpSQL: "CREATE TABLE plugin_demo_y (id TEXT)"},
	})
	if applied != 0 {
		t.Fatalf("applied = %d, want 0 (chain stops at first failure)", applied)
	}
	if len(errs) == 0 {
		t.Fatal("expected error for failed first step")
	}
}
