package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/authconnections"
	"github.com/soulacy/soulacy/internal/rbac"
)

func TestAgentCreateAndUpdateSynchronizeAuthenticatedConnectionGrant(t *testing.T) {
	s := newTestGateway(t, "secret")
	store, err := authconnections.Open(filepath.Join(t.TempDir(), "connections.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.SetAuthenticatedConnectionStore(store)
	s.SetCredentialVault(newMemVault())

	status, created := gatewayJSON(t, s, http.MethodPost, "/api/v1/authenticated-connections", "secret", `{
		"name":"Research portal","scope":"user","kind":"browser_session",
		"base_url":"https://members.example.com/login","allowed_domains":["example.com"]
	}`)
	if status != http.StatusCreated {
		t.Fatalf("create connection status=%d body=%v", status, created)
	}
	id := created["connection"].(map[string]any)["id"].(string)

	agentBody := fmt.Sprintf(`{
		"id":"research-agent","name":"Research Agent","enabled":true,
		"trigger":"channel","system_prompt":"Research.","connections":[%q],
		"llm":{"provider":"openai","model":"gpt-4o-mini"},
		"memory":{"read_scopes":["session"],"write_scopes":["session"],"max_tokens":20},
		"max_turns":5
	}`, id)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", agentBody)
	if status != http.StatusCreated {
		t.Fatalf("create agent status=%d body=%v", status, body)
	}
	connection, err := store.Get(t.Context(), "personal", id)
	if err != nil {
		t.Fatal(err)
	}
	if len(connection.AgentIDs) != 1 || connection.AgentIDs[0] != "research-agent" {
		t.Fatalf("agent grants after create = %v", connection.AgentIDs)
	}

	agentWithoutConnection := `{
		"id":"research-agent","name":"Research Agent","enabled":true,
		"trigger":"channel","system_prompt":"Research.",
		"llm":{"provider":"openai","model":"gpt-4o-mini"},
		"memory":{"read_scopes":["session"],"write_scopes":["session"],"max_tokens":20},
		"max_turns":5
	}`
	status, body = gatewayJSON(t, s, http.MethodPut, "/api/v1/agents/research-agent", "secret", agentWithoutConnection)
	if status != http.StatusOK {
		t.Fatalf("update agent status=%d body=%v", status, body)
	}

	connection, err = store.Get(t.Context(), "personal", id)
	if err != nil {
		t.Fatal(err)
	}
	if len(connection.AgentIDs) != 0 {
		t.Fatalf("agent grants after update = %v", connection.AgentIDs)
	}
}

func TestAuthenticatedConnectionAPILifecycleNeverReturnsCookieValues(t *testing.T) {
	s := newTestGateway(t, "secret")
	store, err := authconnections.Open(filepath.Join(t.TempDir(), "connections.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.SetAuthenticatedConnectionStore(store)
	vault := newMemVault()
	s.SetCredentialVault(vault)

	status, created := gatewayJSON(t, s, http.MethodPost, "/api/v1/authenticated-connections", "secret", `{
		"name":"Research portal","scope":"user","kind":"browser_session",
		"base_url":"https://members.example.com/login","allowed_domains":["example.com"],
		"agent_ids":["research-agent"]
	}`)
	if status != http.StatusCreated {
		t.Fatalf("create status=%d body=%v", status, created)
	}
	connection := created["connection"].(map[string]any)
	id := connection["id"].(string)

	const cookieValue = "cookie-value-that-must-stay-secret"
	state := `{"storage_state":{"cookies":[{"name":"session","value":"` + cookieValue + `","domain":".example.com","path":"/","secure":true}],"origins":[]}}`
	status, setRaw := gatewayRaw(t, s, http.MethodPut, "/api/v1/authenticated-connections/"+id+"/session", "secret", state)
	if status != http.StatusOK {
		t.Fatalf("set session status=%d body=%s", status, setRaw)
	}
	if strings.Contains(setRaw, cookieValue) {
		t.Fatalf("set-session response leaked cookie: %s", setRaw)
	}

	status, listRaw := gatewayRaw(t, s, http.MethodGet, "/api/v1/authenticated-connections", "secret", "")
	if status != http.StatusOK || !strings.Contains(listRaw, `"has_secret":true`) {
		t.Fatalf("list status=%d body=%s", status, listRaw)
	}
	if strings.Contains(listRaw, cookieValue) {
		t.Fatalf("list response leaked cookie: %s", listRaw)
	}
	stored, err := vault.ReadBlob(t.Context(), authConnectionVaultNamespace(id), authConnectionSessionKey)
	if err != nil || !strings.Contains(string(stored), cookieValue) {
		t.Fatalf("encrypted-vault write missing: value=%s err=%v", stored, err)
	}
}

func TestPairedPhoneCanListAndRefreshWebsiteAccessWithoutCredentialAccess(t *testing.T) {
	s := newTestGateway(t, "secret")
	store, err := authconnections.Open(filepath.Join(t.TempDir(), "connections.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.SetAuthenticatedConnectionStore(store)
	s.SetCredentialVault(newMemVault())
	s.rbacManager = rbac.NewManager(rbac.NoopStore{}, zap.NewNop())

	status, created := gatewayJSON(t, s, http.MethodPost, "/api/v1/authenticated-connections", "secret", `{
		"name":"Research portal","scope":"user","kind":"browser_session",
		"base_url":"https://members.example.com/login","allowed_domains":["example.com"]
	}`)
	if status != http.StatusCreated {
		t.Fatalf("create status=%d body=%v", status, created)
	}
	id := created["connection"].(map[string]any)["id"].(string)

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{
			Role: "operator", Kind: "access", Scopes: companionScopes(),
			RegisteredClaims: jwt.RegisteredClaims{Subject: "admin"},
		})
		return c.Next()
	})
	api := app.Group("/api/v1")
	api.Get("/authenticated-connections", s.rbacMW(rbac.ResourceWebsiteAccess, rbac.ActionList), s.handleListAuthenticatedConnections)
	api.Put("/authenticated-connections/:id/session", s.rbacMW(rbac.ResourceWebsiteAccess, rbac.ActionSet), s.handleSetAuthenticatedConnectionSession)
	api.Delete("/authenticated-connections/:id", s.rbacMW(rbac.ResourceWebsiteAccess, rbac.ActionDelete), s.handleDeleteAuthenticatedConnection)

	if code, body := doJSON(t, app, http.MethodGet, "/api/v1/authenticated-connections", ""); code != http.StatusOK {
		t.Fatalf("paired phone list: %d %+v", code, body)
	}
	state := `{"storage_state":{"cookies":[{"name":"session","value":"opaque","domain":".example.com","path":"/","secure":true}],"origins":[]}}`
	if code, body := doJSON(t, app, http.MethodPut, "/api/v1/authenticated-connections/"+id+"/session", state); code != http.StatusOK {
		t.Fatalf("paired phone refresh: %d %+v", code, body)
	}
	if code, body := doJSON(t, app, http.MethodDelete, "/api/v1/authenticated-connections/"+id, ""); code != http.StatusForbidden || body["required"] != "website_access:delete" {
		t.Fatalf("paired phone delete should remain denied: %d %+v", code, body)
	}
	if (&auth.Claims{Role: "operator", Scopes: companionScopes()}).Allows(rbac.ResourceCredentials, rbac.ActionList) {
		t.Fatal("paired phone website access scopes exposed general credentials")
	}
}

func TestNormalizeConnectionBoundary(t *testing.T) {
	base, domains, err := normalizeConnectionBoundary("https://members.hbr.org/login?next=/home#form", []string{"hbr.org", ".HBR.org"})
	if err != nil {
		t.Fatal(err)
	}
	if base != "https://members.hbr.org/login" {
		t.Fatalf("base = %q", base)
	}
	if len(domains) != 1 || domains[0] != "hbr.org" {
		t.Fatalf("domains = %v", domains)
	}
	for _, test := range []struct {
		name, raw string
		domains   []string
	}{
		{"plaintext", "http://hbr.org/login", nil},
		{"credentials", "https://user:pass@hbr.org/login", nil},
		{"loopback", "https://127.0.0.1/login", nil},
		{"local", "https://app.local/login", nil},
		{"foreign boundary", "https://hbr.org/login", []string{"evil.test"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := normalizeConnectionBoundary(test.raw, test.domains); err == nil {
				t.Fatal("expected boundary validation failure")
			}
		})
	}
}

func TestValidateBrowserStorageStateDomainConfinement(t *testing.T) {
	valid := json.RawMessage(`{"cookies":[{"name":"session","value":"opaque","domain":".hbr.org","path":"/"}],"origins":[{"origin":"https://members.hbr.org","localStorage":[{"name":"state","value":"opaque"}]}]}`)
	if err := validateBrowserStorageState(valid, []string{"hbr.org"}); err != nil {
		t.Fatalf("valid state rejected: %v", err)
	}
	parentDomainSSO := json.RawMessage(`{"cookies":[{"name":"SID","value":"opaque","domain":".google.com","path":"/"}],"origins":[]}`)
	if err := validateBrowserStorageState(parentDomainSSO, []string{"notebook.google.com"}); err != nil {
		t.Fatalf("parent-domain SSO cookie rejected: %v", err)
	}
	foreignCookie := json.RawMessage(`{"cookies":[{"name":"session","value":"opaque","domain":"evil.test"}],"origins":[]}`)
	if err := validateBrowserStorageState(foreignCookie, []string{"hbr.org"}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("foreign cookie error = %v", err)
	}
	foreignOrigin := json.RawMessage(`{"cookies":[],"origins":[{"origin":"https://evil.test","localStorage":[{"name":"state","value":"opaque"}]}]}`)
	if err := validateBrowserStorageState(foreignOrigin, []string{"hbr.org"}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("foreign origin error = %v", err)
	}
}
