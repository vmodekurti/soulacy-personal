package docker

import (
	"context"
	"strings"
	"testing"
)

func TestNewDefaults(t *testing.T) {
	ex := New("", "", "")
	if ex.image != "python:3.12-slim" {
		t.Fatalf("image = %q", ex.image)
	}
	if ex.pythonBin != "python3" {
		t.Fatalf("pythonBin = %q", ex.pythonBin)
	}
	if ex.network != "none" {
		t.Fatalf("network = %q", ex.network)
	}
}

func TestRunArgumentsAreHardened(t *testing.T) {
	args := strings.Join(dockerRunArgs("none", "worker@sha256:abc", "python3", nil, "print(1)"), " ")
	for _, required := range []string{"--network none", "--read-only", "--user 65532:65532", "--cap-drop ALL", "no-new-privileges", "--pids-limit 128", "/tmp:rw,noexec,nosuid"} {
		if !strings.Contains(args, required) {
			t.Errorf("missing %q in %s", required, args)
		}
	}
}

func TestNewCustom(t *testing.T) {
	ex := New("python:3.11", "python", "bridge")
	if ex.image != "python:3.11" || ex.pythonBin != "python" || ex.network != "bridge" {
		t.Fatalf("custom executor = %#v", ex)
	}
}

func TestSignedImageMustBeDigestPinned(t *testing.T) {
	ex := NewHardened(Config{Image: "registry.example/execution:latest", RequireSignedImage: true})
	if err := ex.verifyImage(context.Background()); err == nil || !strings.Contains(err.Error(), "digest-pinned") {
		t.Fatalf("verify error=%v", err)
	}
}
