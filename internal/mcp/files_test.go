package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestFileVaultNamespace(t *testing.T) {
	if got := FileVaultNamespace("notebooklm"); got != "mcp_file_notebooklm" {
		t.Fatalf("namespace = %q", got)
	}
}

func TestMaterializeServerFilesWritesWithSecurePerms(t *testing.T) {
	base := t.TempDir()
	dir, err := materializeServerFiles(base, "NoteBook LM", map[string][]byte{
		"cookies.txt": []byte("session=abc"),
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	// Server id is sanitized for the directory name (spaces → underscore).
	if filepath.Base(dir) != "notebook_lm" {
		t.Errorf("dir base = %q, want sanitized id", filepath.Base(dir))
	}
	data, err := os.ReadFile(filepath.Join(dir, "cookies.txt"))
	if err != nil || string(data) != "session=abc" {
		t.Fatalf("file content = %q, err %v", data, err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "cookies.txt")); fi != nil && fi.Mode().Perm() != 0o600 {
		t.Errorf("file perm = %v, want 0600", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(dir); fi != nil && fi.Mode().Perm() != 0o700 {
		t.Errorf("dir perm = %v, want 0700", fi.Mode().Perm())
	}
}

func TestMaterializeServerFilesRemovesStale(t *testing.T) {
	base := t.TempDir()
	if _, err := materializeServerFiles(base, "srv", map[string][]byte{"a.txt": []byte("1"), "b.txt": []byte("2")}); err != nil {
		t.Fatal(err)
	}
	// Re-materialize with only a.txt — b.txt must be swept.
	dir, err := materializeServerFiles(base, "srv", map[string][]byte{"a.txt": []byte("1")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); !os.IsNotExist(err) {
		t.Errorf("stale b.txt should have been removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err != nil {
		t.Errorf("a.txt should remain: %v", err)
	}
}

func TestMaterializeServerFilesNoFilesIsNoop(t *testing.T) {
	dir, err := materializeServerFiles(t.TempDir(), "srv", nil)
	if err != nil || dir != "" {
		t.Fatalf("empty set should return empty dir, got %q err %v", dir, err)
	}
}

func TestSafeFileBaseRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"../escape", "a/b", ".", "..", "/etc/passwd"} {
		if got := safeFileBase(bad); got != "" {
			t.Errorf("safeFileBase(%q) = %q, want rejected", bad, got)
		}
	}
	if got := safeFileBase("cookies.txt"); got != "cookies.txt" {
		t.Errorf("safeFileBase(cookies.txt) = %q", got)
	}
}

func TestProvisionFilesInjectsDir(t *testing.T) {
	base := t.TempDir()
	c := &Client{
		log:     zap.NewNop(),
		fileDir: base,
		materializeFiles: func(_ context.Context, id string) (map[string][]byte, error) {
			return map[string][]byte{"cookies.txt": []byte("v")}, nil
		},
	}
	dir, err := c.provisionFiles("srv")
	if err != nil {
		t.Fatalf("provisionFiles: %v", err)
	}
	if dir == "" {
		t.Fatal("expected a non-empty dir when files are present")
	}
	if _, err := os.Stat(filepath.Join(dir, "cookies.txt")); err != nil {
		t.Errorf("materialized file missing: %v", err)
	}

	// No materializer → no-op, no dir.
	empty := &Client{log: zap.NewNop(), fileDir: base}
	if dir, err := empty.provisionFiles("srv"); err != nil || dir != "" {
		t.Errorf("no materializer should be a no-op, got %q err %v", dir, err)
	}
}
