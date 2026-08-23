package gateway

import (
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/tenancy"
)

// handlePlatformOverview is the landing payload for the deployment control
// plane. It combines safe catalog counts with replica readiness. It never
// resolves a workspace and never returns tenant-owned data.
func (s *Server) handlePlatformOverview(c *fiber.Ctx) error {
	catalog, ok := s.tenantResolver.(tenancy.PlatformCatalog)
	if !ok {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "platform tenant catalog is unavailable")
	}
	summary, err := catalog.PlatformOverview(c.UserContext())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "platform tenant catalog could not be read")
	}
	ready, readiness, dependencies := s.readinessState(c.UserContext())
	authStatus, authMode, authDetail := s.authPosture()
	cfg := s.config()
	issues := cfg.DeploymentReadinessIssues()
	acknowledgedIssues := cfg.AcknowledgedDeploymentIssues()
	return c.JSON(fiber.Map{
		"mode": cfg.DeploymentMode(), "version": config.Version,
		"timestamp": time.Now().UTC(), "summary": summary,
		"replica":        fiber.Map{"ready": ready, "status": readiness, "draining": s.Draining()},
		"dependencies":   dependencies,
		"authentication": fiber.Map{"status": authStatus, "mode": authMode, "detail": authDetail},
		"deployment": fiber.Map{
			"profile": cfg.Deployment.Profile, "owner": cfg.Deployment.Owner,
			"region": cfg.Deployment.Region, "notes": cfg.Deployment.Notes,
			"ready": len(issues) == 0, "issues": issues, "acknowledged_issues": acknowledgedIssues,
			"storage_backend": cfg.Storage.Backend, "queue_backend": cfg.Queue.Backend,
			"kms_provider": cfg.Credentials.KMSProvider, "executor_backend": cfg.Executor.Backend,
			"sandbox_enabled": cfg.Runtime.Sandbox.Enabled, "sandbox_mode": cfg.Runtime.Sandbox.Mode,
			"rate_limit_backend": cfg.RateLimit.Backend, "rate_limit_enabled": cfg.RateLimit.Enabled,
		},
	})
}

func (s *Server) handlePlatformAudit(c *fiber.Ctx) error {
	reader, ok := s.tenantResolver.(tenancy.PlatformAuditReader)
	if !ok {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "platform audit catalog is unavailable")
	}
	page, err := reader.ListPlatformAuditPage(c.UserContext(), c.QueryInt("limit", 100), strings.TrimSpace(c.Query("cursor")))
	if errors.Is(err, tenancy.ErrInvalidAuditCursor) {
		return s.errMsg(c, fiber.StatusBadRequest, "cursor is not a valid page cursor")
	}
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "platform audit catalog could not be read")
	}
	events := make([]adminAuditRecord, 0, len(page.Entries))
	for _, entry := range page.Entries {
		events = append(events, adminAuditRecord{
			Timestamp: entry.CreatedAt, Action: entry.Action, Resource: entry.ResourceType,
			Target: entry.ResourceID, Actor: entry.ActorSubject, RequestID: entry.RequestID, Status: "ok",
		})
	}
	return c.JSON(fiber.Map{"events": events, "next_cursor": page.NextCursor})
}

type platformStatusRequest struct {
	Status      string `json:"status"`
	Reason      string `json:"reason"`
	ConfirmName string `json:"confirm_name"`
}

func parsePlatformStatusRequest(c *fiber.Ctx) (platformStatusRequest, error) {
	var request platformStatusRequest
	if err := c.BodyParser(&request); err != nil {
		return request, errors.New("invalid lifecycle request")
	}
	request.Status = strings.ToLower(strings.TrimSpace(request.Status))
	request.Reason = strings.TrimSpace(request.Reason)
	request.ConfirmName = strings.TrimSpace(request.ConfirmName)
	if request.Status != tenancy.WorkspaceActive && request.Status != tenancy.WorkspaceSuspended {
		return request, errors.New("status must be active or suspended")
	}
	if request.Status == tenancy.WorkspaceSuspended && request.Reason == "" {
		return request, errors.New("a suspension reason is required")
	}
	return request, nil
}

func (s *Server) handlePlatformOrganizationStatus(c *fiber.Ctx) error {
	manager, ok := s.tenantResolver.(tenancy.PlatformLifecycleManager)
	if !ok {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "platform lifecycle management is unavailable")
	}
	request, err := parsePlatformStatusRequest(c)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	catalog, catalogOK := s.tenantResolver.(tenancy.PlatformCatalog)
	if !catalogOK {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "platform tenant catalog is unavailable")
	}
	organizations, err := catalog.ListPlatformOrganizations(c.UserContext())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "organization could not be verified")
	}
	name := ""
	for _, organization := range organizations {
		if organization.ID == c.Params("id") {
			name = organization.Name
			break
		}
	}
	if name == "" {
		return s.errMsg(c, fiber.StatusNotFound, "organization was not found")
	}
	if request.Status == tenancy.WorkspaceSuspended && request.ConfirmName != name {
		return s.errMsg(c, fiber.StatusBadRequest, "type the organization name to confirm suspension")
	}
	organization, err := manager.SetOrganizationStatus(c.UserContext(), s.platformMutation(c), c.Params("id"), request.Status, request.Reason)
	if err != nil {
		return s.errMsg(c, fiber.StatusConflict, err.Error())
	}
	s.recordAdminAudit(c, "organization.status.update", "organization", organization.ID, "ok", map[string]any{"status": organization.Status, "reason": request.Reason})
	return c.JSON(fiber.Map{"organization": organization})
}

