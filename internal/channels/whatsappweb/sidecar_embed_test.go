package whatsappweb

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureSidecarScript(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "whatsapp-web")
	path, err := EnsureSidecarScript(dir)
	if err != nil {
		t.Fatalf("EnsureSidecarScript: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("path not absolute: %q", path)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, sidecarScript) {
		t.Fatalf("written script differs from embedded (err=%v)", err)
	}

	// Stale content (an older shipped script) is refreshed on next ensure.
	if err := os.WriteFile(path, []byte("// old version"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureSidecarScript(dir); err != nil {
		t.Fatalf("re-ensure: %v", err)
	}
	got, _ = os.ReadFile(path)
	if !bytes.Equal(got, sidecarScript) {
		t.Error("stale script was not refreshed")
	}
}

// The embedded script and the documented scripts/ copy must never drift.
func TestEmbeddedScriptMatchesScriptsCopy(t *testing.T) {
	repoCopy, err := os.ReadFile(filepath.FromSlash("../../../scripts/whatsapp-web-sidecar.mjs"))
	if err != nil {
		t.Skipf("scripts copy not readable: %v", err)
	}
	if !bytes.Equal(repoCopy, sidecarScript) {
		t.Fatal("scripts/whatsapp-web-sidecar.mjs differs from the embedded copy — sync them (cp scripts/whatsapp-web-sidecar.mjs internal/channels/whatsappweb/)")
	}
}

func TestBaileysInstalledDetection(t *testing.T) {
	dir := t.TempDir()
	if BaileysInstalled(dir) {
		t.Fatal("empty dir must not report installed")
	}
	pkg := filepath.Join(dir, "node_modules", "@whiskeysockets", "baileys")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !BaileysInstalled(dir) {
		t.Fatal("installed package not detected")
	}
	// EnsureBaileys must short-circuit without touching npm.
	if err := EnsureBaileys(t.Context(), dir); err != nil {
		t.Fatalf("EnsureBaileys short-circuit: %v", err)
	}
}
