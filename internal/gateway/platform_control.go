package gateway

import (
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
	return c.JSON(fiber.Map{
		"mode": s.config().DeploymentMode(), "version": config.Version,
		"timestamp": time.Now().UTC(), "summary": summary,
		"replica":        fiber.Map{"ready": ready, "status": readiness, "draining": s.Draining()},
		"dependencies":   dependencies,
		"authentication": fiber.Map{"status": authStatus, "mode": authMode, "detail": authDetail},
	})
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
		if strings.Contains(err.Error(), "required") {
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
		if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "not found") {
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
