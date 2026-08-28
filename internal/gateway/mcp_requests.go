package gateway

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/mcpstore"
	"github.com/soulacy/soulacy/internal/tenancy"
)

// handleCreateMCPInstallRequest lets a developer ask for third-party code
// without granting any installation authority. The administrator later runs
// the ordinary immutable-source security review themselves.
func (s *Server) handleCreateMCPInstallRequest(c *fiber.Ctx) error {
	if s.mcpServers == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace MCP requests are not available")
	}
	var body struct {
		SourceURL   string                `json:"source_url"`
		Reason      string                `json:"reason"`
		Permissions mcpInstallPermissions `json:"permissions"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	source, err := canonicalGitHubRepository(body.SourceURL)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	permissions, err := normalizeMCPInstallPermissions(body.Permissions)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	reason := strings.TrimSpace(body.Reason)
	if len(reason) > 2000 {
		return s.errMsg(c, fiber.StatusBadRequest, "request reason must be 2000 characters or fewer")
	}
	request := mcpstore.InstallRequest{
		ID:              newMCPRequestID(),
		WorkspaceID:     mcpWorkspace(c),
		SourceURL:       source,
		Reason:          reason,
		Network:         permissions.Network,
		WorkspaceAccess: permissions.Workspace,
		Status:          mcpstore.RequestPending,
		RequestedBy:     mcpRequestActor(c, s),
		RequestedAt:     time.Now().UTC(),
	}
	if err := s.mcpServers.CreateInstallRequest(c.UserContext(), request); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.recordAdminAudit(c, "mcp.install.request", "mcp", request.ID, "ok", map[string]any{
		"source_url": source, "network": request.Network, "workspace_access": request.WorkspaceAccess,
	})
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"ok": true, "request": request,
		"message": "Request sent. A workspace owner or admin can now review and install this MCP server.",
	})
}

func (s *Server) handleListMCPInstallRequests(c *fiber.Ctx) error {
	if s.mcpServers == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace MCP requests are not available")
	}
	requester := ""
	if identity, ok := requestIdentity(c); ok && identity.Role() != tenancy.RoleOwner && identity.Role() != tenancy.RoleAdmin {
		requester = mcpRequestActor(c, s)
	}
	requests, err := s.mcpServers.ListInstallRequests(c.UserContext(), mcpWorkspace(c), requester)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if requests == nil {
		requests = []mcpstore.InstallRequest{}
	}
	return c.JSON(fiber.Map{"requests": requests, "count": len(requests)})
}

func (s *Server) handleDenyMCPInstallRequest(c *fiber.Ctx) error {
	if s.mcpServers == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace MCP requests are not available")
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
	if err := s.mcpServers.DecideInstallRequest(c.UserContext(), mcpWorkspace(c), c.Params("id"),
		mcpstore.RequestDenied, mcpRequestActor(c, s), body.Reason, ""); err != nil {
		if err == sql.ErrNoRows {
			return s.errMsg(c, fiber.StatusNotFound, "pending MCP request not found")
		}
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.recordAdminAudit(c, "mcp.install.request.deny", "mcp", c.Params("id"), "ok", map[string]any{"reason": strings.TrimSpace(body.Reason)})
	return c.JSON(fiber.Map{"ok": true, "message": "MCP installation request denied."})
}

func mcpRequestActor(c *fiber.Ctx, s *Server) string {
	if identity, ok := requestIdentity(c); ok && strings.TrimSpace(identity.Subject()) != "" {
		return strings.TrimSpace(identity.Subject())
	}
	return s.auditActor(c)
}

func newMCPRequestID() string {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return "mcp_req_" + hex.EncodeToString(value)
}
