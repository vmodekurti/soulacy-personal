package dockerutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveHonorsExplicitAbsoluteOverride(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOULACY_DOCKER_BIN", bin)
	got, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if got != bin {
		t.Fatalf("Resolve() = %q, want %q", got, bin)
	}
}

func TestResolveRejectsMissingExplicitOverride(t *testing.T) {
	t.Setenv("SOULACY_DOCKER_BIN", filepath.Join(t.TempDir(), "missing"))
	if _, err := Resolve(); err == nil {
		t.Fatal("Resolve() should reject a missing explicit Docker binary")
	}
}

func TestResolveFindsDockerDesktopOutsideServicePATH(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Docker Desktop fallback is macOS-specific")
	}
	found := false
	for _, candidate := range macOSDockerCandidates {
		if _, err := os.Stat(candidate); err == nil {
			found = true
			break
		}
	}
	if !found {
		t.Skip("Docker Desktop is not installed in a standard location")
	}
	t.Setenv("SOULACY_DOCKER_BIN", "")
	t.Setenv("PATH", "/usr/bin:/bin")
	got, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("Resolve() = %q, want an absolute path", got)
	}
}

func TestEnvironAddsDockerCredentialHelperPath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Docker Desktop helper path is macOS-specific")
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	var got string
	for _, entry := range Environ() {
		if len(entry) > 5 && entry[:5] == "PATH=" {
			got = entry[5:]
			break
		}
	}
	if !pathListContains(got, "/Applications/Docker.app/Contents/Resources/bin") {
		t.Fatalf("PATH %q does not include Docker Desktop credential helpers", got)
	}
}
