package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesystemToolsDenyOutsideConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "config.yaml")
	if err := os.WriteFile(secret, []byte("api_key: should-not-leak"), 0600); err != nil {
		t.Fatal(err)
	}
	e := newMinimalEngine(t)
	if err := e.SetFilesystemRoots([]string{root}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args map[string]any
	}{
		{"read_file", map[string]any{"path": secret}},
		{"list_dir", map[string]any{"path": outside}},
		{"find_files", map[string]any{"path": outside}},
		{"write_file", map[string]any{"path": filepath.Join(outside, "written.txt"), "content": "no"}},
		{"download_file", map[string]any{"url": "https://example.com/file", "dest_path": filepath.Join(outside, "download")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := systemTool(t, e, tc.name)
			_, err := tool.Handler(context.Background(), tc.args)
			if err == nil || !strings.Contains(err.Error(), "outside configured workspace roots") {
				t.Fatalf("%s did not route through filesystem policy: %v", tc.name, err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(outside, "written.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside write occurred: %v", err)
	}
}

func TestFilesystemToolsDenySymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	e := newMinimalEngine(t)
	if err := e.SetFilesystemRoots([]string{root}); err != nil {
		t.Fatal(err)
	}

	read := systemTool(t, e, "read_file")
	if _, err := read.Handler(context.Background(), map[string]any{"path": filepath.Join(link, "secret.txt")}); err == nil {
		t.Fatal("read_file followed a symlink outside the workspace")
	}
	write := systemTool(t, e, "write_file")
	if _, err := write.Handler(context.Background(), map[string]any{
		"path": filepath.Join(link, "new.txt"), "content": "escape",
	}); err == nil {
		t.Fatal("write_file followed a symlinked parent outside the workspace")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("symlink escape created outside file: %v", err)
	}
}

func TestFilesystemRelativePathsResolveFromWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	e := newMinimalEngine(t)
	if err := e.SetFilesystemRoots([]string{root}); err != nil {
		t.Fatal(err)
	}
	out, err := systemTool(t, e, "read_file").Handler(context.Background(), map[string]any{"path": "inside.txt"})
	if err != nil || out != "ok" {
		t.Fatalf("relative workspace read = %q, %v", out, err)
	}
}
