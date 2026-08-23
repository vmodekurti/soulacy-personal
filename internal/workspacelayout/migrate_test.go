package workspacelayout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateBuildsOneWorkspaceTree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "agents", ".workspaces", "ws_alpha", "agent.yaml"), "agent")
	writeFile(t, filepath.Join(root, "logs", ".workspaces", "ws_alpha", "agent.log"), "log")
	writeFile(t, filepath.Join(root, "agents", ".workspaces", "ws_beta", "other.yaml"), "other")
	writeFile(t, filepath.Join(root, "agents", "personal.yaml"), "personal")

	report, err := Migrate(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Moves) != 3 {
		t.Fatalf("moves = %d, want 3", len(report.Moves))
	}
	assertFile(t, filepath.Join(root, "workspaces", "ws_alpha", "agents", "agent.yaml"), "agent")
	assertFile(t, filepath.Join(root, "workspaces", "ws_alpha", "logs", "agent.log"), "log")
	assertFile(t, filepath.Join(root, "workspaces", "ws_beta", "agents", "other.yaml"), "other")
	assertFile(t, filepath.Join(root, "agents", "personal.yaml"), "personal")

	retry, err := Migrate(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(retry.Moves) != 0 {
		t.Fatalf("idempotent retry planned %d moves", len(retry.Moves))
	}
}

func TestMigratePreflightsEveryDestinationBeforeMoving(t *testing.T) {
	root := t.TempDir()
	legacyA := filepath.Join(root, "agents", ".workspaces", "ws_alpha", "agent.yaml")
	legacyB := filepath.Join(root, "logs", ".workspaces", "ws_alpha", "agent.log")
	writeFile(t, legacyA, "agent")
	writeFile(t, legacyB, "log")
	writeFile(t, filepath.Join(root, "workspaces", "ws_alpha", "logs", "existing.log"), "existing")

	_, err := Migrate(root)
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("Migrate error = %v, want conflict", err)
	}
	assertFile(t, legacyA, "agent")
	assertFile(t, legacyB, "log")
}

func TestMigrateRejectsSymlinkedWorkspaceNamespace(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "agents", ".workspaces")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Plan(root); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Plan error = %v, want symlink rejection", err)
	}
}

func TestPlanLeavesRootWorkspaceNamespaceUntouched(t *testing.T) {
	root := t.TempDir()
	rootNamespaceFile := filepath.Join(root, ".workspaces", "ws_alpha", "state.json")
	writeFile(t, rootNamespaceFile, "root state")
	writeFile(t, filepath.Join(root, "agents", ".workspaces", "ws_alpha", "agent.yaml"), "agent")

	moves, err := Plan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 1 {
		t.Fatalf("moves = %d, want only the subsystem-first workspace", len(moves))
	}
	if moves[0].From != filepath.Join(root, "agents", ".workspaces", "ws_alpha") {
		t.Fatalf("move source = %q, want agents legacy namespace", moves[0].From)
	}
	assertFile(t, rootNamespaceFile, "root state")
}

func writeFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
