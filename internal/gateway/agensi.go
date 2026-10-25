package gateway

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/netguard"
	"github.com/soulacy/soulacy/internal/secrets"
)

const (
	agensiMCPID             = "agensi"
	agensiMCPURL            = "https://mcp.agensi.io/mcp/auth"
	agensiResourceMetadata  = "https://mcp.agensi.io/.well-known/oauth-protected-resource/mcp/auth"
	agensiOAuthIssuer       = "https://mcp.agensi.io"
	agensiOAuthSecretRef    = "mcp.agensi.oauth_tokens"
	agensiOAuthCallbackPath = "/oauth/agensi/callback"
	agensiOAuthHTTPTimeout  = 20 * time.Second
)

var agensiScopes = []string{"read", "install"}

type mcpOAuthPending struct {
	ClientID    string
	Verifier    string
	RedirectURI string
	Resource    string
	TokenURL    string
	ExpiresAt   time.Time
}

func secureOAuthValue() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (s *Server) agensiStatus(ctx context.Context) fiber.Map {
	status := fiber.Map{
		"id": agensiMCPID, "name": "Agensi", "url": agensiMCPURL,
		"configured": false, "authenticated": false, "connected": false,
		"tools": []any{}, "fresh_on_every_use": true, "anonymous_access": false,
		"purchase_policy": "Always asks before any payment or credit purchase.",
	}
	if _, ok := secrets.New(s.CredentialVault()).Get(ctx, agensiOAuthSecretRef); ok {
		status["authenticated"] = true
	}
	if s.mcp == nil {
		status["detail"] = "MCP is unavailable."
		return status
	}
	for _, server := range s.mcp.ServersSnapshot() {
		if server.ID != agensiMCPID {
			continue
		}
		status["configured"] = true
		status["connected"] = server.Connected
		status["detail"] = server.Detail
		status["tools"] = server.Tools
		break
	}
	return status
}

func (s *Server) handleAgensiStatus(c *fiber.Ctx) error {
	return c.JSON(s.agensiStatus(c.UserContext()))
}

func (s *Server) agensiCallbackURL(c *fiber.Ctx) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(s.cfg.Server.PublicURL), "/")
	if base == "" {
		scheme := strings.TrimSpace(c.Protocol())
		host := strings.TrimSpace(c.Get(fiber.HeaderHost))
		if scheme == "" || host == "" {
			return "", fmt.Errorf("could not determine this gateway's callback address")
		}
		base = scheme + "://" + host
	}
	callback := base + agensiOAuthCallbackPath
	u, err := url.Parse(callback)
	if err != nil || u.User != nil || u.Hostname() == "" {
		return "", fmt.Errorf("could not build a safe OAuth callback address")
	}
	if u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return "", fmt.Errorf("agensi sign-in requires HTTPS unless Soulacy is opened on localhost")
	}
	return callback, nil
}

