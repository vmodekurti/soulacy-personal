package binpath

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFindUsesPATHFirst(t *testing.T) {
	want := filepath.Join(t.TempDir(), "sy")
	got, err := find("sy", "/ignored/soulacy", "darwin", func(string) (string, error) {
		return want, nil
	})
	if err != nil || got != want {
		t.Fatalf("find() = %q, %v; want %q", got, err, want)
	}
}

func TestFindUsesExecutableSiblingWhenPATHIsMinimal(t *testing.T) {
	dir := t.TempDir()
	sy := filepath.Join(dir, "sy")
	if err := os.WriteFile(sy, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := find("sy", filepath.Join(dir, "soulacy"), "darwin", func(string) (string, error) {
		return "", errors.New("not found")
	})
	if err != nil || got != sy {
		t.Fatalf("find() = %q, %v; want %q", got, err, sy)
	}
}

func TestFindUsesWindowsExecutableSuffix(t *testing.T) {
	dir := t.TempDir()
	sy := filepath.Join(dir, "sy.exe")
	if err := os.WriteFile(sy, []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := find("sy", filepath.Join(dir, "soulacy.exe"), "windows", func(string) (string, error) {
		return "", errors.New("not found")
	})
	if err != nil || got != sy {
		t.Fatalf("find() = %q, %v; want %q", got, err, sy)
	}
}
