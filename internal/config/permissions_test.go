package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSecureWorkspacePermissionsRepairsPermissiveInstall(t *testing.T) {
	root := t.TempDir()
	ws := soulspacePaths(root)
	old := syscallUmask(0)
	defer syscallUmask(old)
	for _, dir := range ws.Dirs() {
		if err := os.MkdirAll(dir, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	secretFile := filepath.Join(ws.Data, "actions.db")
	if err := os.WriteFile(secretFile, []byte("state"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ws.ConfigFile, []byte("server: {}\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := secureWorkspacePermissions(&Config{}, ws); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		path string
		want os.FileMode
	}{
		{ws.Root, 0o700}, {ws.Data, 0o700}, {ws.Secrets, 0o700},
		{secretFile, 0o600}, {ws.ConfigFile, 0o600},
	} {
		info, err := os.Stat(check.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != check.want {
			t.Errorf("%s mode %o, want %o", check.path, got, check.want)
		}
	}
}

func TestRetentionDurationSupportsDefaultsExplicitDeletionAndKeepForever(t *testing.T) {
	if got := RetentionDuration("", 24*time.Hour); got != 24*time.Hour {
		t.Fatalf("default = %s", got)
	}
	if got := RetentionDuration("12h", 24*time.Hour); got != 12*time.Hour {
		t.Fatalf("override = %s", got)
	}
	if got := RetentionDuration("0", 24*time.Hour); got != 0 {
		t.Fatalf("keep forever = %s", got)
	}
}
