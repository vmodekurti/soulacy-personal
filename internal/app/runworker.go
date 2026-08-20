package app

// runworker.go — executing a durable run (MU-020, last criterion).
//
// A durable run is submitted through /api/v1/runs and its caller leaves. The
// worker pool picks the message up like any other, but a run also has a record
// that has to move: queued → running → succeeded|failed. Without that, the
// record would be a submission receipt rather than a run anybody can follow.
//
// The run channel is not a chat channel and has no adapter. Its replies are
// stored on the record rather than sent anywhere, which is the whole point: the
// caller comes back for the result instead of holding a connection open.

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/runs"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/message"
)

// RunChannel and RunIDMetadataKey are aliases of the definitions that live
// with the record (internal/runs/message.go). Two spellings of "run_id" — one
// where the message is built, one where it is read — is a drift that produces
// a message which executes fine and updates nothing.
const (
	RunChannel       = runs.Channel
	RunIDMetadataKey = runs.IDMetadataKey
)

// RunIDOf returns the run id a message belongs to, if any.
func RunIDOf(msg message.Message) string {
	if msg.Metadata == nil {
		return ""
	}
	return strings.TrimSpace(msg.Metadata[RunIDMetadataKey])
}

// runPrincipal rebuilds the identity a run was admitted under.
//
// Taken from the record rather than from the worker's own context: a durable
// run may start minutes later, in a different process, picked up by a worker
// with no request behind it. The recorded principal is the only answer that is
// not a guess.
func runPrincipal(run runs.Run, requestID string) runtime.Principal {
	subject, workspaceID, membershipID, organizationID, role := run.Principal()
	if role == "" {
		role = "operator"
	}
	return runtime.Principal{
		Subject: subject, OrganizationID: organizationID, WorkspaceID: workspaceID,
		MembershipID: membershipID, Role: role, CredentialID: run.CredentialID,
		RequestID: requestID, Kind: run.PrincipalKind,
	}
}

// beginRun claims a queued run for execution.
//
// Returns ok=false when the run may not start — most importantly when it was
// cancelled between submission and pickup, which is the ordinary case for a
// queue with any depth. Claiming through the state machine rather than a
// separate flag means two workers cannot both claim it: the loser's transition
// finds the status already changed.
func beginRun(ctx context.Context, store *runs.Store, workspaceID, runID, owner string, log *zap.Logger) (runs.Run, bool) {
	run, err := store.Transition(ctx, workspaceID, runID, runs.StatusRunning,
		runs.TransitionOptions{Owner: owner, Lease: runs.DefaultRunLease})
	switch {
	case err == nil:
		return run, true
	case errors.Is(err, runs.ErrTerminal):
		log.Info("durable run was already finished when a worker picked it up",
			zap.String("run_id", runID))
	case errors.Is(err, runs.ErrInvalidTransition):
		// Another worker claimed it first. Not an error: exactly one of them
		// was going to win, and this is how the loser finds out.
		log.Debug("durable run claimed by another worker", zap.String("run_id", runID))
	case errors.Is(err, runs.ErrNotFound):
		log.Warn("durable run vanished before it could start", zap.String("run_id", runID))
	default:
		log.Error("durable run could not be started", zap.String("run_id", runID), zap.Error(err))
	}
	return runs.Run{}, false
}

// finishRun records the outcome.
//
// A transition refused because the run is already terminal is logged, not
// forced. That happens when someone cancelled the run while it was executing,
// and overwriting their decision with "succeeded" would tell them the
// cancellation did not take — the work did finish, but the record of what the
// operator asked for is the one that matters afterwards.
func finishRun(ctx context.Context, store *runs.Store, run runs.Run, result string, runErr error, log *zap.Logger) {
	status, opts := runs.StatusSucceeded, runs.TransitionOptions{Result: result}
	if runErr != nil {
		status, opts = runs.StatusFailed, runs.TransitionOptions{FailureReason: concise(runErr)}
	}
	if _, err := store.Transition(ctx, run.WorkspaceID, run.ID, status, opts); err != nil {
		if errors.Is(err, runs.ErrTerminal) {
			log.Info("durable run finished after it was cancelled; the cancellation stands",
				zap.String("run_id", run.ID), zap.String("would_have_been", status))
			return
		}
		log.Error("durable run outcome could not be recorded",
			zap.String("run_id", run.ID), zap.String("status", status), zap.Error(err))
	}
}

// replyText flattens an engine reply into the stored result.
func replyText(reply message.Message) string {
	var sb strings.Builder
	for _, part := range reply.Parts {
		if strings.TrimSpace(part.Text) == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(part.Text)
	}
	return sb.String()
}

// ── Side-effect marking (MU-021 criterion 6) ────────────────────────────────

