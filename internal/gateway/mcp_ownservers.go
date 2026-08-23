package gateway

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/mcpstore"
	"github.com/soulacy/soulacy/internal/netguard"
	"github.com/soulacy/soulacy/internal/redact"
	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// mcp_ownservers.go — a workspace's own MCP servers.
//
// The operator's catalog lives in config.yaml behind the platform credential.
// This is the other half: a tenant defining a server the operator never
// published, durable across restarts, visible to nobody else.
//
// SECRETS DO NOT GO IN THE STORE. A submitted env value or header whose key
// looks like a credential is diverted into the per-workspace vault, under the
// same namespace the operator's template servers use — so a tenant sets a
// credential in one place whether the server is theirs or the operator's, and
// the SQLite file on the gateway's disk never holds one. The definition keeps
// the KEY so the pool still knows the server wants it.

// SetMCPServerStore wires the durable per-workspace server registry.
func (s *Server) SetMCPServerStore(store *mcpstore.Store) {
	s.mcpServers = store
	if s.mcpPool != nil && store != nil {
		s.mcpPool.SetServerStore(mcp.ServerStoreFunc(s.workspaceOwnedServers))
	}
}

// workspaceOwnedServers resolves one workspace's stored servers for the pool.
//
// Errors return nothing rather than falling back to anything shared: a store
// that cannot be read means "this workspace has no servers of its own", which
// costs the tenant a tool, where the alternative would hand them somebody
// else's.
func (s *Server) workspaceOwnedServers(workspaceID string) map[string]mcp.ServerConfig {
	if s == nil || s.mcpServers == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), vaultReadTimeout)
	defer cancel()
	stored, err := s.mcpServers.List(ctx, workspaceID)
	if err != nil {
		s.log.Warn("workspace MCP servers could not be read",
			zap.String("workspace_id", workspaceID), zap.Error(err))
		return nil
	}
	out := make(map[string]mcp.ServerConfig, len(stored))
	for _, server := range stored {
		publicRemote := s.authorizationRequired()
		if publicRemote && !safeWorkspaceRemoteDefinition(server.Transport, server.URL, server.Command, server.Args, server.Env, server.InheritEnv) {
			s.log.Warn("unsafe legacy workspace MCP definition withheld",
				zap.String("workspace_id", workspaceID), zap.String("server_id", server.ID))
			continue
		}
		out[server.ID] = mcp.ServerConfig{
			Transport:    server.Transport,
			Command:      server.Command,
			Args:         server.Args,
			Env:          s.fillSecrets(workspaceID, server.ID, server.Env),
			URL:          server.URL,
			Headers:      s.fillSecrets(workspaceID, server.ID, server.Headers),
			InheritEnv:   server.InheritEnv,
			PublicRemote: publicRemote,
		}
	}
	return out
}

// fillSecrets restores the credential values that were diverted to the vault
// on write. A key with no stored value is left empty rather than dropped: the
// server still declares that it wants it, and GET /mcp/:id/credentials is how
// the tenant sees which are outstanding.
func (s *Server) fillSecrets(workspaceID, serverID string, values map[string]string) map[string]string {
	if len(values) == 0 {
		return values
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		if !redact.SecretKeyName(key) {
			out[key] = value
			continue
		}
		if own, ok := s.vaultServerSecret(workspaceID, serverID, key); ok {
			out[key] = own
			continue
		}
		out[key] = ""
	}
	return out
}

type ownServerBody struct {
	Transport  string            `json:"transport"`
	Command    string            `json:"command"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env"`
	URL        string            `json:"url"`
	Headers    map[string]string `json:"headers"`
	InheritEnv []string          `json:"inherit_env"`
}

func safeWorkspaceRemoteDefinition(transport, rawURL, command string, args []string, env map[string]string, inheritEnv []string) bool {
	t := strings.ToLower(strings.TrimSpace(transport))
	if t == "container" {
		// Container definitions are written only by the approval endpoint; the
		// generic PUT policy below rejects this transport. Re-check the durable
		// row on every load so a legacy/manual row cannot become host execution.
		return immutableOCIReference.MatchString(strings.TrimSpace(command)) && rawURL == "" &&
			len(inheritEnv) == 0 && len(env) == 0
	}
	if t != "http" && t != "https" {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	return err == nil && strings.EqualFold(u.Scheme, "https") && u.Hostname() != "" && u.User == nil && u.Fragment == "" &&
		strings.TrimSpace(command) == "" && len(args) == 0 && len(env) == 0 && len(inheritEnv) == 0
}

// workspaceMCPAdmin limits changes to the two roles that administer a
// workspace. Personal mode preserves its single-user compatibility path.
func (s *Server) workspaceMCPAdmin(c *fiber.Ctx) error {
	if !s.authorizationRequired() {
		return c.Next()
	}
	identity, ok := requestIdentity(c)
	if !ok || (identity.Role() != tenancy.RoleOwner && identity.Role() != tenancy.RoleAdmin) {
		return fiber.NewError(fiber.StatusForbidden, "workspace MCP administration is not permitted")
	}
	return c.Next()
}

func (s *Server) validateOwnMCPPolicy(body ownServerBody) error {
	if !s.authorizationRequired() {
		return nil
	}
	if !safeWorkspaceRemoteDefinition(body.Transport, body.URL, body.Command, body.Args, body.Env, body.InheritEnv) {
		return fiber.NewError(fiber.StatusBadRequest,
			"Team and Scale workspaces may enable only remote HTTPS MCP servers; command, args, env, and inherit_env are not permitted")
	}
	if err := netguard.CheckPublic(body.URL); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "workspace MCP URL is not a public destination: "+err.Error())
	}
	return nil
}

