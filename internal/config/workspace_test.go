package config

// Workspace resolution ("soulspace"): fresh installs live in the organized
// ~/.soulacy/soulspace workspace; pre-soulspace installations keep their
// flat ~/.soulacy layout untouched until the operator runs
// `sy workspace migrate`.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorkspace_FreshInstallUsesSoulspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOULACY_WORKSPACE", "")

	ws, err := ResolveWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if ws.Legacy {
		t.Error("fresh install must not be legacy")
	}
	want := filepath.Join(home, ".soulacy", "soulspace")
	if ws.Root != want {
		t.Errorf("Root = %q, want %q", ws.Root, want)
	}
	// Organized layout: databases under data/, config at the root.
	if ws.DB("actions") != filepath.Join(want, "data", "actions.db") {
		t.Errorf("DB(actions) = %q", ws.DB("actions"))
	}
	if ws.Agents != filepath.Join(want, "agents") || ws.Data != filepath.Join(want, "data") {
		t.Errorf("layout: %+v", ws)
	}
	if ws.ConfigFile != filepath.Join(want, "config.yaml") {
		t.Errorf("ConfigFile = %q", ws.ConfigFile)
	}
	if ws.CredentialsDB() != filepath.Join(want, "secrets", "credentials.db") {
		t.Errorf("CredentialsDB = %q", ws.CredentialsDB())
	}
}

func TestResolveWorkspace_LegacyInstallationDetected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOULACY_WORKSPACE", "")
	legacy := filepath.Join(home, ".soulacy")
	if err := os.MkdirAll(filepath.Join(legacy, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.yaml"), []byte("server:\n  port: 18789\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ws, err := ResolveWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if !ws.Legacy {
		t.Fatal("existing flat ~/.soulacy must resolve as legacy")
	}
	if ws.Root != legacy {
		t.Errorf("Root = %q, want %q", ws.Root, legacy)
	}
	// Legacy paths are the EXACT pre-soulspace locations — zero disruption.
	if ws.DB("actions") != filepath.Join(legacy, "actions.db") {
		t.Errorf("legacy DB(actions) = %q", ws.DB("actions"))
	}
	if ws.Agents != filepath.Join(legacy, "agents") || ws.Data != legacy {
		t.Errorf("legacy layout: %+v", ws)
	}
	if ws.CredentialsDB() != filepath.Join(legacy, "credentials.db") {
		t.Errorf("legacy CredentialsDB = %q", ws.CredentialsDB())
	}
}

func TestResolveWorkspace_SoulspaceWinsOverLegacyOnceCreated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOULACY_WORKSPACE", "")
	// Both exist (post-migration): soulspace wins.
	if err := os.MkdirAll(filepath.Join(home, ".soulacy", "soulspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".soulacy", "leftover.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws, err := ResolveWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if ws.Legacy || ws.Root != filepath.Join(home, ".soulacy", "soulspace") {
		t.Errorf("ws = %+v, want soulspace", ws)
	}
}

func TestResolveWorkspace_EnvOverride(t *testing.T) {
	home := t.TempDir()
	custom := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOULACY_WORKSPACE", custom)

	ws, err := ResolveWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if ws.Legacy || ws.Root != custom {
		t.Errorf("ws = %+v, want env-pinned %q", ws, custom)
	}
}

func TestResolveWorkspace_PointerSelectsCustomLocation(t *testing.T) {
	home := t.TempDir()
	custom := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOULACY_WORKSPACE", "")

	if err := SaveWorkspacePointer(home, custom); err != nil {
		t.Fatal(err)
	}
	ws, err := ResolveWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if ws.Legacy || ws.Root != custom {
		t.Errorf("ws = %+v, want pointer-selected %q", ws, custom)
	}
	if ws.ConfigFile != filepath.Join(custom, "config.yaml") {
		t.Errorf("ConfigFile = %q, want %q", ws.ConfigFile, filepath.Join(custom, "config.yaml"))
	}

	// Clearing the pointer reverts to the canonical soulspace default.
	if err := ClearWorkspacePointer(home); err != nil {
		t.Fatal(err)
	}
	ws2, err := ResolveWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if ws2.Root != filepath.Join(home, ".soulacy", "soulspace") {
		t.Errorf("after clear, Root = %q, want default soulspace", ws2.Root)
	}
}

func TestResolveWorkspace_StalePointerIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOULACY_WORKSPACE", "")

	// A pointer naming a non-existent directory must be ignored (fall back to
	// the default), not silently create a fresh empty workspace at a dead path.
	dotDir := filepath.Join(home, ".soulacy")
	if err := os.MkdirAll(dotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dotDir, "workspace"), []byte("/no/such/dir\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws, err := ResolveWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if ws.Root != filepath.Join(dotDir, "soulspace") {
		t.Errorf("stale pointer should fall back to soulspace, got %q", ws.Root)
	}
}

func TestResolveWorkspace_EnvOverridesPointer(t *testing.T) {
	home := t.TempDir()
	pointed := t.TempDir()
	envDir := t.TempDir()
	t.Setenv("HOME", home)
	if err := SaveWorkspacePointer(home, pointed); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOULACY_WORKSPACE", envDir)

	ws, err := ResolveWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if ws.Root != envDir {
		t.Errorf("env var must win over pointer: Root = %q, want %q", ws.Root, envDir)
	}
}

func TestLoad_FreshInstallDefaultsLandInSoulspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOULACY_WORKSPACE", "")
	cwd, _ := os.Getwd()
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil { // avoid picking up a repo-level config.yaml
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	cfg, resolvedPath, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".soulacy", "soulspace")
	if got := cfg.AgentDirs[0]; got != filepath.Join(root, "agents") {
		t.Errorf("agent_dirs default = %q", got)
	}
	if cfg.Memory.SQLitePath != filepath.Join(root, "data", "archive.db") {
		t.Errorf("memory.sqlite_path default = %q", cfg.Memory.SQLitePath)
	}
	if cfg.Knowledge.DBPath != filepath.Join(root, "data", "knowledge.db") {
		t.Errorf("knowledge.db_path default = %q", cfg.Knowledge.DBPath)
	}
	if resolvedPath != filepath.Join(root, "config.yaml") {
		t.Errorf("resolvedPath = %q", resolvedPath)
	}

	// EnsureDirs builds the organized layout.
	if err := EnsureDirs(cfg); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"agents", "skills", "plugins", "templates", "memory", "data", "logs", "audit", "secrets", "tools"} {
		if st, err := os.Stat(filepath.Join(root, d)); err != nil || !st.IsDir() {
			t.Errorf("EnsureDirs missing %s: %v", d, err)
		}
	}
}

func TestLoad_LegacyInstallKeepsOldDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOULACY_WORKSPACE", "")
	legacy := filepath.Join(home, ".soulacy")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.yaml"), []byte("server:\n  port: 18789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	cfg, resolvedPath, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Memory.SQLitePath != filepath.Join(legacy, "archive.db") {
		t.Errorf("legacy memory.sqlite_path = %q", cfg.Memory.SQLitePath)
	}
	if resolvedPath != filepath.Join(legacy, "config.yaml") {
		t.Errorf("legacy resolvedPath = %q", resolvedPath)
	}
}
