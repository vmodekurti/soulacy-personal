package gateway

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/httptestutil"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/agent"
)

// adaptiveApp builds a fiber app whose every request carries the given claims,
// mounted on the real adaptive-memory handlers.
func adaptiveApp(t *testing.T, s *Server, role, subject string) *fiber.App {
	t.Helper()
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{Role: role, RegisteredClaims: jwt.RegisteredClaims{Subject: subject}})
		return c.Next()
	})
	api := app.Group("/api/v1")
	api.Get("/memory/facts/status", s.handleAdaptiveMemoryStatus)
	api.Get("/memory/facts/export", s.handleAdaptiveMemoryExport)
	api.Get("/memory/facts", s.handleAdaptiveMemoryList)
	api.Post("/memory/facts", s.handleAdaptiveMemoryAdd)
	api.Patch("/memory/facts/:id", s.handleAdaptiveMemoryUpdate)
	api.Delete("/memory/facts/:id", s.handleAdaptiveMemoryDelete)
	api.Delete("/memory/facts", s.handleAdaptiveMemoryPurge)
	return app
}

func adaptiveGateway(t *testing.T) (*Server, *memory.LocalAdaptive) {
	t.Helper()
	s, _ := newTestGatewayWithLLM(t, "secret")
	s.loader.Register(&agent.Definition{ID: "helper", Name: "Helper", Enabled: true})
	store, err := memory.OpenFactSQLite(filepath.Join(t.TempDir(), "adaptive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	local := memory.NewLocalAdaptive(store, nil, nil, memory.LocalOptions{})
	s.engine.SetAdaptiveMemory(local, runtime.AdaptiveMemoryOptions{Enabled: true})
	return s, local
}

func doJSON(t *testing.T, app *fiber.App, method, path, body string) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer validated-fixture")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(httptestutil.WithHost(req))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	data, _ := io.ReadAll(resp.Body)
	if len(data) > 0 {
		_ = json.Unmarshal(data, &out)
	}
	return resp.StatusCode, out
}

func TestAdaptiveMemoryFactsAreIsolatedPerUser(t *testing.T) {
	s, _ := adaptiveGateway(t)
	ada := adaptiveApp(t, s, "operator", "ada")
	bob := adaptiveApp(t, s, "operator", "bob")
	admin := adaptiveApp(t, s, "admin", "root")

	code, out := doJSON(t, ada, "POST", "/api/v1/memory/facts", `{"agent_id":"helper","category":"preference","content":"Prefers metric units"}`)
	if code != 201 {
		t.Fatalf("add: %d %+v", code, out)
	}
	id := out["fact"].(map[string]any)["id"].(string)

	if code, out := doJSON(t, bob, "GET", "/api/v1/memory/facts?agent_id=helper", ""); code != 200 || len(out["facts"].([]any)) != 0 {
		t.Fatalf("bob must not see ada's facts: %d %+v", code, out)
	}
	if code, _ := doJSON(t, bob, "GET", "/api/v1/memory/facts?agent_id=helper&owner=ada", ""); code != 403 {
		t.Fatalf("non-admin owner override must be refused, got %d", code)
	}
	if code, _ := doJSON(t, bob, "DELETE", "/api/v1/memory/facts/"+id+"?agent_id=helper", ""); code != 404 {
		t.Fatalf("bob must not delete ada's fact, got %d", code)
	}
	if code, out := doJSON(t, admin, "GET", "/api/v1/memory/facts?agent_id=helper&owner=ada", ""); code != 200 || len(out["facts"].([]any)) != 1 {
		t.Fatalf("admin owner override should list ada's fact: %d %+v", code, out)
	}
	if code, out := doJSON(t, ada, "GET", "/api/v1/memory/facts?agent_id=helper", ""); code != 200 || len(out["facts"].([]any)) != 1 {
		t.Fatalf("ada should see her own fact: %d %+v", code, out)
	}
}

func TestAdaptiveMemoryCrudSearchExportPurge(t *testing.T) {
	s, _ := adaptiveGateway(t)
	ada := adaptiveApp(t, s, "operator", "ada")
	_, out := doJSON(t, ada, "POST", "/api/v1/memory/facts", `{"agent_id":"helper","category":"identity","content":"Lives in Denver"}`)
	id := out["fact"].(map[string]any)["id"].(string)
	doJSON(t, ada, "POST", "/api/v1/memory/facts", `{"agent_id":"helper","category":"entity","content":"Has a dog named Rex"}`)

	if code, out := doJSON(t, ada, "PATCH", "/api/v1/memory/facts/"+id, `{"agent_id":"helper","content":"Lives in Boulder"}`); code != 200 || out["fact"].(map[string]any)["content"] != "Lives in Boulder" || out["fact"].(map[string]any)["category"] != "identity" {
		t.Fatalf("patch should rewrite content and keep category: %d %+v", code, out)
	}
	if code, out := doJSON(t, ada, "GET", "/api/v1/memory/facts?agent_id=helper&q=dog+Rex", ""); code != 200 || len(out["facts"].([]any)) == 0 || out["facts"].([]any)[0].(map[string]any)["content"] != "Has a dog named Rex" {
		t.Fatalf("search should rank the dog fact first: %d %+v", code, out)
	}
	if code, out := doJSON(t, ada, "GET", "/api/v1/memory/facts/status?agent_id=helper", ""); code != 200 || out["provider"] != "local" || out["active"].(float64) != 2 {
		t.Fatalf("status: %d %+v", code, out)
	}
	if code, out := doJSON(t, ada, "GET", "/api/v1/memory/facts/export?agent_id=helper", ""); code != 200 || len(out["facts"].([]any)) != 2 || out["owner"] != "ada" {
		t.Fatalf("export: %d %+v", code, out)
	}
	if code, _ := doJSON(t, ada, "POST", "/api/v1/memory/facts", `{"agent_id":"helper","category":"identity","content":""}`); code != 400 {
		t.Fatalf("empty fact must be rejected, got %d", code)
	}
	if code, _ := doJSON(t, ada, "DELETE", "/api/v1/memory/facts?agent_id=helper", ""); code != 400 {
		t.Fatalf("purge without confirm must be refused, got %d", code)
	}
	if code, out := doJSON(t, ada, "DELETE", "/api/v1/memory/facts?agent_id=helper&confirm=true", ""); code != 200 || out["removed"].(float64) != 2 {
		t.Fatalf("purge: %d %+v", code, out)
	}
	if code, _ := doJSON(t, ada, "GET", "/api/v1/memory/facts?agent_id=nope", ""); code != 404 {
		t.Fatalf("unknown agent should 404, got %d", code)
	}
}

func TestAdaptiveMemoryRequiresIdentityAndEnabledEngine(t *testing.T) {
	s, _ := adaptiveGateway(t)
	viewerNoSubject := adaptiveApp(t, s, "viewer", "")
	if code, _ := doJSON(t, viewerNoSubject, "GET", "/api/v1/memory/facts", ""); code != 403 {
		t.Fatalf("missing subject must be refused, got %d", code)
	}
	s.engine.SetAdaptiveMemory(nil, runtime.AdaptiveMemoryOptions{})
	if code, _ := doJSON(t, adaptiveApp(t, s, "admin", "root"), "GET", "/api/v1/memory/facts", ""); code != 503 {
		t.Fatalf("disabled engine should 503, got %d", code)
	}
}

func TestConfigPatchHotAppliesAdaptiveMemory(t *testing.T) {
	s, _ := adaptiveGateway(t)
	s.cfgPath = filepath.Join(t.TempDir(), "config.yaml")
	var rebuilt []string
	s.SetAdaptiveMemoryRebuilder(func(ac config.AdaptiveMemoryConfig) error { rebuilt = append(rebuilt, ac.Provider); return nil })
	app := fiber.New()
	app.Patch("/api/v1/config", func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{Role: "admin"})
		return s.handlePatchConfig(c)
	})
	req := httptest.NewRequest("PATCH", "/api/v1/config", strings.NewReader(`{"memory":{"adaptive":{"provider":"mem0","mem0":{"base_url":"http://127.0.0.1:1","api_key":"k"}}}}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(httptestutil.WithHost(req))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("patch: %d", resp.StatusCode)
	}
	if len(rebuilt) != 1 || rebuilt[0] != "mem0" || s.cfg.Memory.Adaptive.Mem0.APIKey != "k" {
		t.Fatalf("rebuilder not invoked with patched config: %v %+v", rebuilt, s.cfg.Memory.Adaptive)
	}
	view := s.safeConfigView()["memory"].(fiber.Map)["adaptive"].(fiber.Map)["mem0"].(fiber.Map)
	if view["api_key"] != "***" || view["configured"] != true {
		t.Fatalf("config view must mask the key and report configured: %+v", view)
	}
}
