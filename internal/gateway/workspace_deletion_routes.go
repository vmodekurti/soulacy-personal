package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/requestctx"
	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/internal/workspacepurge"
)

// workspace_deletion_routes.go — MU-032 criteria 3, 4 and 6 at the HTTP edge.
//
// THE SHAPE IS REQUEST → WINDOW → PURGE, and the ordering is the safeguard
// rather than an implementation detail. `POST /workspace/deletion` does not
// delete anything. It moves the workspace to `deleting`, which
// workspaceContextMW already reads to refuse new writes and runs, and records
// a deadline. Nothing is destroyed until that deadline passes.
//
// A deletion that begins by removing data is one nobody can undo. A deletion
// that begins by refusing writes is one that can — and the mistake this
// protects against is usually noticed by somebody OTHER than the person who
// made it, who has to be awake, in another timezone, and looking.

// SetWorkspaceLifecycle wires the durable workspace lifecycle. Deletion routes
// answer 503 until it is present rather than pretending: a deletion that
// reports success without a store to record it is the worst possible failure
// of this particular endpoint.
func (s *Server) SetWorkspaceLifecycle(lifecycle tenancy.WorkspaceLifecycle) {
	s.workspaceLifecycle = lifecycle
}

func (s *Server) registerWorkspaceDeletionRoutes(api fiber.Router) {
	api.Get("/workspace/deletion", s.handleWorkspaceDeletionStatus)
	api.Post("/workspace/deletion", s.requireRecentAuth(),
		s.auditing("workspace.deletion_requested", "workspace", "", s.handleRequestWorkspaceDeletion))
	api.Delete("/workspace/deletion", s.requireRecentAuth(),
		s.auditing("workspace.deletion_cancelled", "workspace", "", s.handleCancelWorkspaceDeletion))
}

func (s *Server) workspaceLifecycleOr503(c *fiber.Ctx) (requestctx.Identity, tenancy.WorkspaceLifecycle, bool) {
	identity, err := s.workspaceOwner(c)
	if err != nil {
		_ = c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"error": "only a workspace owner can export or delete a workspace",
		})
		return requestctx.Identity{}, nil, false
	}
	if s.workspaceLifecycle == nil {
		_ = s.errMsg(c, fiber.StatusServiceUnavailable, "workspace deletion is unavailable on this deployment")
		return requestctx.Identity{}, nil, false
	}
	return identity, s.workspaceLifecycle, true
}

func deletionMutation(c *fiber.Ctx, identity requestctx.Identity) tenancy.Mutation {
	return tenancy.Mutation{
		ActorSubject: identity.Subject(),
		RequestID:    identity.RequestID(),
		At:           time.Now().UTC(),
	}
}

func (s *Server) handleWorkspaceDeletionStatus(c *fiber.Ctx) error {
	identity, lifecycle, ok := s.workspaceLifecycleOr503(c)
	if !ok {
		return nil
	}
	record, err := lifecycle.Workspace(c.UserContext(), identity.WorkspaceID())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "the workspace's status could not be read")
	}
	now := time.Now().UTC()
	return c.JSON(fiber.Map{
		"workspace":   record,
		"recoverable": record.Recoverable(now),
		"purge_due":   record.PurgeDue(now),
	})
}

func (s *Server) handleRequestWorkspaceDeletion(c *fiber.Ctx) error {
	identity, lifecycle, ok := s.workspaceLifecycleOr503(c)
	if !ok {
		return nil
	}
	var req DeletionRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body")
	}
	record, err := lifecycle.Workspace(c.UserContext(), identity.WorkspaceID())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "the workspace's status could not be read")
	}

	// The three gates, in the order checkDeletionGates defines: status, then
	// recent authentication, then the exact name, then the window. Each
	// defends against something the others cannot — see workspace_deletion.go.
	window, refusal := checkDeletionGates(req, record.Name, record.Status, recentlyAuthenticated(auth.ClaimsFromCtx(c), time.Now()))
	if refusal != nil {
		return c.Status(refusal.status).JSON(fiber.Map{
			"error": refusal.message, "code": refusal.code, "remedy": refusal.remedy,
		})
	}

	at := time.Now().UTC()
	updated, err := lifecycle.BeginDeletion(c.UserContext(), deletionMutation(c, identity), identity.WorkspaceID(), at.Add(window))
	switch {
	case errors.Is(err, tenancy.ErrWorkspaceNotActive):
		// Between the read above and this write, somebody else started one.
		// The store decides, not the read: a gate checked against a value
		// fetched a moment ago is a gate two concurrent owners both pass.
		return s.errMsg(c, fiber.StatusConflict, "this workspace is already being deleted")
	case errors.Is(err, tenancy.ErrWorkspaceLifecycleUnavailable):
		return s.errMsg(c, fiber.StatusNotImplemented,
			"a personal installation's only workspace is the installation itself; remove its data directory instead")
	case err != nil:
		return s.errMsg(c, fiber.StatusServiceUnavailable, "the deletion could not be recorded")
	}

	report := newDeletionReport(updated.ID, updated.Name, identity.Subject(), req.Reason, at, window)
	report.RecoverUntil = updated.RecoverUntil
	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"deletion": report,
		"remedy":   "DELETE /api/v1/workspace/deletion cancels this until " + updated.RecoverUntil.Format(time.RFC3339),
	})
}

