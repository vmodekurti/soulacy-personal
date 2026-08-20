// rundrain_test.go — a restart must not be worse than a crash.
package app

import (
	"context"
	"testing"
	"time"
)

func TestARunKeepsGoingBrieflyAfterShutdownIsRequested(t *testing.T) {
	// The HTTP layer gets a drain window on SIGTERM; the run workers got
	// nothing. One cancel() started the gateway's polite shutdown AND cut off
	// every executing agent mid-tool-call, in the same instant.
	previous := runDrainGrace
	runDrainGrace = 200 * time.Millisecond
	t.Cleanup(func() { runDrainGrace = previous })

	parent, shutdown := context.WithCancel(context.Background())
	runCtx, draining, stop := runDrainContext(parent)
	defer stop()

	shutdown() // SIGTERM

	// Not cancelled with its parent — that is the whole fix.
	select {
	case <-runCtx.Done():
		t.Fatal("the run was cancelled the instant shutdown began, with no grace at all")
	case <-time.After(50 * time.Millisecond):
	}
	if !draining() {
		t.Error("the run cannot tell that shutdown has begun, so the outcome path cannot distinguish " +
			"an interrupted run from a genuinely failed one")
	}

	// But it is bounded. A run that never finishes must not hold the process
	// open, or the operator reaches for SIGKILL and the graceful path bought
	// nothing.
	select {
	case <-runCtx.Done():
	case <-time.After(2 * time.Second):
		t.Error("the drain never expired; a stuck run would block process exit indefinitely")
	}
}

func TestARunContextKeepsItsValues(t *testing.T) {
	// The drain context is deliberately NOT a child of the app context, so it
	// would be easy to build one that has lost the workspace, the principal
	// and the run identity — all of which travel as context values and all of
	// which the run needs to record its own outcome correctly.
	type key struct{}
	parent := context.WithValue(context.Background(), key{}, "ws-a")

	runCtx, _, stop := runDrainContext(parent)
	defer stop()

	if got := runCtx.Value(key{}); got != "ws-a" {
		t.Errorf("value = %v, want ws-a — detaching the context dropped the identity travelling on it", got)
	}
}

func TestNotDrainingBeforeShutdown(t *testing.T) {
	// draining() gates whether an error is treated as "the process is
	// stopping" or "this run failed". Reporting true too early would make
	// every ordinary run failure look like a shutdown and leave it stuck in
	// `running` forever.
	runCtx, draining, stop := runDrainContext(context.Background())
	defer stop()

	if draining() {
		t.Error("draining() reported true with no shutdown requested")
	}
	if runCtx.Err() != nil {
		t.Error("the run context was already cancelled")
	}
}

func TestStoppingTheDrainReleasesItsGoroutine(t *testing.T) {
	// A run that finishes normally must not leave a timer goroutine waiting on
	// a shutdown that may never come. One per run, for the life of the
	// process, is a leak that only shows up under load.
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()

	runCtx, _, stop := runDrainContext(parent)
	stop() // returns only once the goroutine has exited

	if runCtx.Err() == nil {
		t.Error("stopping the drain left the run context live")
	}
}