// withRunID puts the durable run's identity on the context.
//
// The run ID goes on through runtime.WithRunID rather than a key of this
// package's own, because the engine needs the same fact for the run's scratch
// directory (runtime/runscratch.go). Two keys carrying one identity is how a
// run ends up marked for side effects but writing to the shared tree, or the
// reverse. The workspace comes back off the principal, which is already there.
func withRunID(ctx context.Context, workspaceID, runID string) context.Context {
	if strings.TrimSpace(runID) == "" {
		return ctx
	}
	if strings.TrimSpace(workspaceID) != "" && runtime.WorkspaceFromContext(ctx) != workspaceID {
		p, _ := runtime.PrincipalFromContext(ctx)
		p.WorkspaceID = workspaceID
		ctx = runtime.WithPrincipal(ctx, p)
	}
	return runtime.WithRunID(ctx, runID)
}

func runFromContext(ctx context.Context) (workspaceID, runID string, ok bool) {
	runID = runtime.RunIDFromContext(ctx)
	if runID == "" {
		return "", "", false
	}
	return runtime.WorkspaceFromContext(ctx), runID, true
}

// runSideEffectRecorder implements runtime.SideEffectRecorder against the run
// store. It is installed once, for the whole process.
//
// A call with no run on its context is not an error and not a miss: chat
// requests, scheduled invocations and channel messages all reach the same tool
// dispatch, and none of them has a durable record whose retry safety could be
// affected. Returning nil for them keeps the ordinary case silent.
type runSideEffectRecorder struct{ store *runs.Store }

func (r runSideEffectRecorder) RecordSideEffect(ctx context.Context, tool string) error {
	if r.store == nil {
		return nil
	}
	workspaceID, runID, ok := runFromContext(ctx)
	if !ok {
		return nil
	}
	// context.WithoutCancel: the marker must survive the run's own timeout.
	// A run killed by its deadline mid-tool-call is exactly the case the
	// marker exists for, and writing it through the dying context would lose
	// the fact that makes the run unsafe to retry.
	return r.store.MarkSideEffect(context.WithoutCancel(ctx), workspaceID, runID, tool)
}

// ── Cancellation observation (MU-027 criterion 5) ───────────────────────────

// cancelPollInterval is how often a running worker re-reads its own record.
//
// Two seconds is a compromise with a reason on each side. Shorter turns a
// long-running tenant into a steady read load on the run store for a signal
// that is almost never set. Longer makes "cancellation is acknowledged
// promptly" untrue in the only sense the person cancelling cares about —
// they are watching a spinner, and the gap between clicking and the work
// actually stopping is what they experience.
var cancelPollInterval = 2 * time.Second

// watchForCancellation cancels ctx when the run's record says to stop.
//
// Polling rather than a notification, because the cancel may be issued by a
// DIFFERENT gateway process: an in-memory channel would only reach a worker in
// the same process as the request, which is the arrangement MU-023 spent a
// story removing from the scheduler. The record is the one thing both
// processes can see.
//
// Returns a stop function; the caller must call it, or the poller outlives the
// run and holds a database handle for a record nobody is watching.
func watchForCancellation(ctx context.Context, cancel context.CancelFunc,
	store *runs.Store, run runs.Run, log *zap.Logger) func() {
	if store == nil {
		return func() {}
	}
	done := make(chan struct{})
	// stopped is closed when the goroutine has actually exited, and the
	// returned function waits for it.
	//
	// It used to return the moment `done` was closed, which made the guarantee
	// weaker than this function's own doc comment claims: a "stopped" watcher
	// could still be mid-poll, holding a database handle for a record nobody is
	// watching — the exact thing stopping it is for. Under -race the gap shows
	// up as the goroutine reading cancelPollInterval while the caller has moved
	// on, which is how it was found: internal/app had never been run under
	// -race, because the release gate did not include it.
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(cancelPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				// The run ended on its own. Nothing to cancel, and continuing
				// to poll a finished run is pure load.
				return
			case <-ticker.C:
				// context.WithoutCancel: this read must not inherit the run's
				// deadline, or a run near its timeout stops being able to
				// observe its own cancellation.
				current, err := store.Get(context.WithoutCancel(ctx), run.WorkspaceID, run.ID)
				if err != nil {
					// A read failure is not a cancellation. Cancelling on an
					// unreachable store would make a database hiccup kill
					// every in-flight run in the deployment.
					continue
				}
				if runs.CancelRequested(current.Status) {
					log.Info("durable run cancelled while executing",
						zap.String("run_id", run.ID), zap.String("status", current.Status))
					cancel()
					return
				}
			}
		}
	}()
	// Idempotent: executeDurableRun calls this on every path, and a second
	// close of `done` would panic. sync.Once rather than a boolean because the
	// cancellation path and the normal path can reach it concurrently.
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-stopped
	}
}

