package mcp

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestProcessJanitorCleansNewDescendants(t *testing.T) {
	if !supportsProcessJanitor() {
		t.Skipf("process janitor is not supported on %s", runtime.GOOS)
	}

	trigger := t.TempDir() + "/go.fifo"
	if err := exec.Command("mkfifo", trigger).Run(); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	cmd := exec.Command("sh", "-c", `read _ < "$1"; sleep 60 & wait`, "janitor-test", trigger)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start shell: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	j := newProcessJanitor(cmd.Process.Pid, zap.NewNop())
	if j == nil {
		t.Fatal("expected janitor")
	}

	go func() {
		_ = os.WriteFile(trigger, []byte("go\n"), 0o600)
	}()
	waitForDescendant(t, cmd.Process.Pid)

	j.Cleanup(context.Background())

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(descendantPIDs(cmd.Process.Pid)) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected janitor to clean descendants, still found %v", descendantPIDs(cmd.Process.Pid))
}

func waitForDescendant(t *testing.T, rootPID int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(descendantPIDs(rootPID)) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for descendant of %d", rootPID)
}
