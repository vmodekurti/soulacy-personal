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

// RunChannel is the pseudo-channel a durable run travels on.
const RunChannel = "run"

// RunIDMetadataKey carries the run id on the inbound message.
const RunIDMetadataKey = "run_id"

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
