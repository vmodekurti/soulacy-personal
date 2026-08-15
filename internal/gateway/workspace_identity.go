package gateway

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/tenancy"
)

// identityResponse is the stable schema behind `sy context show` and
// `sy whoami`. It reports the identity the server actually resolved for this
// request — not the claims the client presented — so a stale or over-broad
// token cannot make the CLI misreport who you are or what you may do.
type identityResponse struct {
	Subject        string   `json:"subject"`
	PrincipalKind  string   `json:"principal_kind"`
	CredentialID   string   `json:"credential_id,omitempty"`
	OrganizationID string   `json:"organization_id"`
	WorkspaceID    string   `json:"workspace_id"`
	MembershipID   string   `json:"membership_id"`
	Role           string   `json:"role"`
	Scopes         []string `json:"scopes"`
	DeploymentMode string   `json:"deployment_mode"`
	RequestID      string   `json:"request_id,omitempty"`
}

type workspacesResponse struct {
	Workspaces        []tenancy.SubjectWorkspace `json:"workspaces"`
	ActiveWorkspaceID string                     `json:"active_workspace_id"`
}

func (s *Server) deploymentMode() string {
	if s == nil || s.cfg == nil {
		return config.DeploymentModePersonal
	}
	return s.cfg.DeploymentMode()
}

// handleWorkspaceIdentity answers "who am I, here?" — the question a CLI must
// ask before a destructive operation, and the one a user asks after switching
// contexts.
func (s *Server) handleWorkspaceIdentity(c *fiber.Ctx) error {
	identity, ok := requestIdentity(c)
	if !ok {
		return s.errMsg(c, fiber.StatusUnauthorized, "authenticated workspace context is required")
	}
	scopes := identity.Scopes()
	if scopes == nil {
		scopes = []string{}
	}
	return c.JSON(identityResponse{
		Subject:        identity.Subject(),
		PrincipalKind:  identity.PrincipalKind(),
		CredentialID:   identity.CredentialID(),
		OrganizationID: identity.OrganizationID(),
		WorkspaceID:    identity.WorkspaceID(),
		MembershipID:   identity.MembershipID(),
		Role:           identity.Role(),
		Scopes:         scopes,
		DeploymentMode: s.deploymentMode(),
		RequestID:      identity.RequestID(),
	})
}

// handleListSelectableWorkspaces enumerates the workspaces this subject may
// switch into. The list comes from stored memberships on every call, so a
// removed or suspended member stops seeing a workspace without waiting for a
// token to expire.
func (s *Server) handleListSelectableWorkspaces(c *fiber.Ctx) error {
	identity, ok := requestIdentity(c)
	if !ok {
		return s.errMsg(c, fiber.StatusUnauthorized, "authenticated workspace context is required")
	}
	lister, supported := s.tenantResolver.(tenancy.WorkspaceLister)
	if !supported {
		// Fail closed rather than implying the active workspace is the only
		// one that exists: the caller cannot distinguish those cases.
		return s.errMsg(c, fiber.StatusServiceUnavailable, "this deployment cannot enumerate workspaces")
	}
	workspaces, err := lister.ListSubjectWorkspaces(c.UserContext(), identity.Subject())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspaces could not be listed")
	}
	if workspaces == nil {
		workspaces = []tenancy.SubjectWorkspace{}
	}
	return c.JSON(workspacesResponse{Workspaces: workspaces, ActiveWorkspaceID: identity.WorkspaceID()})
}

// handleSelectWorkspace verifies that a requested workspace is one the subject
// may act in and echoes the verified selection. The CLI stores only the
// returned IDs: a client may request a workspace, but only the server decides
// whether that request is legitimate.
func (s *Server) handleSelectWorkspace(c *fiber.Ctx) error {
	identity, ok := requestIdentity(c)
	if !ok {
		return s.errMsg(c, fiber.StatusUnauthorized, "authenticated workspace context is required")
	}
	var body struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body")
	}
	requested := strings.TrimSpace(body.WorkspaceID)
	if requested == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "workspace_id is required")
	}
	if s.tenantResolver == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace membership resolver is unavailable")
	}
	membership, err := s.tenantResolver.ResolveMembership(c.UserContext(), identity.Subject(), requested)
	if err != nil {
		// 404 rather than 403: confirming that a workspace exists but is
		// closed to you is an enumeration oracle.
		return s.errMsg(c, fiber.StatusNotFound, "workspace not found")
	}
	return c.JSON(tenancy.SubjectWorkspace{
		OrganizationID: membership.OrganizationID,
		WorkspaceID:    membership.WorkspaceID,
		MembershipID:   membership.MembershipID,
		Role:           membership.Role,
		PrincipalKind:  identity.PrincipalKind(),
	})
}
