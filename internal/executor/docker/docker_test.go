package docker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/sandbox"
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

func TestNewCustom(t *testing.T) {
	ex := New("python:3.11", "python", "bridge")
	if ex.image != "python:3.11" || ex.pythonBin != "python" || ex.network != "bridge" {
		t.Fatalf("custom executor = %#v", ex)
	}
}

func TestRunArgsApplyHardenedProfile(t *testing.T) {
	ex := NewWithOptions(Options{
		Image:  "python:3.12-slim",
		Limits: sandbox.Limits{MemoryMB: 256, CPUSeconds: 10, OpenFiles: 64, FileSizeMB: 8},
		PIDs:   32,
		User:   "1000:1000",
	})
	joined := strings.Join(ex.runArgs("print(1)"), " ")
	for _, want := range []string{
		"--network none", "--read-only", "--cap-drop ALL", "--security-opt no-new-privileges",
		"--pids-limit 32", "--user 1000:1000", "--tmpfs /tmp:rw,noexec,nosuid,size=64m",
		"-w /tmp", "--memory 256m", "--cpus 1", "--ulimit nofile=64:64", "--ulimit fsize=8388608:8388608",
		"-e HOME=/tmp", "-e PYTHONDONTWRITEBYTECODE=1", "python:3.12-slim python3 -c print(1)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("docker args missing %q:\n%s", want, joined)
		}
	}
}

func TestRunDoesNotForwardHostEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	t.Setenv("SOULACY_GATEWAY_SECRET", "must-not-enter-container")
	argsFile := filepath.Join(t.TempDir(), "args")
	fake := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsFile + "\"\necho ok\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := NewWithOptions(Options{Binary: fake}).Run(context.Background(), "", "", "print(1)", nil)
	if err != nil || out != "ok" {
		t.Fatalf("Run = %q, %v", out, err)
	}
	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "must-not-enter-container") {
		t.Fatal("host environment entered the container arguments")
	}
}

func TestRunFailsClosedWithoutDocker(t *testing.T) {
	ex := NewWithOptions(Options{Binary: filepath.Join(t.TempDir(), "missing-docker")})
	if _, err := ex.Run(context.Background(), "", "", "print(1)", nil); err == nil {
		t.Fatal("expected an error when docker is unavailable")
	}
}

// TestRealDockerBoundary exercises the profile against a real daemon. It is
// opt-in (SOULACY_DOCKER_TESTS=1) because it needs Docker and pulls an image.
func TestRealDockerBoundary(t *testing.T) {
	if os.Getenv("SOULACY_DOCKER_TESTS") != "1" {
		t.Skip("set SOULACY_DOCKER_TESTS=1 to run against a real Docker daemon")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not installed")
	}
	t.Setenv("SOULACY_GATEWAY_SECRET", "must-not-enter-container")
	// Run as the gateway user AND as the image's root: for root, only
	// --read-only and --cap-drop stand between the tool and a writable,
	// capable container, so that case is what proves the profile.
	for _, user := range []string{"auto", "image"} {
		t.Run(user, func(t *testing.T) { realDockerProbe(t, sandbox.ContainerUser(user)) })
	}
}

func realDockerProbe(t *testing.T, user string) {
	vol := t.TempDir()
	ex := NewWithOptions(Options{
		Volumes: []string{vol + ":/data:rw"},
		Limits:  sandbox.Limits{MemoryMB: 256, CPUSeconds: 10, OpenFiles: 64, FileSizeMB: 8},
		PIDs:    32,
		User:    user,
	})
	probe := `
def run(args):
    import os, socket
    def can_write(path):
        try:
            with open(path, "w") as f:
                f.write("x")
            return True
        except OSError:
            return False
    try:
        socket.create_connection(("1.1.1.1", 53), timeout=2)
        net = True
    except OSError:
        net = False
    cap_eff = [l.split()[1] for l in open("/proc/self/status") if l.startswith("CapEff")][0]
    return {
        "uid": os.getuid(),
        "root_writable": can_write("/usr/probe"),
        "tmp_writable": can_write("/tmp/probe"),
        "volume_writable": can_write("/data/probe"),
        "network": net,
        "cap_eff": cap_eff,
        "secret_visible": "SOULACY_GATEWAY_SECRET" in os.environ,
        "echo": args.get("echo"),
    }
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := ex.Run(ctx, "", "run", probe, []byte(`{"echo":"hi"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got struct {
		UID            int    `json:"uid"`
		RootWritable   bool   `json:"root_writable"`
		TmpWritable    bool   `json:"tmp_writable"`
		VolumeWritable bool   `json:"volume_writable"`
		Network        bool   `json:"network"`
		CapEff         string `json:"cap_eff"`
		SecretVisible  bool   `json:"secret_visible"`
		Echo           string `json:"echo"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.Echo != "hi" {
		t.Errorf("args not delivered: %+v", got)
	}
	if got.RootWritable || !got.TmpWritable {
		t.Errorf("root must be read-only and /tmp writable: %+v", got)
	}
	if got.Network {
		t.Errorf("container reached the network: %+v", got)
	}
	if strings.Trim(got.CapEff, "0") != "" {
		t.Errorf("container kept capabilities %s", got.CapEff)
	}
	if got.SecretVisible {
		t.Error("gateway environment leaked into the container")
	}
	if user == "" {
		if got.UID != 0 {
			t.Errorf("image user should be root here, got uid %d", got.UID)
		}
		return
	}
	if runtime.GOOS != "windows" {
		if got.UID != os.Getuid() {
			t.Errorf("uid = %d, want the gateway uid %d", got.UID, os.Getuid())
		}
		if !got.VolumeWritable {
			t.Errorf("allowlisted volume should be writable by the gateway user: %+v", got)
		}
	}
}
