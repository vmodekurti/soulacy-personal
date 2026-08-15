package gateway

import (
	"encoding/json"
	"fmt"
	"github.com/soulacy/soulacy/internal/wsroot"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/secrets"
	"go.uber.org/zap"
)

func TestRBACMiddlewareWiredAfterRouteConstructionStillEnforces(t *testing.T) {
	s := &Server{}
	middleware := s.rbacMW(rbac.ResourceSecrets, rbac.ActionList)
	s.rbacManager = rbac.NewManager(rbac.NoopStore{}, zap.NewNop())
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{Role: rbac.RoleOperator, Kind: "access"})
		return c.Next()
	})
	app.Get("/", middleware, func(c *fiber.Ctx) error { return c.SendStatus(http.StatusNoContent) })
	resp, err := app.Test(mustRequest(t, http.MethodGet, "/"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("late-wired RBAC status = %d, want 403", resp.StatusCode)
	}
}

func TestCredentialRevealRequiresConfirmationAndIsAudited(t *testing.T) {
	s := newTestGateway(t, "secret")
	vault := newMemVault()
	if err := vault.Set(t.Context(), wsroot.PersonalWorkspaceID, "agent-a", "token", []byte("top-secret-value")); err != nil {
		t.Fatal(err)
	}
	s.SetCredentialVault(vault)
	backend := &fakeTailBackend{}
	s.actions = backend

	reveal := func(confirm bool) (int, map[string]any) {
		req := mustRequest(t, http.MethodGet, "/api/v1/credentials/agent-a/token")
		req.Header.Set("Authorization", "Bearer secret")
		if confirm {
			req.Header.Set("X-Soulacy-Confirm-Credential-Reveal", "true")
		}
		resp, err := s.app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}
	if status, _ := reveal(false); status != http.StatusPreconditionRequired {
		t.Fatalf("unconfirmed reveal status = %d", status)
	}
	status, body := reveal(true)
	if status != http.StatusOK || body["value"] == nil {
		t.Fatalf("confirmed reveal = %d %v", status, body)
	}

	events, err := backend.QueryEvents(adminAuditAgentID, "", 10, adminAuditEventTypes())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("reveal audit events = %d, want 2", len(events))
	}
	for _, event := range events {
		if strings.Contains(fmt.Sprint(event.Payload), "top-secret-value") {
			t.Fatalf("audit event leaked credential: %#v", event.Payload)
		}
	}
}

func TestPerAgentCredentialAPIHidesGlobalSecretScope(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.SetCredentialVault(newMemVault())
	status, _ := gatewayJSON(t, s, http.MethodGet, "/api/v1/credentials/"+secrets.GlobalScope, "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("global scope via credential API status = %d, want 404", status)
	}
}

func mustRequest(t *testing.T, method, path string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}
