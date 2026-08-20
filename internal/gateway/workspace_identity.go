package gateway

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/apiversion"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/tenancy"
)

// identityResponse is the stable schema behind `sy context show` and
// `sy whoami`. It reports the identity the server actually resolved for this
// request — not the claims the client presented — so a stale or over-broad
// token cannot make the CLI misreport who you are or what you may do.
type identityResponse struct {
	Subject          string   `json:"subject"`
	PrincipalKind    string   `json:"principal_kind"`
	CredentialID     string   `json:"credential_id,omitempty"`
	OrganizationID   string   `json:"organization_id"`
	OrganizationName string   `json:"organization_name,omitempty"`
	OrganizationLogo string   `json:"organization_logo,omitempty"`
	WorkspaceID      string   `json:"workspace_id"`
	WorkspaceName    string   `json:"workspace_name,omitempty"`
	WorkspaceLogo    string   `json:"workspace_logo,omitempty"`
	MembershipID     string   `json:"membership_id"`
	Role             string   `json:"role"`
	Scopes           []string `json:"scopes"`
	DeploymentMode   string   `json:"deployment_mode"`
	RequestID        string   `json:"request_id,omitempty"`

	// Permissions is what the VERIFIED role may do, projected from the RBAC
	// matrix (MU-030 criterion 2). It is served rather than duplicated in the
	// client because two copies of a permission table drift, and the drift is
	// worst in the direction that reads as a server bug: a control the GUI
	// offers for something the server refuses.
	//
	// Advisory only. Every route still authorizes itself; this tells a client
	// what to render and grants nothing. A client that ignored it entirely
	// would see exactly the same refusals.
	Permissions map[string][]string `json:"permissions,omitempty"`
}

type workspacesResponse struct {
	Workspaces        []tenancy.SubjectWorkspace `json:"workspaces"`
	ActiveWorkspaceID string                     `json:"active_workspace_id"`
}

func (s *Server) deploymentMode() string {
	if s == nil || s.config() == nil {
		return config.DeploymentModePersonal
	}
	return s.config().DeploymentMode()
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
	var organizationName, organizationLogo, workspaceName, workspaceLogo string
	if lister, supported := s.tenantResolver.(tenancy.WorkspaceLister); supported {
		if workspaces, err := lister.ListSubjectWorkspaces(c.UserContext(), identity.Subject()); err == nil {
			for _, workspace := range workspaces {
				if workspace.WorkspaceID == identity.WorkspaceID() {
					organizationName, organizationLogo = workspace.OrganizationName, workspace.OrganizationLogo
					workspaceName, workspaceLogo = workspace.WorkspaceName, workspace.WorkspaceLogo
					break
				}
			}
		}
	}
	return c.JSON(identityResponse{
		Subject:          identity.Subject(),
		PrincipalKind:    identity.PrincipalKind(),
		CredentialID:     identity.CredentialID(),
		OrganizationID:   identity.OrganizationID(),
		OrganizationName: organizationName,
		OrganizationLogo: organizationLogo,
		WorkspaceID:      identity.WorkspaceID(),
		WorkspaceName:    workspaceName,
		WorkspaceLogo:    workspaceLogo,
		MembershipID:     identity.MembershipID(),
		Role:             identity.Role(),
		Scopes:           scopes,
		DeploymentMode:   s.deploymentMode(),
		RequestID:        identity.RequestID(),
		// The role the SERVER resolved from stored membership, not the
		// broader one the caller's token may assert — the same distinction
		// the Role field above already makes, and for the same reason: a GUI
		// rendered from the token's role shows an operator every owner
		// control and then fails each one.
		Permissions: rbac.PermissionsFor(identity.Role()),
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

// handleCreateWorkspace lets an active workspace owner create another
// workspace inside the same organization. It deliberately grants the creator
// owner in the new workspace; creating an ownerless tenant would leave a row
// nobody can administer, while letting an admin mint themselves owner would be
// a cross-role elevation.
func (s *Server) handleCreateWorkspace(c *fiber.Ctx) error {
	identity, ok := requestIdentity(c)
	if !ok || identity.Role() != tenancy.RoleOwner || identity.PrincipalKind() != "user" {
		return s.errMsg(c, fiber.StatusForbidden, "workspace creation requires a workspace owner")
	}
	creator, supported := s.tenantResolver.(tenancy.WorkspaceCreator)
	if !supported {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "this deployment cannot create workspaces")
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := c.BodyParser(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "workspace name is required")
	}
	workspace, membership, err := creator.CreateWorkspaceForOwner(
		c.UserContext(), membershipMutation(identity), identity.OrganizationID(),
		identity.WorkspaceID(), body.Name, identity.Subject(),
	)
	s.recordAdminAudit(c, "workspace.created", "workspace", workspace.ID, auditOutcome(err), nil)
	if errors.Is(err, tenancy.ErrRoleEscalation) {
		return s.errMsg(c, fiber.StatusForbidden, "workspace creation requires a workspace owner")
	}
	if err != nil {
		return s.errMsg(c, fiber.StatusConflict, "workspace could not be created")
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"workspace": workspace, "membership": membership})
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

// handleCapabilities publishes what this build can do and which CLI versions
// it works with. It is reachable wherever /health is, because a client must be
// able to tell "your CLI is too old" apart from "your credentials are wrong".
func (s *Server) handleCapabilities(c *fiber.Ctx) error {
	return c.JSON(apiversion.Describe(config.Version, s.deploymentMode()))
}
