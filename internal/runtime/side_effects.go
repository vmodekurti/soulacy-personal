package runtime

import (
	"context"

	"go.uber.org/zap"
)

// side_effects.go — MU-021 criterion 6: "worker loss causes bounded retry only
// for retry-safe stages; side-effecting calls are not repeated without
// idempotency proof or approval."
//
// A durable run whose worker dies leaves a record in `running`. Re-enqueueing
// it blindly is how one crash becomes two payments, two emails, two deletions.
// Refusing to retry anything is the other failure: a run that had not yet done
// anything is retry-safe by construction, and abandoning it makes a restart
// lose work for no safety gain.
//
// The distinction needs a fact, not a guess. SideEffectRecorder is that fact:
// the engine reports the first outside-visible call a run makes, and the run
// record remembers it. A run with no recorded side effect may be re-queued; a
// run with one may not, and says so in its failure reason.
//
// WHY THE ENGINE DOES NOT KNOW ABOUT RUNS. The recorder takes a context and
// nothing else. The engine has no runs.Store, no run ID, and no opinion about
// durability — it reports "this run just did something observable" and the
// app-side implementation decides whether any record is listening. A chat
// request, a scheduled invocation and a channel message all reach the same
// dispatch path with no run on their context, and for them this is a no-op.

// SideEffectRecorder notes that the run on ctx has made its first
// outside-visible call. Implementations must be idempotent: dispatch reports
// every side-effecting call, not only the first.
type SideEffectRecorder interface {
	RecordSideEffect(ctx context.Context, tool string) error
}

// SetSideEffectRecorder installs the recorder. nil disables recording, which
// is the correct state for a deployment with no durable run store: there is no
// record to protect, so there is nothing to mark.
func (e *Engine) SetSideEffectRecorder(r SideEffectRecorder) { e.sideEffects = r }

// recordSideEffect is called from the tool dispatch choke point.
//
// A recording failure is logged and the call proceeds. The alternative —
// refusing to run the tool because the bookkeeping failed — converts a
// database hiccup into a run failure, and the conservative reading is already
// safe: a run whose side effect went unrecorded is treated as retryable, which
// is wrong, but a run that cannot execute at all is wrong every time. The log
// line is what tells an operator the marker is unreliable.
func (e *Engine) recordSideEffect(ctx context.Context, tool string) {
	if e.sideEffects == nil {
		return
	}
	if err := e.sideEffects.RecordSideEffect(ctx, tool); err != nil && e.log != nil {
		e.log.Warn("side effect could not be recorded; a lost worker may retry this run",
			zap.String("tool", tool), zap.Error(err))
	}
}
