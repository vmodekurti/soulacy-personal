//go:build unix

package supportbundle

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWriteFileUsesOwnerOnlyModeUnderPermissiveUmask(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	out := filepath.Join(t.TempDir(), "nested", "support.zip")
	path, _, err := WriteFile(out, Options{Doctor: map[string]any{"ok": true}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("bundle mode %o, want 600", got)
	}
}