func (s *Server) handleAgensiConnect(c *fiber.Ctx) error {
	if s.cfgPath == "" || s.mcp == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "Agensi needs a writable Soulacy workspace with MCP enabled")
	}
	if !secrets.New(s.CredentialVault()).Enabled() {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "Agensi sign-in needs Soulacy's encrypted credential vault")
	}
	redirectURI, err := s.agensiCallbackURL(c)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	client := netguard.NewHTTPClient(agensiOAuthHTTPTimeout, true, nil)
	ctx, cancel := context.WithTimeout(c.UserContext(), agensiOAuthHTTPTimeout)
	defer cancel()
	resourceMetadata, err := mcp.FetchProtectedResourceMetadata(ctx, client, agensiResourceMetadata)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadGateway, "Could not read Agensi sign-in metadata: "+err.Error())
	}
	if resourceMetadata.Resource != agensiMCPURL || len(resourceMetadata.AuthorizationServers) != 1 || strings.TrimRight(resourceMetadata.AuthorizationServers[0], "/") != agensiOAuthIssuer {
		return s.errMsg(c, fiber.StatusBadGateway, "Agensi sign-in metadata did not match the official service")
	}
	authorizationMetadata, err := mcp.FetchAuthorizationServerMetadata(ctx, client, resourceMetadata.AuthorizationServers[0])
	if err != nil {
		return s.errMsg(c, fiber.StatusBadGateway, "Could not read Agensi authorization metadata: "+err.Error())
	}
	registration, err := mcp.RegisterOAuthClient(ctx, client, authorizationMetadata.RegistrationEndpoint, redirectURI, "Soulacy Personal")
	if err != nil {
		return s.errMsg(c, fiber.StatusBadGateway, "Could not register Soulacy with Agensi: "+err.Error())
	}
	state, err := secureOAuthValue()
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	verifier, err := secureOAuthValue()
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	authorizationURL, err := mcp.AuthorizationURL(authorizationMetadata, registration.ClientID, redirectURI, state, verifier, resourceMetadata.Resource, agensiScopes)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.mcpOAuthMu.Lock()
	for key, pending := range s.mcpOAuthPending {
		if time.Now().After(pending.ExpiresAt) {
			delete(s.mcpOAuthPending, key)
		}
	}
	// Bound pre-auth state even if a caller repeatedly starts and abandons the
	// browser flow. The oldest pending state is safe to discard and can simply
	// be restarted from the Skills screen.
	if len(s.mcpOAuthPending) >= 64 {
		var oldestKey string
		var oldestExpiry time.Time
		for key, pending := range s.mcpOAuthPending {
			if oldestKey == "" || pending.ExpiresAt.Before(oldestExpiry) {
				oldestKey, oldestExpiry = key, pending.ExpiresAt
			}
		}
		delete(s.mcpOAuthPending, oldestKey)
	}
	s.mcpOAuthPending[state] = mcpOAuthPending{
		ClientID: registration.ClientID, Verifier: verifier, RedirectURI: redirectURI,
		Resource: resourceMetadata.Resource, TokenURL: authorizationMetadata.TokenEndpoint,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	s.mcpOAuthMu.Unlock()
	return c.JSON(fiber.Map{
		"ok": true, "status": "sign_in_required", "authorization_url": authorizationURL,
		"message":    "Open the secure Agensi sign-in page, sign in, and click Allow. Soulacy will connect automatically.",
		"expires_in": 600,
	})
}

func (s *Server) handleAgensiOAuthCallback(c *fiber.Ctx) error {
	state := strings.TrimSpace(c.Query("state"))
	s.mcpOAuthMu.Lock()
	pending, ok := s.mcpOAuthPending[state]
	if ok {
		delete(s.mcpOAuthPending, state)
	}
	s.mcpOAuthMu.Unlock()
	if !ok || state == "" || time.Now().After(pending.ExpiresAt) {
		return agensiOAuthPage(c, fiber.StatusBadRequest, "Agensi sign-in expired", "Return to Soulacy Skills and start sign-in again.", false)
	}
	if denied := strings.TrimSpace(c.Query("error")); denied != "" {
		return agensiOAuthPage(c, fiber.StatusBadRequest, "Agensi did not connect", "The authorization was not completed. No Agensi tools were enabled.", false)
	}
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		return agensiOAuthPage(c, fiber.StatusBadRequest, "Agensi did not connect", "The authorization response did not include a code.", false)
	}
	client := netguard.NewHTTPClient(agensiOAuthHTTPTimeout, true, nil)
	ctx, cancel := context.WithTimeout(c.UserContext(), agensiOAuthHTTPTimeout)
	defer cancel()
	tokens, err := mcp.ExchangeAuthorizationCode(ctx, client, pending.TokenURL, code, pending.Verifier, pending.ClientID, pending.RedirectURI, pending.Resource)
	if err != nil {
		return agensiOAuthPage(c, fiber.StatusBadGateway, "Agensi did not connect", err.Error(), false)
	}
	encoded, _ := json.Marshal(tokens)
	if err := secrets.New(s.CredentialVault()).Set(ctx, agensiOAuthSecretRef, string(encoded)); err != nil {
		return agensiOAuthPage(c, fiber.StatusServiceUnavailable, "Agensi did not connect", "Soulacy could not save the encrypted OAuth session.", false)
	}
	body := mcpServerBody{
		ID: agensiMCPID, Transport: "http", URL: agensiMCPURL, PublicOnly: true,
		Auth: mcpAuthBody{Type: "oauth_authorization_code", SecretRef: agensiOAuthSecretRef,
			TokenURL: pending.TokenURL, ClientID: pending.ClientID, Scopes: agensiScopes, Resource: pending.Resource},
		Timeout: "60s",
	}
	if err := s.persistAgensiMCP(body); err != nil {
		_ = secrets.New(s.CredentialVault()).Delete(ctx, agensiOAuthSecretRef)
		return agensiOAuthPage(c, fiber.StatusInternalServerError, "Agensi did not connect", err.Error(), false)
	}
	if err := s.mcp.AddServer(agensiMCPID, mcpBodyToServerConfig(body)); err != nil {
		return agensiOAuthPage(c, fiber.StatusBadGateway, "Agensi sign-in saved", "Soulacy saved the secure session but could not load Agensi tools: "+err.Error(), false)
	}
	return agensiOAuthPage(c, fiber.StatusOK, "Agensi is connected", "You can close this page. Genie and System can now search and load Agensi skills.", true)
}

