package gateway

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/requestctx"
	"github.com/soulacy/soulacy/internal/tenancy"
)

func membershipMutation(identity requestctx.Identity) tenancy.Mutation {
	return tenancy.Mutation{ActorSubject: identity.Subject(), RequestID: identity.RequestID(), At: time.Now().UTC()}
}

func (s *Server) membershipAdmin(c *fiber.Ctx) (requestctx.Identity, error) {
	identity, ok := requestIdentity(c)
	if !ok || (identity.Role() != tenancy.RoleOwner && identity.Role() != tenancy.RoleAdmin) {
		return requestctx.Identity{}, fiber.NewError(fiber.StatusForbidden, "workspace membership administration is not permitted")
	}
	if s.tenantMembers == nil {
		return requestctx.Identity{}, fiber.NewError(fiber.StatusServiceUnavailable, "workspace membership service is unavailable")
	}
	return identity, nil
}

func (s *Server) handleListWorkspaceMembers(c *fiber.Ctx) error {
	identity, err := s.membershipAdmin(c)
	if err != nil {
		return err
	}
	members, err := s.tenantMembers.ListMembers(c.UserContext(), identity.WorkspaceID())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace members could not be loaded")
	}
	return c.JSON(fiber.Map{"members": members, "current_role": identity.Role()})
}

func (s *Server) handleCreateWorkspaceInvitation(c *fiber.Ctx) error {
	identity, err := s.membershipAdmin(c)
	if err != nil {
		return err
	}
	var req struct {
		Email     string `json:"email"`
		Role      string `json:"role"`
		ExpiresIn string `json:"expires_in"`
	}
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request")
	}
	if !tenancy.CanAdministerRole(identity.Role(), req.Role) {
		return s.errMsg(c, fiber.StatusForbidden, "requested role cannot be administered")
	}
	ttl := 7 * 24 * time.Hour
	if strings.TrimSpace(req.ExpiresIn) != "" {
		ttl, err = time.ParseDuration(req.ExpiresIn)
		if err != nil || ttl < time.Minute || ttl > 30*24*time.Hour {
			return s.errMsg(c, fiber.StatusBadRequest, "expires_in must be between 1 minute and 30 days")
		}
	}
	invitation, err := s.tenantMembers.CreateInvitation(c.UserContext(), membershipMutation(identity), identity.OrganizationID(), identity.WorkspaceID(), identity.Role(), req.Email, req.Role, time.Now().UTC().Add(ttl))
	// The invited ROLE and the expiry are recorded; the email is not, and the
	// token certainly is not. An invitation is a durable way into the
	// workspace, so what matters in the trail is what authority it grants and
	// for how long — and an audit record is read by more people than the
	// invitation was addressed to.
	s.recordAdminAudit(c, "invitation.created", "invitation", "", auditOutcome(err),
		map[string]any{"role": req.Role, "expires_in": ttl.String()})
	if err != nil {
		return s.errMsg(c, fiber.StatusConflict, "invitation could not be created")
	}
	return c.Status(fiber.StatusCreated).JSON(invitation)
}

func (s *Server) handleListWorkspaceInvitations(c *fiber.Ctx) error {
	identity, err := s.membershipAdmin(c)
	if err != nil {
		return err
	}
	invitations, err := s.tenantMembers.ListInvitations(c.UserContext(), identity.WorkspaceID())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace invitations could not be loaded")
	}
	return c.JSON(fiber.Map{"invitations": invitations})
}

func (s *Server) handleAcceptInvitation(c *fiber.Ctx) error {
	if s.tenantMembers == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "invitation service is unavailable")
	}
	claims := auth.ClaimsFromCtx(c)
	if claims == nil || strings.TrimSpace(claims.Subject) == "" || claims.Kind != "access" {
		return s.errMsg(c, fiber.StatusUnauthorized, "authentication failed")
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := c.BodyParser(&req); err != nil || strings.TrimSpace(req.Token) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request")
	}
	requestID := localString(c.Locals("request_id"))
	if requestID == "" {
		requestID = "invitation-accept"
	}
	membership, err := s.tenantMembers.AcceptInvitation(c.UserContext(), tenancy.Mutation{ActorSubject: claims.Subject, RequestID: requestID, At: time.Now().UTC()}, req.Token, claims.Subject)
	if err != nil {
		// Deliberately collapse unknown, expired, already-used-by-another-user,
		// and email-mismatch cases into one non-enumerable response.
		return s.errMsg(c, fiber.StatusNotFound, "invitation is invalid or expired")
	}
	return c.JSON(fiber.Map{"membership": membership})
}

