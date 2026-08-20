package app

import (
	"context"
	"time"
)

// rundrain.go — what happens to a run that is executing when the process is
// asked to stop.
//
// TWO BUGS, AND THE SECOND IS THE SURPRISING ONE.
//
// FIRST: the HTTP layer and the run workers disagreed about what "graceful"
// meant. One cancel() on SIGTERM started the gateway's drain — readiness fails,
// five seconds pass, in-flight requests get twenty-five more — AND cancelled
// the worker pool's context at the same instant. So a web request got a polite
// goodbye and an executing agent was cut off mid-tool-call. Same signal, same
// process, opposite treatment.
//
// SECOND: a clean shutdown recovered LESS work than a crash. When the run
// context was cancelled, the outcome path recorded the run as `failed` — a
// terminal state — and the recovery sweep skips terminal runs by design. A
// crash, by contrast, leaves the run `running` with a lease that lapses, and
// recovery then applies the real policy: re-queue what never touched the
// outside world, fail what did with the tool named.
//
// So `systemctl restart` lost work that `kill -9` would have recovered. That
// is backwards in a way nobody would predict, and it appears only in a
// deployment that restarts often — which is exactly what a Team deployment
// doing rolling upgrades is.
//
// The fix for the second is to do LESS: on shutdown, release the lease and
// write nothing. The run stays `running` with no holder, which is precisely
// what a crashed worker leaves behind, so the next boot's recovery makes the
// decision it was written to make.

// runDrainGrace is how long an executing run may keep running after the
// process has been asked to stop.
//
// Matched to the gateway's own drain budget rather than chosen independently:
// the two are the same event, and a run allowed to outlive the HTTP shutdown
// would hold the process open past the point where anything can observe it. A
// run that needs longer than this is not finishing during a restart under any
// budget, and leaving it to recovery is the better outcome — recovery knows
// whether it is safe to retry, and a partially-completed run does not.
//
// A variable so the shutdown tests can exercise the path in milliseconds.
var runDrainGrace = 20 * time.Second

// runDrainContext returns a context that survives parent's cancellation by
// runDrainGrace.
//
// The returned context is NOT a child of parent — it is deliberately detached
// and then cancelled on a timer — because a child would inherit the
// cancellation instantly, which is the behaviour being fixed. It keeps
// parent's VALUES, so the workspace, principal and run identity all travel
// with it.
//
// Also returns a function reporting whether the drain has begun, so the
// outcome path can tell "the process is stopping" from "this run's own
// deadline fired". Those need opposite treatment: the first must leave the
// record alone for recovery, the second is a genuine failure to record.
func runDrainContext(parent context.Context) (ctx context.Context, draining func() bool, stop func()) {
	detached, cancel := context.WithCancel(context.WithoutCancel(parent))
	started := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		select {
		case <-parent.Done():
			close(started)
			select {
			case <-time.After(runDrainGrace):
				// The grace period expired with work still running. Cancelling
				// is the honest end: the alternative is a process that will
				// not exit, and an operator reaching for SIGKILL, which is
				// the ungraceful shutdown all of this exists to avoid.
				cancel()
			case <-detached.Done():
			}
		case <-detached.Done():
		}
	}()

	draining = func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	}
	stop = func() {
		cancel()
		<-done
	}
	return detached, draining, stop
}
