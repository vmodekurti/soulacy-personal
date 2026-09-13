//go:build linux || darwin

package publishedfiles

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func fixtureFile(t *testing.T, root, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPublishedListAndRead(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "reports"), 0700); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, root, "reports/Hello 🌎 #?%.md", []byte("# Published\nこんにちは"))
	fixtureFile(t, root, "z.txt", []byte("last"))
	fixtureFile(t, root, ".env", []byte("secret"))
	fixtureFile(t, root, "image.png", []byte("binary"))
	list, err := List(root, "")
	if err != nil || !list.ReadOnly || list.Truncated || len(list.Entries) != 3 || list.Entries[0].Kind != "directory" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	preview, err := Read(root, "reports/Hello 🌎 #?%.md")
	if err != nil || !preview.ReadOnly || preview.Content != "# Published\nこんにちは" || len(preview.SHA256) != 64 || preview.SizeBytes != int64(len(preview.Content)) {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	if _, err := Read(root, "image.png"); !errors.Is(err, ErrPreview) {
		t.Fatalf("binary extension: %v", err)
	}
	if _, err := Read(root, "missing.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := List(root, "z.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("file as folder: %v", err)
	}
}

func TestPublishedPathAndBounds(t *testing.T) {
	for _, value := range []string{"/etc/passwd", "../secret", "reports/../secret", ".env", "a/.b", "a//b", "a/", "a\\b", "a\x00.txt", "a\u202e.txt", strings.Repeat("x", 256), strings.Repeat("x/", 25) + "x"} {
		if ValidPath(value) {
			t.Errorf("accepted %q", value)
		}
	}
	for _, value := range []string{"", "a", "reports/a b#?%.md", "%2e%2e.txt", "中文.md"} {
		if !ValidPath(value) {
			t.Errorf("rejected %q", value)
		}
	}
	for _, value := range []string{"/", "relative", "/tmp/../etc", "/tmp/"} {
		if ValidRoot(value) {
			t.Errorf("accepted root %q", value)
		}
	}
	root := t.TempDir()
	fixtureFile(t, root, "limit.txt", []byte(strings.Repeat("a", MaxPreviewBytes)))
	if _, err := Read(root, "limit.txt"); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"large.txt": []byte(strings.Repeat("a", MaxPreviewBytes+1)), "utf8.txt": {0xff}, "nul.txt": {'a', 0, 'b'}} {
		fixtureFile(t, root, name, data)
		if _, err := Read(root, name); !errors.Is(err, ErrPreview) {
			t.Fatalf("%s = %v", name, err)
		}
	}
	for i := 0; i < MaxEntries+2; i++ {
		fixtureFile(t, root, fmt.Sprintf("%04d.txt", i), []byte("a"))
	}
	list, err := List(root, "")
	if err != nil || len(list.Entries) > MaxEntries || !list.Truncated {
		t.Fatalf("unbounded directory: %+v %v", list, err)
	}
}

func TestPublishedRejectsSymlinksHardLinksAndSpecialFiles(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	fixtureFile(t, outside, "secret.txt", []byte("secret"))
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "symlink.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(outside, "secret.txt"), filepath.Join(root, "hardlink.txt")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "pipe.txt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("loop", filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"escape/secret.txt", "symlink.txt", "hardlink.txt", "pipe.txt", "loop/secret.txt"} {
		if _, err := Read(root, name); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s = %v", name, err)
		}
	}
	list, err := List(root, "")
	if err != nil || len(list.Entries) != 0 {
		t.Fatalf("leaked entries: %+v %v", list, err)
	}
	if _, err := List(filepath.Join(root, "escape"), ""); !errors.Is(err, ErrUnavailable) {
		t.Fatal("root symlink accepted", err)
	}
}

func TestPublishedConcurrentSymlinkSwapsNeverEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	fixtureFile(t, outside, "secret.txt", []byte("OUTSIDE SECRET"))
	if err := os.Mkdir(filepath.Join(root, "flip"), 0700); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, root, "flip/secret.txt", []byte("published"))
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; i < 200; i++ {
			_ = os.Rename(filepath.Join(root, "flip"), filepath.Join(root, "holding"))
			_ = os.Symlink(outside, filepath.Join(root, "flip"))
			_ = os.Remove(filepath.Join(root, "flip"))
			_ = os.Rename(filepath.Join(root, "holding"), filepath.Join(root, "flip"))
		}
	})
	for i := 0; i < 200; i++ {
		preview, err := Read(root, "flip/secret.txt")
		if err == nil && preview.Content != "published" {
			t.Errorf("escaped: %+v", preview)
		}
	}
	wg.Wait()
}