func (s *Server) handleSetWorkspaceMemberRole(c *fiber.Ctx) error {
	identity, err := s.membershipAdmin(c)
	if err != nil {
		return err
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := c.BodyParser(&req); err != nil || !tenancy.IsMembershipRole(req.Role) {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid role")
	}
	membership, err := s.tenantMembers.SetMembershipRole(c.UserContext(), membershipMutation(identity), identity.WorkspaceID(), c.Params("id"), identity.Role(), req.Role)
	// MU-031 criterion 2. Membership changes already reach the tenancy store's
	// own trail; they were absent from the WORKSPACE audit trail, which is the
	// one an owner reads. Two trails that each hold half the story is worse
	// than one that holds all of it: the investigation that matters is "what
	// happened in this workspace", and it was answerable only by knowing to
	// look somewhere else.
	//
	// The requested role is recorded, not the granted one — they differ
	// exactly when the store refused an escalation, and that difference is the
	// interesting record.
	s.recordAdminAudit(c, "membership.role_changed", "membership", c.Params("id"), auditOutcome(err),
		map[string]any{"requested_role": req.Role})
	return s.membershipMutationResponse(c, membership, err)
}

func (s *Server) handleSetWorkspaceMemberStatus(c *fiber.Ctx) error {
	identity, err := s.membershipAdmin(c)
	if err != nil {
		return err
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request")
	}
	membership, err := s.tenantMembers.SetMembershipStatusInWorkspace(c.UserContext(), membershipMutation(identity), identity.WorkspaceID(), c.Params("id"), identity.Role(), req.Status)
	s.recordAdminAudit(c, "membership.status_changed", "membership", c.Params("id"), auditOutcome(err),
		map[string]any{"requested_status": req.Status})
	return s.membershipMutationResponse(c, membership, err)
}

func (s *Server) handleRemoveWorkspaceMember(c *fiber.Ctx) error {
	identity, err := s.membershipAdmin(c)
	if err != nil {
		return err
	}
	membership, err := s.tenantMembers.SetMembershipStatusInWorkspace(c.UserContext(), membershipMutation(identity), identity.WorkspaceID(), c.Params("id"), identity.Role(), tenancy.MembershipDeleted)
	s.recordAdminAudit(c, "membership.removed", "membership", c.Params("id"), auditOutcome(err), nil)
	return s.membershipMutationResponse(c, membership, err)
}

func (s *Server) membershipMutationResponse(c *fiber.Ctx, membership tenancy.StoredMembership, err error) error {
	if err == nil {
		return c.JSON(fiber.Map{"membership": membership})
	}
	if errors.Is(err, tenancy.ErrLastOwner) {
		return s.errMsg(c, fiber.StatusConflict, err.Error())
	}
	if errors.Is(err, tenancy.ErrRoleEscalation) {
		return s.errMsg(c, fiber.StatusForbidden, "membership cannot be administered")
	}
	return s.errMsg(c, fiber.StatusNotFound, "membership not found")
}

func (s *Server) handleWorkspaceMembershipAudit(c *fiber.Ctx) error {
	identity, err := s.membershipAdmin(c)
	if err != nil {
		return err
	}
	if identity.Role() != tenancy.RoleOwner {
		return s.errMsg(c, fiber.StatusForbidden, "workspace owner role is required")
	}
	limit, _ := strconv.Atoi(c.Query("limit", "100"))
	// Keyset-paginated (MU-031 criterion 4). An offset cursor skips or repeats
	// rows whenever a write lands between two pages, and this trail is
	// append-only under exactly the conditions somebody reads it — during an
	// investigation, while access is being changed.
	page, err := s.tenantMembers.ListMembershipAuditPage(c.UserContext(), identity.WorkspaceID(), limit, c.Query("cursor"))
	if err != nil {
		if errors.Is(err, tenancy.ErrInvalidAuditCursor) {
			// 400, not an empty page. A malformed cursor answered with no
			// results makes a client bug and a tampering attempt both look
			// like the end of the trail, and an investigator would conclude it
			// stopped there.
			return s.errMsg(c, fiber.StatusBadRequest, "the page cursor is not one this server issued")
		}
		return s.errMsg(c, fiber.StatusServiceUnavailable, "membership audit could not be loaded")
	}
	entries := page.Entries
	// MU-031 criterion 4. Reading who has been added, removed or re-roled is
	// how somebody learns the shape of a workspace's access, and it left no
	// trace. Recorded after a successful read so a refusal does not produce a
	// record implying the data was served, and carrying the size rather than
	// the contents — a copy of the trail inside itself helps nobody.
	s.recordAdminAudit(c, "audit.read", "membership_audit", identity.WorkspaceID(), "ok", map[string]any{
		"limit":    limit,
		"returned": len(entries),
	})
	// next_cursor is always present, empty on the last page. Omitting it there
	// would make an exhausted trail indistinguishable from a server too old to
	// paginate, so a client would have to guess which it was looking at.
	return c.JSON(fiber.Map{"events": entries, "next_cursor": page.NextCursor})
}

// auditOutcome turns an operation's error into the audit trail's status.
//
// A FAILED attempt is recorded, not skipped. "Somebody tried to make
// themselves an owner and the store refused" is one of the more interesting
// lines an audit trail can carry, and a trail that records only successes
// cannot show an attack that did not work.
func auditOutcome(err error) string {
	if err != nil {
		return "failed"
	}
	return "ok"
}
