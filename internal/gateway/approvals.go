package gateway

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/approvals"
	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// isTruthy reports whether a header/query string represents an affirmative flag.
func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// approver resolves who is asking, and what they may currently do (MU-022).
//
// This replaces `authenticatedPrincipal`'s `admin` bool, which was computed as
// "the role is owner or admin" with NO WORKSPACE IN IT. Passed to the broker
// as authority, it made any workspace's admin an approver for every other
// workspace — and a reader of their paused calls' arguments.
//
// The workspace comes from the verified identity, never from the request. The
// permission comes from the live RBAC matrix rather than a role comparison, so
// "who may approve" has one definition and changing it is a one-line change in
// one file.
func (s *Server) approver(c *fiber.Ctx) approvals.Eligibility {
	workspaceID := wsroot.PersonalWorkspaceID
	subject := ""
	role := ""
	if identity, ok := requestIdentity(c); ok {
		workspaceID = wsroot.Normalize(identity.WorkspaceID())
		subject = strings.TrimSpace(identity.Subject())
		role = strings.TrimSpace(identity.Role())
	}
	if subject == "" {
		// A personal deployment authenticates with a static key and has no
		// subject. The local operator is the owner of the only workspace
		// there is; inventing an empty-subject "nobody" would break every
		// existing install to express a distinction it does not have.
		subject, role = "local", rbac.RoleOwner
	}
	return approvals.Eligibility{
		Subject:     subject,
		WorkspaceID: workspaceID,
		Permits: func(resource, action string) bool {
			return rbac.HasPermission(role, resource, action)
		},
	}
}

// handleListApprovals returns the tool calls this workspace is waiting on, so
// any paired device — including the mobile companion — can review them, and no
// device belonging to another tenant can.
func (s *Server) handleListApprovals(c *fiber.Ctx) error {
	by := s.approver(c)
	if by.Permits == nil || !by.Permits(rbac.ResourceApprovals, rbac.ActionRead) {
		// The arguments of a paused call are the details of something that was
		// stopped for being dangerous. Read access to them is not a lesser
		// privilege than deciding, and 403 rather than an empty list because
		// "you may not see this" and "there is nothing" are different answers.
		return s.errMsg(c, fiber.StatusForbidden, "approvals require the approvals:read permission")
	}
	return c.JSON(fiber.Map{"approvals": s.engine.Broker().List(c.UserContext(), by.WorkspaceID)})
}

// handleResolveApproval approves or denies a pending tool call by id.
func (s *Server) handleResolveApproval(decide bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := strings.TrimSpace(c.Params("id"))
		if id == "" {
			return s.errMsg(c, fiber.StatusBadRequest, "call id is required")
		}
		by := s.approver(c)
		err := s.engine.Broker().Resolve(c.UserContext(), by.WorkspaceID, id, decide, by, "")
		if err != nil {
			return s.approvalError(c, err)
		}
		outcome := "denied"
		if decide {
			outcome = "approved"
		}
		metrics.ApprovalsResolvedTotal.WithLabelValues(outcome).Inc()
		return c.JSON(fiber.Map{"ok": true, "approved": decide})
	}
}

// approvalError maps a store outcome onto a status code.
//
// ErrNotFound covers both "no such approval" and "it belongs to another
// workspace", and both answer 404 — distinguishing them would turn approval
// IDs into an enumeration oracle over other tenants' paused actions.
func (s *Server) approvalError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, approvals.ErrNotFound):
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "call_id not found — it may have already been resolved, expired, or never existed",
		})
	case errors.Is(err, approvals.ErrNotPending):
		// 409, not 404: the approval is real and the caller may see it. They
		// need to know somebody else already answered, which is exactly what
		// a team of approvers produces.
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error": "this approval has already been decided, expired, or was invalidated",
		})
	case errors.Is(err, approvals.ErrNotEligible):
		return s.errMsg(c, fiber.StatusForbidden, "deciding an approval requires the approvals:write permission")
	default:
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
}