// finishCancelled closes out a run whose worker stopped because it was asked
// to.
//
// Separate from finishRun because the outcome is different in kind: the run did
// not fail, and recording it as failed would put a cancelled run in whatever
// dashboard counts failures. A run already marked `cancelled` — because it was
// queued when the request arrived — is left alone.
func finishCancelled(ctx context.Context, store *runs.Store, run runs.Run, log *zap.Logger) bool {
	current, err := store.Get(ctx, run.WorkspaceID, run.ID)
	if err != nil || !runs.CancelRequested(current.Status) {
		return false
	}
	if runs.Terminal(current.Status) {
		return true
	}
	if _, err := store.Transition(ctx, run.WorkspaceID, run.ID, runs.StatusCancelled,
		runs.TransitionOptions{FailureReason: "stopped after a cancellation request"}); err != nil {
		log.Warn("cancelled run could not be closed out",
			zap.String("run_id", run.ID), zap.Error(err))
	}
	return true
}

// holdRunLease renews this worker's claim for as long as the run executes, and
// returns a function that stops renewing and releases it.
//
// WHY RENEWAL RATHER THAN A LONG LEASE. The lease has to outlive any pause a
// live worker can take — a slow provider call, a stop-the-world GC, a machine
// that swaps — or a healthy run gets stolen and executed twice, which is the
// failure the lease exists to prevent, arriving from the other direction. A
// lease long enough to cover the worst pause is also long enough that a real
// crash strands the run for that long. Renewal decouples the two: the lease
// stays short, and a live worker keeps saying so.
//
// A LOST LEASE CANCELS THE RUN. If renewal fails because somebody else now
// holds the claim, this worker is executing a run another worker also has, and
// continuing means two engines writing one outcome. Cancelling is the safe
// side: the record already belongs to the other holder, and finishRun's
// status-conditioned UPDATE will refuse this one's outcome anyway — but
// stopping means it also stops making tool calls.
// runLeaseRenewInterval is how often holdRunLease renews.
//
// A variable rather than a constant so the lease tests can exercise the
// lost-lease path in milliseconds instead of parking the suite for twenty
// seconds. Production never writes it — the same shape internal/mcp uses for
// its drain grace.
var runLeaseRenewInterval = runs.RenewInterval

func holdRunLease(ctx context.Context, store *runs.Store, run runs.Run, owner string,
	onLost context.CancelFunc, log *zap.Logger) func() {
	if store == nil || owner == "" {
		return func() {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(runLeaseRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Renewed through a context that outlives the run's own
				// deadline is NOT wanted here: if the run's context is done
				// the run is over, and renewing a lease on a finished run
				// would hold it past the outcome.
				if err := store.RenewLease(ctx, run.WorkspaceID, run.ID, owner, runs.DefaultRunLease); err != nil {
					if errors.Is(err, runs.ErrLeaseLost) {
						log.Warn("this worker lost the lease on a run it is executing; stopping to avoid a second execution",
							zap.String("run_id", run.ID), zap.String("workspace_id", run.WorkspaceID))
						if onLost != nil {
							onLost()
						}
						return
					}
					// A transient failure is not a lost lease. Logged and
					// retried on the next tick: RenewInterval is a third of
					// the lease precisely so two of these can pass.
					log.Debug("run lease renewal failed; will retry",
						zap.String("run_id", run.ID), zap.Error(err))
				}
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
		// Released through a context that is NOT the run's: the run's context
		// is cancelled by the time this runs on the timeout path, and a
		// release that silently no-ops leaves the run looking held for a full
		// lease period after it finished — which delays recovery of the next
		// crash by exactly that long.
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := store.ReleaseLease(releaseCtx, run.WorkspaceID, run.ID, owner); err != nil {
			log.Debug("run lease release failed", zap.String("run_id", run.ID), zap.Error(err))
		}
	}
}

// workerID identifies this process as a lease holder.
//
// STABLE FOR THE LIFETIME OF THE PROCESS AND UNIQUE ACROSS PROCESSES, which is
// exactly what a lease owner has to be: stable so a renewal is recognised as
// coming from the holder, unique so a restarted process is not mistaken for
// the one that died. A hostname alone fails the second test — a container
// restarting keeps its hostname, and its runs would look renewable by the new
// process while the old one's work was actually gone.
//
// Computed once, lazily, rather than at construction, because the App is built
// in tests that never run a worker and a UUID per test is noise in nothing.
func (a *App) workerID() string {
	a.workerIDOnce.Do(func() {
		host, err := os.Hostname()
		if err != nil || strings.TrimSpace(host) == "" {
			host = "unknown-host"
		}
		a.workerIDValue = host + "/" + strconv.Itoa(os.Getpid()) + "/" + uuid.NewString()[:8]
	})
	return a.workerIDValue
}
