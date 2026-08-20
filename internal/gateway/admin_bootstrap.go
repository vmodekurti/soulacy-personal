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

func (s *Server) handleAdminBootstrapState(c *fiber.Ctx) error {
	if !config.IsMultiUserMode(s.config().DeploymentMode()) {
		return s.errMsg(c, fiber.StatusConflict, "first-owner setup is only used in Team and Scale modes")
	}
	if s.tenantBootstrap == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "tenant bootstrap store is unavailable")
	}
	state, err := s.tenantBootstrap.BootstrapState(c.UserContext())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "tenant bootstrap state could not be read")
	}
	return c.JSON(fiber.Map{"mode": s.config().DeploymentMode(), "state": state})
}

func (s *Server) handleAdminBootstrap(c *fiber.Ctx) error {
	if !config.IsMultiUserMode(s.config().DeploymentMode()) {
		return s.errMsg(c, fiber.StatusConflict, "first-owner setup is only used in Team and Scale modes")
	}
	if s.tenantBootstrap == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "tenant bootstrap store is unavailable")
	}
	var req tenancy.BootstrapRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid setup request")
	}
	requestID := strings.TrimSpace(c.Get("X-Request-ID"))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	result, err := s.tenantBootstrap.BootstrapFirstOwner(c.UserContext(), tenancy.Mutation{ActorSubject: "platform:bootstrap", RequestID: requestID, At: time.Now().UTC()}, req)
	switch {
	case err == nil:
		s.recordAdminAudit(c, "tenant.bootstrap", "organization", result.Organization.ID, "ok", map[string]any{"workspace_id": result.Workspace.ID, "owner_user_id": result.User.ID})
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"ok": true, "result": result})
	case errors.Is(err, tenancy.ErrAlreadyBootstrapped):
		return s.errMsg(c, fiber.StatusConflict, "the first owner already exists; sign in with your organization")
	case errors.Is(err, tenancy.ErrBootstrapBlocked):
		return s.errMsg(c, fiber.StatusConflict, "tenant data is partially initialized; repair it before running first-owner setup")
	case strings.Contains(err.Error(), "required"):
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	default:
		return s.errMsg(c, fiber.StatusInternalServerError, "first-owner setup failed")
	}
}
