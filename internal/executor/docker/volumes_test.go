package docker

import (
	"strings"
	"testing"
)

func TestNewWithVolumes_TrimsAndStores(t *testing.T) {
	e := NewWithVolumes("img", "python3", "bridge", []string{" /data:/data:ro ", "", "  "})
	if len(e.volumes) != 1 || e.volumes[0] != "/data:/data:ro" {
		t.Fatalf("volumes = %#v, want one cleaned entry", e.volumes)
	}
}

func TestDockerRunArgs_IncludesVolumeMounts(t *testing.T) {
	args := dockerRunArgs("none", "python:3.12-slim", "python3", []string{"/host:/c:ro", "/a:/b"}, "print(1)")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-v /host:/c:ro") || !strings.Contains(joined, "-v /a:/b") {
		t.Fatalf("expected both volume mounts, got: %s", joined)
	}
	// network flag precedes image; image precedes the python invocation.
	if !strings.Contains(joined, "--network none") {
		t.Fatalf("missing network flag: %s", joined)
	}
	if args[len(args)-3] != "python3" || args[len(args)-2] != "-c" || args[len(args)-1] != "print(1)" {
		t.Fatalf("python invocation malformed: %#v", args[len(args)-3:])
	}
}

func TestDockerRunArgs_NoVolumesIsClean(t *testing.T) {
	args := dockerRunArgs("none", "img", "python3", nil, "x")
	for _, a := range args {
		if a == "-v" {
			t.Fatalf("did not expect any -v flag: %#v", args)
		}
	}
}