func (s *Server) persistAgensiMCP(body mcpServerBody) error {
	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return err
	}
	servers := getOrCreateMap(getOrCreateMap(raw, "mcp"), "servers")
	servers[agensiMCPID] = mcpServerToYAML(body)
	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return err
	}
	return nil
}

func (s *Server) handleAgensiSearch(c *fiber.Ctx) error {
	var request struct {
		Query string `json:"query"`
	}
	if err := c.BodyParser(&request); err != nil || strings.TrimSpace(request.Query) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "query is required")
	}
	if s.mcp == nil {
		return s.errMsg(c, fiber.StatusPreconditionFailed, "Sign in to Agensi first")
	}
	result, err := s.mcp.Call(c.UserContext(), "mcp__agensi__search_skills", map[string]any{"query": strings.TrimSpace(request.Query)})
	if err != nil {
		return s.errMsg(c, fiber.StatusBadGateway, err.Error())
	}
	var parsed any
	if json.Unmarshal([]byte(result), &parsed) != nil {
		parsed = result
	}
	return c.JSON(fiber.Map{"ok": true, "result": parsed})
}

func agensiOAuthPage(c *fiber.Ctx, status int, title, detail string, success bool) error {
	color := "#ef4444"
	if success {
		color = "#22c55e"
	}
	nonce, _ := secureOAuthValue()
	body := `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>` + html.EscapeString(title) + `</title><style nonce="` + nonce + `">body{margin:0;background:#0b1020;color:#eef2ff;font:16px system-ui;display:grid;place-items:center;min-height:100vh}.card{max-width:560px;margin:24px;padding:32px;border:1px solid #29324a;border-radius:18px;background:#12182a;box-shadow:0 24px 80px #0008}.dot{width:12px;height:12px;border-radius:50%;background:` + color + `;display:inline-block;margin-right:10px}h1{font-size:24px}p{color:#aab4cc;line-height:1.6}</style></head><body><main class="card"><h1><span class="dot"></span>` + html.EscapeString(title) + `</h1><p>` + html.EscapeString(detail) + `</p></main><script nonce="` + nonce + `">if(window.opener){window.opener.postMessage({type:'soulacy:agensi-oauth',success:` + fmt.Sprint(success) + `},window.location.origin)}</script></body></html>`
	c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set("Content-Security-Policy", "default-src 'none'; style-src 'nonce-"+nonce+"'; script-src 'nonce-"+nonce+"'; base-uri 'none'; frame-ancestors 'none'")
	c.Set("Referrer-Policy", "no-referrer")
	c.Set("X-Frame-Options", "DENY")
	return c.Status(status).SendString(body)
}
