package gateway

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/authconnections"
	"github.com/soulacy/soulacy/internal/credentials"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/tenancy"
)

const (
	authConnectionSessionKey = "browser_storage_state"
	authConnectionRefreshKey = "oauth_refresh_token"
	authConnectionClientKey  = "oauth_client_secret"
	maxAuthSessionBytes      = 1024 * 1024
)

type authenticatedConnectionCreateRequest struct {
	Name           string     `json:"name"`
	Scope          string     `json:"scope"`
	Kind           string     `json:"kind"`
	BaseURL        string     `json:"base_url"`
	AllowedDomains []string   `json:"allowed_domains"`
	ExpiresAt      *time.Time `json:"expires_at"`
	AgentIDs       []string   `json:"agent_ids"`
}

func (s *Server) handleListAuthenticatedConnections(c *fiber.Ctx) error {
	if s.authConnections == nil || s.credVault == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "authenticated connections are not configured")
	}
	workspaceID, subject, _ := authenticatedConnectionActor(c)
	connections, err := s.authConnections.ListVisible(c.UserContext(), workspaceID, subject)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if connections == nil {
		connections = []authconnections.Connection{}
	}
	return c.JSON(fiber.Map{"connections": connections, "count": len(connections)})
}

func (s *Server) handleCreateAuthenticatedConnection(c *fiber.Ctx) error {
	if s.authConnections == nil || s.credVault == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "authenticated connections are not configured")
	}
	var body authenticatedConnectionCreateRequest
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body")
	}
	workspaceID, subject, role := authenticatedConnectionActor(c)
	body.Scope = strings.ToLower(strings.TrimSpace(body.Scope))
	if body.Scope == "" {
		body.Scope = authconnections.ScopeUser
	}
	if body.Scope != authconnections.ScopeUser && body.Scope != authconnections.ScopeWorkspace {
		return s.errMsg(c, fiber.StatusBadRequest, "scope must be user or workspace")
	}
	if body.Scope == authconnections.ScopeWorkspace && !isWorkspaceAdministrator(role) {
		return s.errMsg(c, fiber.StatusForbidden, "workspace connections require a workspace owner or admin")
	}
	body.Kind = strings.ToLower(strings.TrimSpace(body.Kind))
	if body.Kind != authconnections.KindBrowser && body.Kind != authconnections.KindOAuth {
		return s.errMsg(c, fiber.StatusBadRequest, "kind must be browser_session or oauth")
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > 120 {
		return s.errMsg(c, fiber.StatusBadRequest, "name must be between 1 and 120 characters")
	}
	baseURL, domains, err := normalizeConnectionBoundary(body.BaseURL, body.AllowedDomains)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	owner := subject
	if body.Scope == authconnections.ScopeWorkspace {
		owner = ""
	}
	connection, err := s.authConnections.Create(c.UserContext(), authconnections.CreateInput{
		WorkspaceID: workspaceID, OwnerSubject: owner, Scope: body.Scope, Kind: body.Kind,
		Name: body.Name, BaseURL: baseURL, AllowedDomains: domains, ExpiresAt: body.ExpiresAt,
	})
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if err := s.authConnections.ReplaceAgentGrants(c.UserContext(), workspaceID, connection.ID, body.AgentIDs); err != nil {
		_ = s.authConnections.Delete(c.UserContext(), workspaceID, connection.ID)
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	connection, _ = s.authConnections.Get(c.UserContext(), workspaceID, connection.ID)
	s.recordAdminAudit(c, "authenticated_connection.create", "credential", connection.ID, "ok", map[string]any{
		"scope": connection.Scope, "kind": connection.Kind, "allowed_domains": connection.AllowedDomains,
	})
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"connection": connection})
}