func (s *Server) handlePlatformWorkspaceStatus(c *fiber.Ctx) error {
	manager, ok := s.tenantResolver.(tenancy.PlatformLifecycleManager)
	if !ok {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "platform lifecycle management is unavailable")
	}
	request, err := parsePlatformStatusRequest(c)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	name := ""
	if catalog, catalogOK := s.tenantResolver.(tenancy.PlatformCatalog); catalogOK {
		organizations, listErr := catalog.ListPlatformOrganizations(c.UserContext())
		if listErr != nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace could not be verified")
		}
		for _, organization := range organizations {
			for _, workspace := range organization.Workspaces {
				if workspace.ID == c.Params("workspaceID") {
					name = workspace.Name
				}
			}
		}
	}
	if name == "" {
		return s.errMsg(c, fiber.StatusNotFound, "workspace was not found")
	}
	if request.Status == tenancy.WorkspaceSuspended && request.ConfirmName != name {
		return s.errMsg(c, fiber.StatusBadRequest, "type the workspace name to confirm suspension")
	}
	workspace, err := manager.SetWorkspaceStatus(c.UserContext(), s.platformMutation(c), c.Params("workspaceID"), request.Status, request.Reason)
	if err != nil {
		return s.errMsg(c, fiber.StatusConflict, err.Error())
	}
	s.recordAdminAudit(c, "workspace.status.update", "workspace", workspace.ID, "ok", map[string]any{"status": workspace.Status, "reason": request.Reason})
	return c.JSON(fiber.Map{"workspace": workspace})
}

func (s *Server) platformMutation(c *fiber.Ctx) tenancy.Mutation {
	requestID := strings.TrimSpace(c.Get("X-Request-ID"))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	return tenancy.Mutation{ActorSubject: "platform:administrator", RequestID: requestID, At: time.Now().UTC()}
}

func (s *Server) handlePlatformProvisionOrganization(c *fiber.Ctx) error {
	provisioner, ok := s.tenantResolver.(tenancy.PlatformProvisioner)
	if !ok {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "platform tenant provisioning is unavailable")
	}
	var request tenancy.BootstrapRequest
	if err := c.BodyParser(&request); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid organization request")
	}
	result, err := provisioner.ProvisionOrganization(c.UserContext(), s.platformMutation(c), request)
	if err != nil {
		if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "address") {
			return s.errMsg(c, fiber.StatusBadRequest, err.Error())
		}
		return s.errMsg(c, fiber.StatusInternalServerError, "organization could not be provisioned")
	}
	s.recordAdminAudit(c, "tenant.provision", "organization", result.Organization.ID, "ok", map[string]any{"workspace_id": result.Workspace.ID, "owner_user_id": result.User.ID})
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"result": result})
}

func (s *Server) handlePlatformProvisionWorkspace(c *fiber.Ctx) error {
	provisioner, ok := s.tenantResolver.(tenancy.PlatformProvisioner)
	if !ok {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "platform workspace provisioning is unavailable")
	}
	var request tenancy.WorkspaceProvisionRequest
	if err := c.BodyParser(&request); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid workspace request")
	}
	result, err := provisioner.ProvisionWorkspace(c.UserContext(), s.platformMutation(c), c.Params("id"), request)
	if err != nil {
		if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "address") {
			return s.errMsg(c, fiber.StatusBadRequest, err.Error())
		}
		return s.errMsg(c, fiber.StatusInternalServerError, "workspace could not be provisioned")
	}
	s.recordAdminAudit(c, "workspace.provision", "workspace", result.Workspace.ID, "ok", map[string]any{"organization_id": result.Organization.ID, "owner_user_id": result.User.ID})
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"result": result})
}

func (s *Server) handlePlatformOrganizations(c *fiber.Ctx) error {
	catalog, ok := s.tenantResolver.(tenancy.PlatformCatalog)
	if !ok {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "platform tenant catalog is unavailable")
	}
	organizations, err := catalog.ListPlatformOrganizations(c.UserContext())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "platform organizations could not be read")
	}
	return c.JSON(fiber.Map{"organizations": organizations, "count": len(organizations)})
}

func (s *Server) handlePlatformWorkspaceAddress(c *fiber.Ctx) error {
	manager, ok := s.tenantResolver.(tenancy.WorkspaceAddressManager)
	if !ok {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace address management is unavailable")
	}
	var request struct {
		Slug string `json:"slug"`
	}
	if err := c.BodyParser(&request); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid workspace address request")
	}
	workspace, err := manager.SetWorkspaceSlug(c.UserContext(), s.platformMutation(c), c.Params("workspaceID"), request.Slug)
	if err != nil {
		if strings.Contains(err.Error(), "address") || strings.Contains(err.Error(), "not found") {
			return s.errMsg(c, fiber.StatusBadRequest, err.Error())
		}
		return s.errMsg(c, fiber.StatusInternalServerError, "workspace address could not be updated")
	}
	s.recordAdminAudit(c, "workspace.address.update", "workspace", workspace.ID, "ok", map[string]any{"slug": workspace.Slug})
	return c.JSON(fiber.Map{"workspace": workspace, "login_url": "/w/" + workspace.Slug})
}
