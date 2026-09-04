package rbac

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/auth"
	"go.uber.org/zap"
)

type objectDecisionStore struct{ NoopStore }

func (objectDecisionStore) CanAccessAgentResource(_, agentID, _, _ string) (bool, error) {
	return agentID == "allowed", nil
}

func TestRequireAgentFromAllIdentifierLocationsAndRoles(t *testing.T) {
	mgr := NewManager(objectDecisionStore{}, zap.NewNop())
	sources := []struct {
		name, method, path, body, contentType string
		source                                AgentIDSource
	}{
		{"path", http.MethodGet, "/objects/allowed", "", "", AgentIDSource{PathParam: "agent"}},
		{"query", http.MethodGet, "/objects?agent_id=allowed", "", "", AgentIDSource{QueryParam: "agent_id"}},
		{"body", http.MethodPost, "/objects", `{"agent_id":"allowed"}`, "application/json", AgentIDSource{BodyField: "agent_id"}},
		{"form", http.MethodPost, "/objects", url.Values{"agent_id": {"allowed"}}.Encode(), "application/x-www-form-urlencoded", AgentIDSource{FormField: "agent_id"}},
	}
	for _, role := range []string{RoleAdmin, RoleOperator, RoleViewer} {
		for _, tc := range sources {
			t.Run(role+"/"+tc.name, func(t *testing.T) {
				app := fiber.New()
				app.Use(func(c *fiber.Ctx) error {
					auth.SetClaims(c, &auth.Claims{Role: role, Scopes: []string{ResourceChat}})
					return c.Next()
				})
				h := mgr.RequireAgentFrom(ResourceChat, ActionChat, tc.source)
				app.Add(tc.method, "/objects/:agent?", h, func(c *fiber.Ctx) error { return c.SendStatus(http.StatusNoContent) })
				req, _ := http.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
				req.Header.Set("Content-Type", tc.contentType)
				resp, err := app.Test(req)
				if err != nil || resp.StatusCode != http.StatusNoContent {
					t.Fatalf("allowed object status=%v err=%v", resp.StatusCode, err)
				}
			})
		}
	}
}

func TestRequireAgentFromExplicitObjectDeny(t *testing.T) {
	for _, role := range []string{RoleAdmin, RoleOperator, RoleViewer} {
		app := fiber.New()
		app.Use(func(c *fiber.Ctx) error {
			auth.SetClaims(c, &auth.Claims{Role: role})
			return c.Next()
		})
		app.Post("/chat", NewManager(objectDecisionStore{}, zap.NewNop()).RequireAgentFrom(
			ResourceChat, ActionChat, AgentIDSource{BodyField: "agent_id"}),
			func(c *fiber.Ctx) error { return c.SendStatus(http.StatusNoContent) })
		req, _ := http.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"agent_id":"denied"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("role %s denied object status=%v err=%v", role, resp.StatusCode, err)
		}
	}
}