func (s *Server) handleSetAuthenticatedConnectionSession(c *fiber.Ctx) error {
	if s.authConnections == nil || s.credVault == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "authenticated connections are not configured")
	}
	workspaceID, subject, role := authenticatedConnectionActor(c)
	connection, err := s.authorizeAuthenticatedConnection(c, workspaceID, c.Params("id"), subject, role)
	if err != nil {
		return err
	}
	var body struct {
		StorageState json.RawMessage `json:"storage_state"`
		RefreshToken string          `json:"refresh_token"`
		ClientSecret string          `json:"client_secret"`
		ExpiresAt    *time.Time      `json:"expires_at"`
	}
	if len(c.Body()) > maxAuthSessionBytes {
		return s.errMsg(c, fiber.StatusRequestEntityTooLarge, "session state exceeds 1 MiB")
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body")
	}
	key := authConnectionSessionKey
	secret := []byte(body.StorageState)
	if connection.Kind == authconnections.KindBrowser {
		if err := validateBrowserStorageState(body.StorageState, connection.AllowedDomains); err != nil {
			return s.errMsg(c, fiber.StatusBadRequest, err.Error())
		}
	} else {
		key = authConnectionRefreshKey
		secret = []byte(strings.TrimSpace(body.RefreshToken))
		if len(secret) == 0 {
			return s.errMsg(c, fiber.StatusBadRequest, "refresh_token is required for an OAuth connection")
		}
	}
	if err := s.credVault.WriteBlob(c.UserContext(), workspaceID, authConnectionVaultNamespace(connection.ID), key, secret); err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "encrypted session could not be stored")
	}
	if connection.Kind == authconnections.KindOAuth && strings.TrimSpace(body.ClientSecret) != "" {
		if err := s.credVault.Set(c.UserContext(), workspaceID, authConnectionVaultNamespace(connection.ID), authConnectionClientKey, []byte(body.ClientSecret)); err != nil {
			_ = s.credVault.Delete(c.UserContext(), workspaceID, authConnectionVaultNamespace(connection.ID), key)
			return s.errMsg(c, fiber.StatusInternalServerError, "OAuth client secret could not be stored")
		}
	}
	if err := s.authConnections.MarkSecret(c.UserContext(), workspaceID, connection.ID, body.ExpiresAt); err != nil {
		_ = s.credVault.Delete(c.UserContext(), workspaceID, authConnectionVaultNamespace(connection.ID), key)
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	updated, _ := s.authConnections.Get(c.UserContext(), workspaceID, connection.ID)
	return c.JSON(fiber.Map{"connection": updated, "message": "Encrypted authentication state saved. Secret values will not be returned by the API."})
}

