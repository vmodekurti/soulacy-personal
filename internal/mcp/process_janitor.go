package mcp

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
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

// ---------------------------------------------------------------------------
// Stopping a server
// ---------------------------------------------------------------------------

// snapshotDescendants lists the processes a server has spawned.
//
// It must be called BEFORE the server's transport is closed. Once the server
// process is gone its children are reparented to init, and there is no longer
// any way to tell which processes were its — the browser that outlived it
// looks exactly like a browser somebody else started.
func snapshotDescendants(rootPID int) []int {
	if rootPID <= 0 || !supportsProcessJanitor() {
		return nil
	}
	var out []int
	for pid := range descendantPIDs(rootPID) {
		if pid != rootPID {
			out = append(out, pid)
		}
	}
	return out
}

// reapStopped kills whatever a stopped server left running.
//
// keeps_processes exempts a server from the per-call janitor because its child
// process IS its state — a browser between two tool calls. That argument ends
// when the server does: nothing owns those processes any more, and nothing
// will ever talk to them again.
//
// Leaving them costs more than memory. Playwright names its runtime directory
// deterministically, so an orphaned browser keeps the socket the next server
// wants, and the replacement attaches to something already dying: "Target
// page, context or browser has been closed", on a fresh gateway, for no
// reason the user can see. Twenty-one of them accumulated on the production
// box across a handful of restarts.
func reapStopped(pids []int, log *zap.Logger) {
	if len(pids) == 0 || !supportsProcessJanitor() {
		return
	}
	// A moment to exit on their own: a well-behaved server takes its children
	// with it, and killing a process that is already leaving is pointless.
	time.Sleep(500 * time.Millisecond)

	alive := map[int]bool{}
	for _, pid := range pids {
		if processAlive(pid) {
			alive[pid] = true
			terminatePID(pid)
		}
	}
	if len(alive) == 0 {
		return
	}
	time.Sleep(250 * time.Millisecond)
	killed := 0
	for pid := range alive {
		if processAlive(pid) {
			killPID(pid)
		}
		killed++
	}
	if log != nil && killed > 0 {
		log.Info("mcp: cleaned up processes left by a stopped server",
			zap.Int("processes", killed))
	}
}

// processAlive reports whether a pid still exists. Signal 0 checks for the
// process without touching it.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
