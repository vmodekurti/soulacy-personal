package runtime

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/sandbox"
)

func TestDockerPrivilegedRunnerBuildsFailClosedBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	root := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	fake := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsFile + "\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	gatewaySecret := "gateway-secret-must-not-enter-container"
	t.Setenv("SOULACY_GATEWAY_SECRET", gatewaySecret)
	e := &Engine{}
	r := DockerPrivilegedRunner{Workspace: root, Image: "sandbox:test", Binary: fake, PIDs: 32,
		Limits: sandbox.Limits{MemoryMB: 128, CPUSeconds: 1, OpenFiles: 64, FileSizeMB: 8}}
	_, err := r.Run(context.Background(), PrivilegedCommand{
		Argv:       []string{"/bin/sh", "-c", "curl http://169.254.169.254/latest/meta-data"},
		WorkingDir: root,
		Env:        e.shellEnviron(),
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := string(b)
	for _, want := range []string{"--network\nnone", "--read-only", "--cap-drop\nALL", "no-new-privileges", "--pids-limit\n32", "--memory\n128m", "nofile=64:64", "fsize=8388608:8388608", root + ":/workspace:rw"} {
		if !strings.Contains(args, want) {
			t.Errorf("docker args missing %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, gatewaySecret) {
		t.Fatal("gateway secret entered container arguments")
	}
}

func TestDockerIsolationFailureDoesNotFallBackToHost(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "must-not-exist")
	e := &Engine{privilegedRunner: DockerPrivilegedRunner{Workspace: root, Binary: filepath.Join(root, "missing-docker")}}
	_, err := e.runPrivilegedCommand(context.Background(), PrivilegedCommand{Argv: []string{"/bin/sh", "-c", "touch " + marker}, WorkingDir: root}, 1000)
	if err == nil || !strings.Contains(err.Error(), "no host fallback") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("command ran on host after isolation failure")
	}
}

func TestPrivilegedChokepointCoversClassificationAndFailsClosed(t *testing.T) {
	e := &Engine{privilegedRunner: denyPrivilegedRunner{}}
	for name, class := range toolSecurityClasses {
		if !class.Privileged {
			continue
		}
		ran := false
		_, err := e.executePrivilegedBuiltin(context.Background(), name, func(context.Context) (string, error) { ran = true; return "bad", nil })
		if err == nil || ran {
			t.Errorf("%s escaped fail-closed chokepoint", name)
		}
	}
	source, err := os.ReadFile("engine_tool_dispatch.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "e.executePrivilegedBuiltin") {
		t.Fatal("runtime dispatch no longer routes through privileged chokepoint")
	}
}

func TestDockerRunnerHonorsWallClockCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	r := DockerPrivilegedRunner{Workspace: t.TempDir(), Binary: filepath.Join(t.TempDir(), "missing")}
	_, err := r.Run(ctx, PrivilegedCommand{Argv: []string{"sleep", "99"}})
	if err == nil {
		t.Fatal("expected bounded command failure")
	}
}