func (s *Server) handleCancelWorkspaceDeletion(c *fiber.Ctx) error {
	identity, lifecycle, ok := s.workspaceLifecycleOr503(c)
	if !ok {
		return nil
	}
	updated, err := lifecycle.CancelDeletion(c.UserContext(), deletionMutation(c, identity), identity.WorkspaceID())
	switch {
	case errors.Is(err, tenancy.ErrWorkspaceNotDeleting):
		return s.errMsg(c, fiber.StatusConflict, "this workspace is not being deleted")
	case errors.Is(err, tenancy.ErrRecoveryWindowClosed):
		// 410, and distinct from the 409 above. "There was nothing to cancel"
		// and "there was, and you have missed it" mean opposite things to an
		// owner racing the deadline; one of them should stop looking and the
		// other should call support immediately.
		return s.errMsg(c, fiber.StatusGone,
			"the recovery window has closed and the purge has begun; this deletion can no longer be cancelled")
	case errors.Is(err, tenancy.ErrWorkspaceLifecycleUnavailable):
		return s.errMsg(c, fiber.StatusNotImplemented, "this deployment does not support workspace deletion")
	case err != nil:
		return s.errMsg(c, fiber.StatusServiceUnavailable, "the cancellation could not be recorded")
	}
	return c.JSON(fiber.Map{"workspace": updated, "status": updated.Status})
}

// PurgeWorkspaceIfDue runs the purge for a workspace whose recovery window has
// closed, and marks it deleted.
//
// Exported and taking a context rather than a request, because the caller is a
// background sweep: by the time a purge is due, the request that asked for it
// ended days ago.
//
// The ORDER is purge, then mark deleted, and never the reverse. `deleted` is
// the state that makes a workspace unreadable to its own members, so setting
// it first would hide a removal in progress from the people entitled to watch
// it — and would make a purge that then failed indistinguishable from one that
// succeeded.
func (s *Server) PurgeWorkspaceIfDue(ctx context.Context, workspaceID string, now time.Time) (workspacepurge.Report, error) {
	if s.workspaceLifecycle == nil {
		return workspacepurge.Report{}, tenancy.ErrWorkspaceLifecycleUnavailable
	}
	record, err := s.workspaceLifecycle.Workspace(ctx, workspaceID)
	if err != nil {
		return workspacepurge.Report{}, err
	}
	if !record.PurgeDue(now) {
		return workspacepurge.Report{}, nil
	}

	purgers := s.workspacePurgers()
	if err := workspacepurge.ValidatePurgers(purgers); err != nil {
		// Refusing to run beats running a purge whose report cannot be
		// trusted. A purger that disagrees with the catalog produces a report
		// claiming coverage it does not have, and that report is the artifact
		// a customer is given.
		return workspacepurge.Report{}, err
	}
	report, err := workspacepurge.Run(workspacepurge.Options{
		WorkspaceID: workspaceID, WorkspaceName: record.Name,
		RequestedBy: "system", Purgers: purgers, Ctx: ctx,
	})
	if err != nil {
		return report, err
	}

	// The workspace is marked deleted even when the purge was incomplete, and
	// the report says so. The alternative — leaving it `deleting` until
	// coverage is total — would keep a workspace the customer was told is gone
	// visible and writable to nobody, in a state no operator can resolve. The
	// honest record is "deleted, and these classes survived", which is exactly
	// what Report.Survivors carries.
	mutation := tenancy.Mutation{ActorSubject: "system", RequestID: "purge:" + workspaceID, At: now.UTC()}
	if _, err := s.workspaceLifecycle.CompleteDeletion(ctx, mutation, workspaceID); err != nil {
		return report, err
	}
	if !report.Complete {
		return report, workspacepurge.ErrIncomplete
	}
	return report, nil
}
