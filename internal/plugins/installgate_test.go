package plugins

// Story E13: the loader honours the install gate — disabled or
// stale-approval plugins do not activate; hand-installed plugins are
// untouched.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/plugininstall"
)

func writeInstallMeta(t *testing.T, dir string, m plugininstall.Meta) {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, plugininstall.MetaFile), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoaderSkipsDisabledInstalledPlugin(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "managed", `
id: managed
`)
	writeInstallMeta(t, filepath.Join(root, "managed"), plugininstall.Meta{
		Source: "test", ApprovedFingerprint: plugininstall.Fingerprint(nil, nil),
		Enabled: false, InstalledAt: time.Now(),
	})
	l := New([]string{root}, zap.NewNop())
	for _, p := range l.All() {
		if p.Manifest.ID == "managed" {
			t.Fatal("disabled installer-managed plugin must not load")
		}
	}
}

func TestLoaderSkipsStaleApproval(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "managed2", `
id: managed2
permissions:
  - cap: vector.search
`)
	writeInstallMeta(t, filepath.Join(root, "managed2"), plugininstall.Meta{
		Source: "test", ApprovedFingerprint: "stale-fingerprint",
		Enabled: true, InstalledAt: time.Now(),
	})
	l := New([]string{root}, zap.NewNop())
	for _, p := range l.All() {
		if p.Manifest.ID == "managed2" {
			t.Fatal("stale-approval plugin must not load until re-approved")
		}
	}
}

func TestLoaderLoadsApprovedInstalledPlugin(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "managed3", `
id: managed3
`)
	writeInstallMeta(t, filepath.Join(root, "managed3"), plugininstall.Meta{
		Source: "test", ApprovedFingerprint: plugininstall.Fingerprint(nil, nil),
		Enabled: true, InstalledAt: time.Now(),
	})
	l := New([]string{root}, zap.NewNop())
	found := false
	for _, p := range l.All() {
		if p.Manifest.ID == "managed3" {
			found = true
		}
	}
	if !found {
		t.Fatal("approved+enabled installer-managed plugin must load")
	}
}
