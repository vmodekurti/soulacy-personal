package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/wsroot"
)

func TestPurgeWorkspaceRemovesOnlyThatWorkspacesPlugins(t *testing.T) {
	base := t.TempDir()
	platform := t.TempDir()
	stores := NewStores([]string{platform}, base, zap.NewNop())

	write := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("plugin"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	deletedFile := filepath.Join(stores.Dir("ws_delete"), "weather", "plugin.yaml")
	keptFile := filepath.Join(stores.Dir("ws_keep"), "research", "plugin.yaml")
	platformFile := filepath.Join(platform, "catalog", "plugin.yaml")
	write(deletedFile)
	write(keptFile)
	write(platformFile)

	stores.For("ws_delete")
	stores.For("ws_keep")
	removed, err := stores.PurgeWorkspace(context.Background(), "ws_delete")
	if err != nil {
		t.Fatal(err)
	}
	if removed.Rows != 1 || removed.Bytes != int64(len("plugin")) {
		t.Fatalf("removed = %+v, want one plugin file and %d bytes", removed, len("plugin"))
	}
	if _, err := os.Stat(stores.Dir("ws_delete")); !os.IsNotExist(err) {
		t.Fatalf("deleted workspace plugin tree still exists: %v", err)
	}
	for _, path := range []string{keptFile, platformFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("purge touched unrelated plugin file %s: %v", path, err)
		}
	}
}

func TestPurgeWorkspaceRefusesPersonalPluginRoot(t *testing.T) {
	stores := NewStores(nil, t.TempDir(), zap.NewNop())
	personalFile := filepath.Join(stores.Dir(wsroot.PersonalWorkspaceID), "keep", "plugin.yaml")
	if err := os.MkdirAll(filepath.Dir(personalFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(personalFile, []byte("plugin"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := stores.PurgeWorkspace(context.Background(), wsroot.PersonalWorkspaceID); err == nil {
		t.Fatal("personal plugin root purge succeeded")
	}
	if _, err := os.Stat(personalFile); err != nil {
		t.Fatalf("personal plugin file was removed: %v", err)
	}
}