// handlePutOwnMCPServer creates or replaces one of this workspace's servers.
//
// PUT /api/v1/mcp/own/:id — a workspace route. Writing the operator's template
// is behind platformMW; this writes only rows carrying this workspace's id.
func (s *Server) handlePutOwnMCPServer(c *fiber.Ctx) error {
	if s.mcpServers == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace MCP servers are not available")
	}
	id := strings.TrimSpace(c.Params("id"))
	if !validMCPID(id) {
		return s.errMsg(c, fiber.StatusBadRequest, "id may contain only letters, digits, '-' and '_'")
	}
	var body ownServerBody
	if err := c.BodyParser(&body); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if msg := validateMCPServer(mcpServerBody{
		ID: id, Transport: body.Transport, Command: body.Command, Args: body.Args,
		Env: body.Env, URL: body.URL, Headers: body.Headers,
	}); msg != "" {
		return s.errMsg(c, fiber.StatusBadRequest, msg)
	}
	if err := s.validateOwnMCPPolicy(body); err != nil {
		return err
	}

	workspaceID := mcpWorkspace(c)
	env, envSecrets, err := s.divertSecrets(c.UserContext(), workspaceID, id, body.Env)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	headers, headerSecrets, err := s.divertSecrets(c.UserContext(), workspaceID, id, body.Headers)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}

	if err := s.mcpServers.Put(c.UserContext(), mcpstore.Server{
		WorkspaceID: workspaceID, ID: id,
		Transport: body.Transport, Command: body.Command, Args: body.Args,
		Env: env, URL: body.URL, Headers: headers, InheritEnv: body.InheritEnv,
		CreatedBy: s.auditActor(c),
	}); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.invalidateMCPWorkspace(workspaceID)
	s.recordAdminAudit(c, "mcp.own.put", "mcp", id, "ok",
		map[string]any{"secrets_stored": envSecrets + headerSecrets})
	return c.JSON(fiber.Map{"ok": true, "id": id, "secrets_stored": envSecrets + headerSecrets})
}

// handleDeleteOwnMCPServer removes one of this workspace's servers.
func (s *Server) handleDeleteOwnMCPServer(c *fiber.Ctx) error {
	if s.mcpServers == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace MCP servers are not available")
	}
	id := strings.TrimSpace(c.Params("id"))
	if !validMCPID(id) {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid server id")
	}
	workspaceID := mcpWorkspace(c)
	if err := s.mcpServers.Delete(c.UserContext(), workspaceID, id); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.invalidateMCPWorkspace(workspaceID)
	s.recordAdminAudit(c, "mcp.own.delete", "mcp", id, "ok", nil)
	return c.JSON(fiber.Map{"ok": true, "id": id})
}

// handleListOwnMCPServers lists this workspace's own servers.
//
// DELIBERATELY NOT MASKED, unlike the deployment-wide list. That one masks
// because config.yaml genuinely holds the operator's tokens and is readable by
// anyone with mcp:read, including a viewer. These rows cannot hold a
// credential — divertSecrets moved every secret-named value to the vault
// before the row was written, and TestASubmittedTokenIsDivertedToTheVaultNotTheDatabase
// pins that.
//
// Masking anyway would replace `LOG_LEVEL: debug` with `***` for the tenant
// who typed it, which hides their own configuration and buys nothing. A mask
// that protects nothing still teaches people the mask means something.
func (s *Server) handleListOwnMCPServers(c *fiber.Ctx) error {
	if s.mcpServers == nil {
		return c.JSON(fiber.Map{"servers": []any{}})
	}
	stored, err := s.mcpServers.List(c.UserContext(), mcpWorkspace(c))
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if stored == nil {
		stored = []mcpstore.Server{}
	}
	statusByID := map[string]mcp.ServerStatus{}
	if client := s.mcpFor(c); client != nil {
		for _, status := range client.ServersSnapshot() {
			statusByID[status.ID] = status
		}
	}
	servers := make([]fiber.Map, 0, len(stored))
	for _, server := range stored {
		status := statusByID[server.ID]
		servers = append(servers, fiber.Map{
			"id": server.ID, "transport": server.Transport, "command": server.Command,
			"args": server.Args, "env": server.Env, "url": server.URL, "headers": server.Headers,
			"inherit_env": server.InheritEnv, "connected": status.Connected,
			"detail": status.Detail, "tools": status.Tools,
		})
	}
	return c.JSON(fiber.Map{"servers": servers})
}

// divertSecrets moves credential-looking values into the vault and returns the
// map with those values blanked, plus how many were stored.
//
// The KEY is kept. Dropping it would lose the fact that the server wants that
// credential, and the tenant would have no way to see what is outstanding.
func (s *Server) divertSecrets(ctx context.Context, workspaceID, serverID string,
	values map[string]string) (map[string]string, int, error) {
	if len(values) == 0 {
		return values, 0, nil
	}
	out := make(map[string]string, len(values))
	stored := 0
	for key, value := range values {
		if !redact.SecretKeyName(key) {
			out[key] = value
			continue
		}
		out[key] = ""
		if strings.TrimSpace(value) == "" {
			continue
		}
		if s.credVault == nil {
			return nil, 0, errors.New("credential vault is unavailable; a server secret cannot be stored safely")
		}
		if err := s.credVault.Set(ctx, wsroot.Normalize(workspaceID),
			mcp.CredentialNamespace(serverID), key, []byte(value)); err != nil {
			return nil, 0, err
		}
		stored++
	}
	return out, stored, nil
}
