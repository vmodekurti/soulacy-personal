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
	return s.membershipMutationResponse(c, membership, err)
}

func (s *Server) handleRemoveWorkspaceMember(c *fiber.Ctx) error {
	identity, err := s.membershipAdmin(c)
	if err != nil {
		return err
	}
	membership, err := s.tenantMembers.SetMembershipStatusInWorkspace(c.UserContext(), membershipMutation(identity), identity.WorkspaceID(), c.Params("id"), identity.Role(), tenancy.MembershipDeleted)
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
	entries, err := s.tenantMembers.ListMembershipAudit(c.UserContext(), identity.WorkspaceID(), limit)
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "membership audit could not be loaded")
	}
	return c.JSON(fiber.Map{"events": entries})
}