func (s *Server) handleSetAuthenticatedConnectionGrants(c *fiber.Ctx) error {
	if s.authConnections == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "authenticated connections are not configured")
	}
	workspaceID, subject, role := authenticatedConnectionActor(c)
	connection, err := s.authorizeAuthenticatedConnection(c, workspaceID, c.Params("id"), subject, role)
	if err != nil {
		return err
	}
	var body struct {
		AgentIDs []string `json:"agent_ids"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body")
	}
	if len(body.AgentIDs) > 100 {
		return s.errMsg(c, fiber.StatusBadRequest, "a connection may be granted to at most 100 agents")
	}
	if err := s.authConnections.ReplaceAgentGrants(c.UserContext(), workspaceID, connection.ID, body.AgentIDs); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	updated, _ := s.authConnections.Get(c.UserContext(), workspaceID, connection.ID)
	s.recordAdminAudit(c, "authenticated_connection.grants.update", "credential", connection.ID, "ok", map[string]any{"agent_ids": updated.AgentIDs})
	return c.JSON(fiber.Map{"connection": updated})
}

func (s *Server) handleRevokeAuthenticatedConnection(c *fiber.Ctx) error {
	if s.authConnections == nil || s.credVault == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "authenticated connections are not configured")
	}
	workspaceID, subject, role := authenticatedConnectionActor(c)
	connection, err := s.authorizeAuthenticatedConnection(c, workspaceID, c.Params("id"), subject, role)
	if err != nil {
		return err
	}
	deleteAuthenticatedConnectionSecrets(c, s.credVault, workspaceID, connection.ID)
	if err := s.authConnections.SetStatus(c.UserContext(), workspaceID, connection.ID, authconnections.StatusRevoked); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{"ok": true, "status": authconnections.StatusRevoked})
}

func (s *Server) handleDeleteAuthenticatedConnection(c *fiber.Ctx) error {
	if s.authConnections == nil || s.credVault == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "authenticated connections are not configured")
	}
	workspaceID, subject, role := authenticatedConnectionActor(c)
	connection, err := s.authorizeAuthenticatedConnection(c, workspaceID, c.Params("id"), subject, role)
	if err != nil {
		return err
	}
	deleteAuthenticatedConnectionSecrets(c, s.credVault, workspaceID, connection.ID)
	if err := s.authConnections.Delete(c.UserContext(), workspaceID, connection.ID); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *Server) authorizeAuthenticatedConnection(c *fiber.Ctx, workspaceID, id, subject, role string) (authconnections.Connection, error) {
	connection, err := s.authConnections.Get(c.UserContext(), workspaceID, id)
	if errors.Is(err, authconnections.ErrNotFound) {
		return connection, s.errMsg(c, fiber.StatusNotFound, "authenticated connection not found")
	}
	if err != nil {
		return connection, s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if connection.Scope == authconnections.ScopeUser && connection.OwnerSubject != subject {
		return connection, s.errMsg(c, fiber.StatusNotFound, "authenticated connection not found")
	}
	if connection.Scope == authconnections.ScopeWorkspace && !isWorkspaceAdministrator(role) {
		return connection, s.errMsg(c, fiber.StatusForbidden, "workspace connections can only be changed by a workspace owner or admin")
	}
	return connection, nil
}

func authenticatedConnectionActor(c *fiber.Ctx) (workspaceID, subject, role string) {
	workspaceID, subject, role = runtime.PersonalWorkspaceID, "usr_local_owner", tenancy.RoleOwner
	if identity, ok := requestIdentity(c); ok {
		workspaceID = runtime.NormalizeWorkspace(identity.WorkspaceID())
		if strings.TrimSpace(identity.Subject()) != "" {
			subject = strings.TrimSpace(identity.Subject())
		}
		if strings.TrimSpace(identity.Role()) != "" {
			role = strings.TrimSpace(identity.Role())
		}
	}
	return
}

func isWorkspaceAdministrator(role string) bool {
	return role == tenancy.RoleOwner || role == tenancy.RoleAdmin || role == rbac.RoleOwner || role == rbac.RoleAdmin
}

func authConnectionVaultNamespace(id string) string {
	return "authenticated_connection_" + strings.TrimSpace(id)
}

func deleteAuthenticatedConnectionSecrets(c *fiber.Ctx, vault credentials.Vault, workspaceID, id string) {
	for _, key := range []string{authConnectionSessionKey, authConnectionRefreshKey, authConnectionClientKey} {
		_ = vault.Delete(c.UserContext(), workspaceID, authConnectionVaultNamespace(id), key)
	}
}

type authenticatedConnectionSelection struct {
	workspaceID   string
	selectedIDs   []string
	manageableIDs []string
}

// prepareAuthenticatedConnectionSelection validates every connection named by
// a Studio draft before the agent file is written. User-owned sessions may be
// granted by their owner. Workspace sessions remain admin-controlled; a
// developer may reference one only when an admin already granted that agent.
func (s *Server) prepareAuthenticatedConnectionSelection(c *fiber.Ctx, agentID string, selectedIDs []string) (authenticatedConnectionSelection, error) {
	selection := authenticatedConnectionSelection{selectedIDs: uniqueConnectionIDs(selectedIDs)}
	if len(selection.selectedIDs) == 0 && s.authConnections == nil {
		return selection, nil
	}
	if s.authConnections == nil {
		return selection, s.errMsg(c, fiber.StatusServiceUnavailable, "authenticated connections are not configured")
	}
	workspaceID, subject, role := authenticatedConnectionActor(c)
	selection.workspaceID = workspaceID
	visible, err := s.authConnections.ListVisible(c.UserContext(), workspaceID, subject)
	if err != nil {
		return selection, s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	byID := make(map[string]authconnections.Connection, len(visible))
	for _, connection := range visible {
		byID[connection.ID] = connection
		if connection.Scope == authconnections.ScopeUser || isWorkspaceAdministrator(role) {
			selection.manageableIDs = append(selection.manageableIDs, connection.ID)
		}
	}
	for _, id := range selection.selectedIDs {
		connection, ok := byID[id]
		if !ok {
			return selection, s.errMsg(c, fiber.StatusBadRequest, "authenticated connection is unavailable: "+id)
		}
		if connection.Scope == authconnections.ScopeWorkspace && !isWorkspaceAdministrator(role) && !containsConnectionAgent(connection.AgentIDs, agentID) {
			return selection, s.errMsg(c, fiber.StatusForbidden, "a workspace owner or admin must grant connection "+connection.Name+" to this agent")
		}
	}
	return selection, nil
}

func (s *Server) applyAuthenticatedConnectionSelection(c *fiber.Ctx, agentID string, selection authenticatedConnectionSelection) error {
	if s.authConnections == nil {
		return nil
	}
	if err := s.authConnections.SyncAgentSelection(c.UserContext(), selection.workspaceID, agentID, selection.manageableIDs, selection.selectedIDs); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return nil
}

func uniqueConnectionIDs(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func containsConnectionAgent(values []string, agentID string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == strings.TrimSpace(agentID) {
			return true
		}
	}
	return false
}

func normalizeConnectionBoundary(rawURL string, rawDomains []string) (string, []string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", nil, errors.New("base_url is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return "", nil, errors.New("base_url must be an https URL")
	}
	if parsed.User != nil {
		return "", nil, errors.New("base_url must not contain credentials")
	}
	baseHost := normalizeDomain(parsed.Hostname())
	if isLocalOrPrivateHost(baseHost) {
		return "", nil, errors.New("private, loopback, and local domains are not allowed")
	}
	domains := append([]string(nil), rawDomains...)
	if len(domains) == 0 {
		domains = []string{baseHost}
	}
	seen := map[string]bool{}
	normalized := make([]string, 0, len(domains))
	for _, raw := range domains {
		domain := normalizeDomain(raw)
		if domain == "" || isLocalOrPrivateHost(domain) {
			return "", nil, errors.New("allowed_domains contains an invalid or private domain")
		}
		if !hostWithinBoundary(baseHost, domain) && !hostWithinBoundary(domain, baseHost) {
			return "", nil, errors.New("allowed_domains must share the base URL's domain boundary")
		}
		if !seen[domain] {
			seen[domain] = true
			normalized = append(normalized, domain)
		}
	}
	sort.Strings(normalized)
	parsed.Fragment = ""
	parsed.RawQuery = ""
	parsed.User = nil
	return parsed.String(), normalized, nil
}

func validateBrowserStorageState(raw json.RawMessage, allowed []string) error {
	if len(raw) == 0 || len(raw) > maxAuthSessionBytes {
		return errors.New("storage_state is required and must be no larger than 1 MiB")
	}
	var state struct {
		Cookies []struct {
			Name, Value, Domain, Path string
			Expires                   float64 `json:"expires"`
			HTTPOnly                  bool    `json:"httpOnly"`
			Secure                    bool    `json:"secure"`
			SameSite                  string  `json:"sameSite"`
		} `json:"cookies"`
		Origins []struct {
			Origin       string                         `json:"origin"`
			LocalStorage []struct{ Name, Value string } `json:"localStorage"`
		} `json:"origins"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return errors.New("storage_state must be valid Playwright storage-state JSON")
	}
	if len(state.Cookies) == 0 && len(state.Origins) == 0 {
		return errors.New("storage_state contains no cookies or local storage")
	}
	if len(state.Cookies) > 500 || len(state.Origins) > 50 {
		return errors.New("storage_state contains too many records")
	}
	for _, cookie := range state.Cookies {
		if strings.TrimSpace(cookie.Name) == "" || !domainAllowed(cookie.Domain, allowed) {
			return errors.New("storage_state contains a cookie outside allowed_domains")
		}
	}
	for _, origin := range state.Origins {
		parsed, err := url.Parse(origin.Origin)
		if err != nil || parsed.Scheme != "https" || !domainAllowed(parsed.Hostname(), allowed) {
			return errors.New("storage_state contains local storage outside allowed_domains")
		}
	}
	return nil
}

func domainAllowed(host string, allowed []string) bool {
	host = normalizeDomain(host)
	for _, boundary := range allowed {
		if hostWithinBoundary(host, normalizeDomain(boundary)) {
			return true
		}
	}
	return false
}

func normalizeDomain(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "*.")
	value = strings.TrimPrefix(value, ".")
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	return strings.TrimSuffix(value, ".")
}

func hostWithinBoundary(host, boundary string) bool {
	return host == boundary || strings.HasSuffix(host, "."+boundary)
}

func isLocalOrPrivateHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
	}
	return false
}
