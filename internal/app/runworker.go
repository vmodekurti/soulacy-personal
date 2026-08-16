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
	"strings"

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
func beginRun(ctx context.Context, store *runs.Store, workspaceID, runID string, log *zap.Logger) (runs.Run, bool) {
	run, err := store.Transition(ctx, workspaceID, runID, runs.StatusRunning, runs.TransitionOptions{})
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

type runContextKey struct{}

// withRunID puts the durable run's identity on the context so the engine's
// side-effect recorder can find it.
//
// Carried on the context rather than passed as an argument because the
// reporting point is deep inside tool dispatch, several layers below anything
// that knows a run exists — and everything in between (chat, schedules,
// channels) legitimately has no run at all.
func withRunID(ctx context.Context, workspaceID, runID string) context.Context {
	if strings.TrimSpace(runID) == "" {
		return ctx
	}
	return context.WithValue(ctx, runContextKey{}, [2]string{workspaceID, runID})
}

func runFromContext(ctx context.Context) (workspaceID, runID string, ok bool) {
	pair, ok := ctx.Value(runContextKey{}).([2]string)
	if !ok {
		return "", "", false
	}
	return pair[0], pair[1], true
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
