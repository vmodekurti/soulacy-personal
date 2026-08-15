// isolation_test.go — the cross-tenant isolation contract for plugin inventory
// (MU-017 criterion 1). internal/ownership/catalog.go names this file as the
// isolation evidence for internal/plugins/loader.go.
//
// A plugin contributes tools agents can call. A single process-wide loader
// means every workspace's agents can invoke every workspace's plugins, which
// is not a disclosure bug but a capability one: third-party code installed by
// one tenant becomes callable by all of them, which is the exact outcome
// MU-017's user story is written against.
package plugins

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/wsroot"
)

func writeIsolationPlugin(t *testing.T, dir, id string) {
	t.Helper()
	writePlugin(t, dir, id, "id: "+id+"\n")
}

func newPluginStores(t *testing.T) (*Stores, string, string) {
	t.Helper()
	root := t.TempDir()
	platform := filepath.Join(root, "platform")
	base := filepath.Join(root, "plugins")
	for _, dir := range []string{platform, base} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return NewStores([]string{platform}, base, zap.NewNop()), platform, base
}

func pluginNames(loader *Loader) map[string]bool {
	out := map[string]bool{}
	if loader == nil {
		return out
	}
	for _, p := range loader.All() {
		if p != nil && p.Manifest.ID != "" {
			out[p.Manifest.ID] = true
		}
	}
	return out
}

// A plugin installed by one workspace is not loadable by another, so its tools
// are not callable by another tenant's agents.
func TestAPluginInstalledInOneWorkspaceIsNotLoadedByAnother(t *testing.T) {
	stores, platform, _ := newPluginStores(t)
	writeIsolationPlugin(t, platform, "shared_tools")

	dirA, err := stores.EnsureDir("ws_a")
	if err != nil {
		t.Fatal(err)
	}
	writeIsolationPlugin(t, dirA, "alpha_tools")

	a := pluginNames(stores.For("ws_a"))
	b := pluginNames(stores.For("ws_b"))

	if !a["alpha_tools"] {
		t.Fatalf("a workspace cannot load its own plugin: %v", a)
	}
	if b["alpha_tools"] {
		t.Fatalf("one workspace's plugin was loaded by another: %v", b)
	}
	if !a["shared_tools"] || !b["shared_tools"] {
		t.Fatalf("the platform plugin was not available as a template: a=%v b=%v", a, b)
	}
}

// Product invariant 7: personal resolves to the base directory itself.
func TestPersonalPluginsStayAtTheBaseDirectory(t *testing.T) {
	stores, _, base := newPluginStores(t)
	writeIsolationPlugin(t, base, "local_tools")

	if got := stores.Dir(""); got != base {
		t.Fatalf("personal plugin directory moved to %q, want %q", got, base)
	}
	if got := stores.Dir(wsroot.PersonalWorkspaceID); got != base {
		t.Fatalf("explicit personal resolved to %q", got)
	}
	if !pluginNames(stores.For(""))["local_tools"] {
		t.Fatal("personal lost sight of its own plugin")
	}
	if pluginNames(stores.For("ws_a"))["local_tools"] {
		t.Fatal("a tenant could load the personal workspace's plugin")
	}
}

// The workspace directory is scanned last so a tenant can shadow a platform
// plugin without modifying the shared copy.
func TestThePluginWorkspaceDirectoryIsScannedLast(t *testing.T) {
	stores, platform, _ := newPluginStores(t)
	dirs := stores.ScanDirs("ws_a")
	if len(dirs) != 2 || dirs[0] != platform || dirs[1] != stores.Dir("ws_a") {
		t.Fatalf("scan list = %v, want [platform, workspace]", dirs)
	}
}
