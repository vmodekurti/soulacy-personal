package gateway

import (
	"context"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/plugins"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// mcp_credentials.go — each workspace's own identity to each MCP server.
//
// The operator publishes a CATALOG of servers in config.yaml: what to run,
// with what arguments, and which credentials it needs. Each workspace supplies
// its own values for those credentials, and gets its own subprocess running
// under its own identity. The operator's token never leaves the operator's
// deployment-level config — see internal/mcp/tenantcreds.go for what happened
// when it did.
//
// The values live in the per-workspace credential vault rather than anywhere
// new. It already encrypts under a per-workspace data key, so two tenants'
// ciphertext is unreadable to each other even if a query loses its predicate —
// which is a stronger boundary than a column filter and is the reason not to
// invent a second store here.

// vaultServerSecret resolves one workspace's value for one server credential.
//
// Errors resolve to "not supplied" rather than to the operator's value. A
// vault that is unreachable must withhold the server, not fall back — falling
// back is precisely the leak, and it would appear exactly when the vault is
// broken and nobody is looking at MCP.
// vaultReadTimeout bounds a single credential lookup.
//
// A NAMED constant rather than a literal, because the guard in
// internal/runtime asks for exactly that: a deadline somebody chose and can be
// reviewed. It is deliberately NOT part of the run/step/LLM hierarchy — that
// hierarchy budgets a user's request, and this is an infrastructure read on
// the way to starting a subprocess. Tying it to the run budget would make a
// long-running agent wait longer for a local database than a short one does.
const vaultReadTimeout = 5 * time.Second

func (s *Server) vaultServerSecret(workspaceID, serverID, key string) (string, bool) {
	if s == nil || s.credVault == nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), vaultReadTimeout)
	defer cancel()
	value, err := s.credVault.Get(ctx, wsroot.Normalize(workspaceID),
		mcp.CredentialNamespace(serverID), key)
	if err != nil || len(value) == 0 {
		return "", false
	}
	return string(value), true
}

type mcpCredentialBody struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// handleSetMCPCredential stores one workspace's value for one server secret.
//
// PUT /api/v1/mcp/:id/credentials — a WORKSPACE route, deliberately. Writing
// the server definition is the operator's job and lives behind platformMW;
// supplying the credential the definition asks for is the tenant's, and is the
// only reason the catalog is usable by anyone but the operator.
func (s *Server) handleSetMCPCredential(c *fiber.Ctx) error {
	if s.credVault == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "credential vault is not available")
	}
	serverID := strings.TrimSpace(c.Params("id"))
	if !validMCPID(serverID) {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid server id")
	}
	var body mcpCredentialBody
	if err := c.BodyParser(&body); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	key := strings.TrimSpace(body.Key)
	if key == "" || strings.TrimSpace(body.Value) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "key and value are required")
	}

	workspaceID := mcpWorkspace(c)
	if err := s.credVault.Set(c.UserContext(), workspaceID, mcp.CredentialNamespace(serverID), key,
		[]byte(body.Value)); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	// The client is rebuilt so the tenant sees the server appear. Without
	// this they set the token, nothing changes, and they conclude it is
	// broken — which is how a correct security control gets turned off.
	s.invalidateMCPWorkspace(workspaceID)
	s.log.Info("mcp credential set for workspace",
		zap.String("workspace_id", workspaceID), zap.String("server", serverID), zap.String("key", key))
	s.recordAdminAudit(c, "mcp.credential.set", "mcp", serverID, "ok", map[string]any{"key": key})
	return c.JSON(fiber.Map{"ok": true, "server_id": serverID, "key": key})
}

// handleDeleteMCPCredential removes one workspace's value, which withholds the
// server again rather than falling back to the operator's.
func (s *Server) handleDeleteMCPCredential(c *fiber.Ctx) error {
	if s.credVault == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "credential vault is not available")
	}
	serverID := strings.TrimSpace(c.Params("id"))
	key := strings.TrimSpace(c.Params("key"))
	if !validMCPID(serverID) || key == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid server id or key")
	}
	workspaceID := mcpWorkspace(c)
	if err := s.credVault.Delete(c.UserContext(), workspaceID, mcp.CredentialNamespace(serverID), key); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.invalidateMCPWorkspace(workspaceID)
	s.recordAdminAudit(c, "mcp.credential.delete", "mcp", serverID, "ok", map[string]any{"key": key})
	return c.JSON(fiber.Map{"ok": true, "server_id": serverID, "key": key})
}

// handleListMCPCredentials names which credentials this workspace has supplied
// for one server. Names only — the vault returns values to nobody here.
func (s *Server) handleListMCPCredentials(c *fiber.Ctx) error {
	if s.credVault == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "credential vault is not available")
	}
	serverID := strings.TrimSpace(c.Params("id"))
	if !validMCPID(serverID) {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid server id")
	}
	keys, err := s.credVault.List(c.UserContext(), mcpWorkspace(c), mcp.CredentialNamespace(serverID))
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if keys == nil {
		keys = []string{}
	}
	return c.JSON(fiber.Map{"server_id": serverID, "keys": keys})
}

// handleMCPPending lists the servers this workspace cannot start yet and the
// credentials each still needs.
//
// A tenant whose tool simply is not there has no way to tell a withheld server
// from a broken one. This is what makes the withholding a setup step.
func (s *Server) handleMCPPending(c *fiber.Ctx) error {
	if s.mcpPool == nil {
		return c.JSON(fiber.Map{"pending": []any{}})
	}
	pending := s.mcpPool.Withheld(mcpWorkspace(c))
	if pending == nil {
		pending = []mcp.WithheldServer{}
	}
	return c.JSON(fiber.Map{"pending": pending})
}

// invalidateMCPWorkspace drops the workspace's client so the next call rebuilds
// it with the credential that just changed.
func (s *Server) invalidateMCPWorkspace(workspaceID string) {
	if s.mcpPool != nil {
		s.mcpPool.InvalidateWorkspace(workspaceID)
	}
}

// handlePluginSettingsPending lists shared plugin settings this workspace did
// not receive, and what to do about each.
//
// Same reason the MCP one exists: a plugin quietly missing a value it expects
// is a failure nobody traces back to a security control. The remedy names the
// manifest's `credentials:` section, because the real fix is the plugin
// declaring the secret rather than the operator sharing it.
func (s *Server) handlePluginSettingsPending(c *fiber.Ctx) error {
	if s.pluginStores == nil {
		return c.JSON(fiber.Map{"withheld": []any{}, "detail": ""})
	}
	items := s.pluginStores.WithheldSettings(mcpWorkspace(c))
	if items == nil {
		items = []plugins.WithheldSetting{}
	}
	return c.JSON(fiber.Map{
		"withheld": items,
		"detail":   plugins.WithheldSettingsMessage(items),
	})
}
