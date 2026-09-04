package plugins

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

// writePlugin creates root/<id>/plugin.yaml with the given manifest body.
func writePlugin(t *testing.T, root, id, manifest string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoader_ValidPermissions_Loaded(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "matrix-suite", `
id: matrix-suite
name: Matrix Suite
version: 1.0.0
permissions:
  - cap: channel.send
    channels: [matrix]
  - cap: events.subscribe
    types: ["run.finished"]
`)
	l := New([]string{root}, zap.NewNop())
	if l.Count() != 1 {
		t.Fatalf("Count = %d, want 1", l.Count())
	}
	m := l.All()[0].Manifest
	if len(m.Permissions) != 2 {
		t.Fatalf("Permissions = %d, want 2", len(m.Permissions))
	}
	if m.Permissions[0].Cap != "channel.send" || m.Permissions[0].Channels[0] != "matrix" {
		t.Fatalf("Permissions[0] = %+v", m.Permissions[0])
	}
}

func TestLoader_UnknownCap_PluginSkipped(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "bad-plugin", `
id: bad-plugin
permissions:
  - cap: nuclear.launch
`)
	l := New([]string{root}, zap.NewNop())
	if l.Count() != 0 {
		t.Fatalf("Count = %d, want 0 (invalid permissions must skip the plugin)", l.Count())
	}
}

func TestLoader_WrongScopeKind_PluginSkipped(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "bad-scope", `
id: bad-scope
permissions:
  - cap: channel.send
    agents: [support-bot]
`)
	l := New([]string{root}, zap.NewNop())
	if l.Count() != 0 {
		t.Fatalf("Count = %d, want 0 (wrong scope kind must skip the plugin)", l.Count())
	}
}

func TestLoader_NoPermissions_StillLoads(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "plain", `
id: plain
name: Plain
`)
	l := New([]string{root}, zap.NewNop())
	if l.Count() != 1 {
		t.Fatalf("Count = %d, want 1", l.Count())
	}
	if len(l.All()[0].Manifest.Permissions) != 0 {
		t.Fatal("expected no permissions")
	}
}
