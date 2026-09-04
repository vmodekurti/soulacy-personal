package mcp

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

type processJanitor struct {
	rootPID  int
	baseline map[int]bool
	log      *zap.Logger
}

func newProcessJanitor(rootPID int, log *zap.Logger) *processJanitor {
	if rootPID <= 0 || !supportsProcessJanitor() {
		return nil
	}
	return &processJanitor{
		rootPID:  rootPID,
		baseline: descendantPIDs(rootPID),
		log:      log,
	}
}

func (j *processJanitor) Cleanup(ctx context.Context) {
	if j == nil || j.rootPID <= 0 || !supportsProcessJanitor() {
		return
	}
	after := descendantPIDs(j.rootPID)
	var leftovers []int
	for pid := range after {
		if pid != j.rootPID && !j.baseline[pid] {
			leftovers = append(leftovers, pid)
		}
	}
	if len(leftovers) == 0 {
		return
	}

	// Let short-lived tool children exit on their own before intervening.
	select {
	case <-time.After(750 * time.Millisecond):
	case <-ctx.Done():
	}

	afterGrace := descendantPIDs(j.rootPID)
	for _, pid := range leftovers {
		if afterGrace[pid] {
			terminatePID(pid)
		}
	}
	time.Sleep(250 * time.Millisecond)
	afterTerminate := descendantPIDs(j.rootPID)
	killed := 0
	for _, pid := range leftovers {
		if afterTerminate[pid] {
			killPID(pid)
			killed++
		} else {
			killed++
		}
	}
	if killed > 0 && j.log != nil {
		j.log.Info("mcp process janitor cleaned up tool descendants",
			zap.Int("root_pid", j.rootPID),
			zap.Int("processes", killed))
	}
}

func supportsProcessJanitor() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux"
}

func descendantPIDs(rootPID int) map[int]bool {
	out := map[int]bool{}
	var walk func(int)
	walk = func(parent int) {
		for _, child := range childPIDs(parent) {
			if out[child] {
				continue
			}
			out[child] = true
			walk(child)
		}
	}
	walk(rootPID)
	return out
}

func childPIDs(parentPID int) []int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pgrep", "-P", strconv.Itoa(parentPID)).Output()
	if err != nil {
		return nil
	}
	fields := strings.Fields(string(out))
	pids := make([]int, 0, len(fields))
	for _, f := range fields {
		pid, err := strconv.Atoi(strings.TrimSpace(f))
		if err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

func terminatePID(pid int) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "kill", "-TERM", strconv.Itoa(pid)).Run()
}

func killPID(pid int) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "kill", "-KILL", strconv.Itoa(pid)).Run()
}
