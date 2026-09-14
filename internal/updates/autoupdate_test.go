package updates

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestVerifyInstalledBinaryAcceptsMatchingVersionAndRejectsOthers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "soulacy")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 2.0.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := VerifyInstalledBinary(context.Background(), dir, "v2.0.0"); err != nil {
		t.Fatalf("matching version rejected: %v", err)
	}
	if err := VerifyInstalledBinary(context.Background(), dir, "2.1.0"); err == nil {
		t.Fatal("mismatched version accepted")
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := VerifyInstalledBinary(context.Background(), dir, "2.0.0"); err == nil {
		t.Fatal("crashing binary accepted")
	}
}

func TestRestoreBackupsPutsPreviousBinariesBack(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soulacy")
	backup := dest + ".bak-20260913T000000Z"
	if err := os.WriteFile(dest, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RestoreBackups([]string{backup}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "old" {
		t.Fatalf("restore did not put the old binary back: %q", got)
	}
	if err := RestoreBackups([]string{filepath.Join(dir, "weird")}); err == nil {
		t.Fatal("unrecognised backup name must error")
	}
}

func TestInstallDirWritableProbe(t *testing.T) {
	if !InstallDirWritable(t.TempDir()) {
		t.Fatal("temp dir should be writable")
	}
	if InstallDirWritable(filepath.Join(t.TempDir(), "missing")) {
		t.Fatal("missing dir must not be writable")
	}
}
