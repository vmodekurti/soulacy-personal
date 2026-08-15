package tenancy

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
)

func testPaths(root string) config.Paths {
	return config.Paths{
		Root: root, Agents: filepath.Join(root, "agents"), Skills: filepath.Join(root, "skills"),
		Plugins: filepath.Join(root, "plugins"), Templates: filepath.Join(root, "templates"),
		Memory: filepath.Join(root, "memory"), Data: filepath.Join(root, "data"),
		Secrets: filepath.Join(root, "secrets"),
	}
}

func TestPlanPersonalMigrationIsReadOnly(t *testing.T) {
	ws := testPaths(t.TempDir())
	if err := os.MkdirAll(ws.Agents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Agents, "SOUL.yaml"), []byte("id: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanPersonalMigration(ws)
	if err != nil {
		t.Fatal(err)
	}
	if plan.AlreadyDone || len(plan.Resources) != 1 || plan.Resources[0].Kind != "agents" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if _, err := os.Stat(plan.DatabasePath); !os.IsNotExist(err) {
		t.Fatalf("plan mutated tenant catalog: %v", err)
	}
}

func TestEnsurePersonalTenantIsStableAndIdempotent(t *testing.T) {
	ws := testPaths(t.TempDir())
	for _, path := range []string{ws.Agents, ws.Memory, ws.Secrets} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	first, plan, err := EnsurePersonalTenant(context.Background(), ws)
	if err != nil {
		t.Fatal(err)
	}
	second, secondPlan, err := EnsurePersonalTenant(context.Background(), ws)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("tenant IDs changed: first=%+v second=%+v", first, second)
	}
	if !plan.AlreadyDone || !secondPlan.AlreadyDone {
		t.Fatalf("migration not marked complete: first=%+v second=%+v", plan, secondPlan)
	}

	db, err := sql.Open("sqlite3", plan.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for table, want := range map[string]int{"organizations": 1, "workspaces": 1, "users": 1, "memberships": 1} {
		var got int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s count = %d, want %d", table, got, want)
		}
	}
	var assignments int
	if err := db.QueryRow(`SELECT COUNT(*) FROM legacy_resource_assignments`).Scan(&assignments); err != nil {
		t.Fatal(err)
	}
	if assignments != len(plan.Resources) {
		t.Fatalf("assignments = %d, want %d", assignments, len(plan.Resources))
	}
}

func TestEnsurePersonalTenantCanceledFreshMigrationLeavesNoCatalog(t *testing.T) {
	ws := testPaths(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, plan, err := EnsurePersonalTenant(ctx, ws)
	if err == nil {
		t.Fatal("expected canceled migration to fail")
	}
	if _, statErr := os.Stat(plan.DatabasePath); !os.IsNotExist(statErr) {
		t.Fatalf("failed migration published a catalog: %v", statErr)
	}
}
