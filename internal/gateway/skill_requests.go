package gateway

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/skillstore"
	"github.com/soulacy/soulacy/internal/tenancy"
)

// handleCreateSkillInstallRequest lets a member ask for a skill from GitHub / URL
// without granting direct installation authority. An administrator reviews the
// safety report and approves or denies it.
func (s *Server) handleCreateSkillInstallRequest(c *fiber.Ctx) error {
	if s.skillRequests == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace skill requests are not available")
	}
	var body struct {
		SourceURL string `json:"source_url"`
		Reason    string `json:"reason"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	source := strings.TrimSpace(body.SourceURL)
	if source == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "source_url is required")
	}
	reason := strings.TrimSpace(body.Reason)
	if len(reason) > 2000 {
		return s.errMsg(c, fiber.StatusBadRequest, "request reason must be 2000 characters or fewer")
	}
	request := skillstore.InstallRequest{
		ID:          newSkillRequestID(),
		WorkspaceID: mcpWorkspace(c),
		SourceURL:   source,
		Reason:      reason,
		Status:      skillstore.RequestPending,
		RequestedBy: mcpRequestActor(c, s),
		RequestedAt: time.Now().UTC(),
	}
	if err := s.skillRequests.CreateInstallRequest(c.UserContext(), request); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.recordAdminAudit(c, "skill.install.request", "skill", request.ID, "ok", map[string]any{
		"source_url": source,
	})
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"ok":      true,
		"request": request,
		"message": "Skill installation request sent. A workspace owner or admin can now review and approve this skill.",
	})
}

// handleListSkillInstallRequests lists skill requests for the workspace.
func (s *Server) handleListSkillInstallRequests(c *fiber.Ctx) error {
	if s.skillRequests == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace skill requests are not available")
	}
	requester := ""
	if identity, ok := requestIdentity(c); ok && identity.Role() != tenancy.RoleOwner && identity.Role() != tenancy.RoleAdmin {
		requester = mcpRequestActor(c, s)
	}
	requests, err := s.skillRequests.ListInstallRequests(c.UserContext(), mcpWorkspace(c), requester)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if requests == nil {
		requests = []skillstore.InstallRequest{}
	}
	return c.JSON(fiber.Map{"requests": requests, "count": len(requests)})
}

// handleDenySkillInstallRequest denies a pending skill request.
func (s *Server) handleDenySkillInstallRequest(c *fiber.Ctx) error {
	if s.skillRequests == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace skill requests are not available")
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&body); err != nil {
			return s.errJSON(c, fiber.StatusBadRequest, err)
		}
	}
	if len(strings.TrimSpace(body.Reason)) > 2000 {
		return s.errMsg(c, fiber.StatusBadRequest, "decision reason must be 2000 characters or fewer")
	}
	if err := s.skillRequests.DecideInstallRequest(c.UserContext(), mcpWorkspace(c), c.Params("id"),
		skillstore.RequestDenied, mcpRequestActor(c, s), body.Reason, ""); err != nil {
		if err == sql.ErrNoRows {
			return s.errMsg(c, fiber.StatusNotFound, "pending skill request not found")
		}
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.recordAdminAudit(c, "skill.install.request.deny", "skill", c.Params("id"), "ok", map[string]any{"reason": strings.TrimSpace(body.Reason)})
	return c.JSON(fiber.Map{"ok": true, "message": "Skill installation request denied."})
}

// SetSkillRequestStore wires the skill requests store into the gateway.
func (s *Server) SetSkillRequestStore(store *skillstore.Store) {
	s.skillRequests = store
}

func (s *Server) workspaceSkillAdmin(c *fiber.Ctx) error {
	if !s.authorizationRequired() {
		return c.Next()
	}
	identity, ok := requestIdentity(c)
	if !ok || (identity.Role() != tenancy.RoleOwner && identity.Role() != tenancy.RoleAdmin) {
		return fiber.NewError(fiber.StatusForbidden, "workspace skill administration is not permitted")
	}
	return c.Next()
}

func newSkillRequestID() string {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return "sk_req_" + hex.EncodeToString(value)
}
