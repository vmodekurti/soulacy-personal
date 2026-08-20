// invalidate_test.go — a revoked plugin stops being callable.
//
// The store cached each workspace's loader on first use and never dropped it,
// so removing a plugin changed the files on disk and nothing else. The API
// answered with a note telling the operator to restart, which is not a
// revocation: an extension somebody has just decided to stop trusting keeps
// running for the life of the process.
//
// The tests move real directories, because the property is "the next scan sees
// what is on disk" and a fake loader would assert the cache behaviour without
// asserting that a rescan is what fills it.
package plugins

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

// writeSimplePlugin reuses the package's existing writePlugin helper with a
// minimal valid manifest — the tests here are about the cache, not about
// manifest parsing, and inventing a second writer would give the two a chance
// to disagree about what a loadable plugin looks like.
func writeSimplePlugin(t *testing.T, dir, id string) {
	t.Helper()
	writePlugin(t, dir, id, "id: "+id+"\nname: "+id+"\nversion: 1.0.0\n")
}

func TestARevokedPluginStopsLoadingWithoutARestart(t *testing.T) {
	base := t.TempDir()
	stores := NewStores(nil, base, zap.NewNop())

	dir, err := stores.EnsureDir("ws-a")
	if err != nil {
		t.Fatal(err)
	}
	writeSimplePlugin(t, dir, "acme")

	if got := stores.For("ws-a").Count(); got != 1 {
		t.Fatalf("plugin not loaded (%d), so this test proves nothing", got)
	}
	// Removal on disk, exactly as Installer.Remove does it.
	if err := os.RemoveAll(filepath.Join(dir, "acme")); err != nil {
		t.Fatal(err)
	}
	// Without invalidation this still reports 1 — the cached loader outlives
	// the files it describes.
	stores.Invalidate("ws-a")
	if got := stores.For("ws-a").Count(); got != 0 {
		t.Errorf("a removed plugin is still loaded (%d) — it stays callable until the gateway restarts", got)
	}
}

func TestInvalidatingOneWorkspaceDoesNotRescanAnother(t *testing.T) {
	base := t.TempDir()
	stores := NewStores(nil, base, zap.NewNop())

	for _, ws := range []string{"ws-a", "ws-b"} {
		dir, err := stores.EnsureDir(ws)
		if err != nil {
			t.Fatal(err)
		}
		writeSimplePlugin(t, dir, "shared-name")
	}
	before := stores.For("ws-b")
	stores.For("ws-a")

	stores.Invalidate("ws-a")

	// The identity check is the assertion: invalidating by workspace rather
	// than clearing the map is what keeps one tenant's install from forcing
	// every other tenant to rescan — and, worse, from being the thing that
	// makes a half-written plugin directory visible to them mid-copy.
	if stores.For("ws-b") != before {
		t.Error("invalidating ws-a dropped ws-b's loader too")
	}
	if got := stores.For("ws-b").Count(); got != 1 {
		t.Errorf("ws-b lost its plugin: %d", got)
	}
}

func TestAFreshlyInstalledPluginIsCallableImmediately(t *testing.T) {
	// The same mechanism in the other direction, and the one an operator
	// notices: approving a plugin and finding it does nothing reads as the
	// install having failed.
	base := t.TempDir()
	stores := NewStores(nil, base, zap.NewNop())
	if got := stores.For("ws-a").Count(); got != 0 {
		t.Fatalf("expected an empty workspace, got %d", got)
	}
	dir, err := stores.EnsureDir("ws-a")
	if err != nil {
		t.Fatal(err)
	}
	writeSimplePlugin(t, dir, "acme")

	stores.Invalidate("ws-a")
	if got := stores.For("ws-a").Count(); got != 1 {
		t.Errorf("a just-approved plugin is not loaded (%d)", got)
	}
}
