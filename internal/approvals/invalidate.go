package approvals

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/wsroot"
)

// invalidate.go — MU-022 criterion 3: "membership revocation, policy changes,
// run cancellation, or expiry invalidate pending decisions."
//
// All four are the same shape: something that was true when the question was
// asked stopped being true, so the answer must not be given. They are separate
// entry points rather than one because the REASON is the useful part — an
// approver who comes back to a closed request needs to know whether the run
// was cancelled or their own access was removed, and an audit reading the
// table later needs to tell "a control fired" from "a control lapsed".
//
// Every one of them writes a terminal status conditioned on `status = pending`,
// so an invalidation racing a human's decision loses rather than overwriting
// it. That direction is deliberate: a decision a person actually made is a
// better record than the system's guess that it was moot.

// InvalidationReason explains why a pending approval was closed without an
// answer. Stored in decision_reason, where a human's own reason would go —
// the field means "why does this record say what it says", and the system
// answering it is as legitimate as a person.
const (
	ReasonRunEnded            = "the run this was blocking is no longer active"
	ReasonMembershipRevoked   = "the requester no longer has access to this workspace"
	ReasonPolicyChanged       = "the policy that produced this request changed"
	ReasonExpired             = "nobody answered before it expired"
	ReasonWorkspaceRestarting = "the process holding this run restarted"
)

// InvalidateRun closes every pending approval blocking one run.
//
// Called when a run is cancelled or ends. An approval whose run is gone is
// unanswerable in the strongest sense: nothing is waiting for the answer, so
// approving it would release nothing while telling the approver they had
// authorized something.
func (s *Store) InvalidateRun(ctx context.Context, workspaceID, runID, reason string) (int, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return 0, nil
	}
	return s.invalidate(ctx, `workspace_id = ? AND run_id = ? AND status = ?`,
		reason, wsroot.Normalize(workspaceID), runID)
}

// InvalidateSubject closes every pending approval a subject requested.
//
// Called on membership revocation. The requester losing access is not the same
// as the approver losing access — the request itself is what stops being
// legitimate, because it was made with authority the person no longer has.
func (s *Store) InvalidateSubject(ctx context.Context, workspaceID, subject, reason string) (int, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return 0, nil
	}
	return s.invalidate(ctx, `workspace_id = ? AND requester_subject = ? AND status = ?`,
		reason, wsroot.Normalize(workspaceID), subject)
}

// InvalidateWorkspace closes every pending approval in a workspace.
//
// Called on a policy change broad enough that no pending request can be
// assumed still correct, and at startup: a restart severs every channel a
// blocked run was waiting on, so a record still saying "pending" describes a
// question nobody is listening for the answer to.
func (s *Store) InvalidateWorkspace(ctx context.Context, workspaceID, reason string) (int, error) {
	return s.invalidate(ctx, `workspace_id = ? AND status = ?`, reason, wsroot.Normalize(workspaceID))
}

// InvalidateAllPending closes every pending approval in every workspace.
//
// Deployment-wide and named so it cannot be reached by accident, for the same
// reason the run recovery sweep is: a restart has no request and therefore no
// tenant. Each row keeps its own workspace; nothing is re-homed.
func (s *Store) InvalidateAllPending(ctx context.Context, reason string) (int, error) {
	return s.invalidate(ctx, `status = ?`, reason)
}

func (s *Store) invalidate(ctx context.Context, where, reason string, args ...any) (int, error) {
	if strings.TrimSpace(reason) == "" {
		reason = ReasonPolicyChanged
	}
	now := time.Now().UTC()
	// `status = ?` is the last placeholder in every where clause above, so
	// StatusPending is appended once here rather than by four callers — the
	// condition that makes an invalidation lose to a real decision is not
	// something a caller should be able to omit.
	args = append(args, StatusPending)
	res, err := s.db.ExecContext(ctx,
		`UPDATE tool_approvals SET status = ?, decision_reason = ?, updated_at = ? WHERE `+where,
		append([]any{StatusInvalidated, reason, now}, args...)...)
	if err != nil {
		return 0, fmt.Errorf("approvals: invalidate: %w", err)
	}
	affected, _ := res.RowsAffected()
	return int(affected), nil
}

// SweepExpired relabels approvals nobody answered in time, across every
// workspace.
//
// The sweep is bookkeeping, not enforcement: Approval.Pending already refuses
// an expired record on read, so a sweep that never ran would still be safe.
// What it buys is that the stored status matches what every reader computes,
// which is the difference between an audit trail and a set of rows that need
// interpreting.
func (s *Store) SweepExpired(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`UPDATE tool_approvals SET status = ?, decision_reason = ?, updated_at = ?
          WHERE status = ? AND expires_at <= ?`,
		StatusExpired, ReasonExpired, now, StatusPending, now)
	if err != nil {
		return 0, fmt.Errorf("approvals: sweep: %w", err)
	}
	affected, _ := res.RowsAffected()
	return int(affected), nil
}
