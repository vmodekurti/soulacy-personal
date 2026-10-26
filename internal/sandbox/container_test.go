package sandbox

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestContainerProfileDefaultsAreHardened(t *testing.T) {
	args := ContainerProfile{}.RunArgs("img", "python3", "-c", "print(1)")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"run --rm -i", "--network none", "--read-only", "--cap-drop ALL",
		"--security-opt no-new-privileges", "--pids-limit 128",
		"--tmpfs /tmp:rw,noexec,nosuid,size=64m",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("default profile missing %q:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{"--user", "-v ", "-w ", "--memory", "--cpus", "--ulimit", "-e "} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("default profile should not emit %q:\n%s", unwanted, joined)
		}
	}
	if tail := strings.Join(args[len(args)-4:], " "); tail != "img python3 -c print(1)" {
		t.Fatalf("image/argv must come last, got %q", tail)
	}
}

func TestContainerProfileEmitsConfiguredKnobs(t *testing.T) {
	p := ContainerProfile{
		Network: "bridge",
		Limits:  Limits{MemoryMB: 128, CPUSeconds: 1, OpenFiles: 64, FileSizeMB: 8},
		PIDs:    32,
		TmpfsMB: 16,
		User:    "1000:1000",
		Workdir: "/workspace",
		Mounts:  []string{" /host:/workspace:rw ", ""},
		Env:     []string{"HOME=/workspace", "EMPTY=", "MALFORMED", "=nokey"},
	}
	joined := strings.Join(p.RunArgs("img"), " ")
	for _, want := range []string{
		"--network bridge", "--pids-limit 32", "--user 1000:1000", "size=16m",
		"-v /host:/workspace:rw", "-w /workspace", "--memory 128m", "--cpus 1",
		"--ulimit nofile=64:64", "--ulimit fsize=8388608:8388608",
		"-e HOME=/workspace", "-e EMPTY=",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("profile missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "MALFORMED") || strings.Contains(joined, "=nokey") {
		t.Errorf("malformed env entries must be dropped:\n%s", joined)
	}
	if strings.Count(joined, "-v ") != 1 {
		t.Errorf("blank mounts must be skipped:\n%s", joined)
	}
}

func TestContainerUser(t *testing.T) {
	if got := ContainerUser("image"); got != "" {
		t.Errorf(`ContainerUser("image") = %q, want ""`, got)
	}
	if got := ContainerUser(" 1000:1000 "); got != "1000:1000" {
		t.Errorf("explicit user = %q", got)
	}
	want := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	if runtime.GOOS == "windows" {
		want = ""
	}
	for _, setting := range []string{"", "auto", "AUTO"} {
		if got := ContainerUser(setting); got != want {
			t.Errorf("ContainerUser(%q) = %q, want %q", setting, got, want)
		}
	}
}
