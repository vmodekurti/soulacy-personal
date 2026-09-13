package gateway

import (
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/httptestutil"
	"github.com/soulacy/soulacy/pkg/agent"
)

func TestModelPreparationReadOnlyEndpoint(t *testing.T) {
	s, p := newTestGatewayWithLLM(t, "secret")
	d := &agent.Definition{ID: "prepared", Name: "Prepared", Enabled: true, SystemPrompt: "Original private instructions", LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"}}
	s.loader.Register(d)
	before := d.Clone()
	path := "/api/v1/agents/prepared/model-preparation"
	undoRequest(t, s, "GET", path, "", "", 401)
	undoRequest(t, s, "GET", "/api/v1/agents/missing/model-preparation", "secret", "", 404)
	res := undoRequest(t, s, "GET", path, "secret", "", 200)
	if res["goal_preserved"] != true || res["agent_id"] != "prepared" || res["profile"].(map[string]any)["model"] != "fake-model" {
		t.Fatal(res)
	}
	if !reflect.DeepEqual(d, before) {
		t.Fatal("preview wrote definition")
	}
	if p.lastRequest().Model != "" {
		t.Fatal("preview performed inference")
	}
	d.LLM.AllowedModels = []string{"other"}
	undoRequest(t, s, "GET", path, "secret", "", 422)
}

func TestModelPreparationScopesApplyWithoutOptionalRBAC(t *testing.T) {
	s, p := newTestGatewayWithLLM(t, "secret")
	s.loader.Register(&agent.Definition{ID: "agent", LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"}})
	for _, tc := range []struct {
		role   string
		scopes []string
		status int
	}{
		{"viewer", []string{"config:read"}, 403}, {"viewer", []string{"agents:read"}, 200}, {"unknown-role", nil, 403},
	} {
		t.Run(fmt.Sprintf("%s-%d-%v", tc.role, tc.status, tc.scopes), func(t *testing.T) {
			app := fiber.New()
			app.Get("/agents/:id/model-preparation", func(c *fiber.Ctx) error {
				auth.SetClaims(c, &auth.Claims{Role: tc.role, Scopes: tc.scopes})
				return s.handleModelPreparation(c)
			})
			res, err := app.Test(httptestutil.WithHost(httptest.NewRequest("GET", "/agents/agent/model-preparation", nil)))
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.status || res.Header.Get("Cache-Control") != "no-store" {
				t.Fatal(res.StatusCode, res.Header)
			}
		})
	}
	if p.lastRequest().Model != "" {
		t.Fatal("viewing metadata executed inference")
	}
}
