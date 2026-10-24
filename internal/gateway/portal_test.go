package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/soulacy/soulacy/internal/httptestutil"
	"github.com/soulacy/soulacy/internal/llm"
	"go.uber.org/zap"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func portalTestContext() context.Context {
	return runtime.WithActiveAgent(context.Background(), runtime.GenieAgentID)
}
func portalTestCall(t *testing.T, p *portalSession, mode, name string, params, body map[string]any) map[string]any {
	t.Helper()
	v, err := p.PortalAction(portalTestContext(), mode, name, params, body)
	if err != nil {
		t.Fatal(err)
	}
	return v.(map[string]any)
}
func TestPortalCatalogRoutesAndBoundaries(t *testing.T) {
	s := newTestGateway(t, "secret")
	registered := map[string]bool{}
	for _, stack := range s.app.Stack() {
		for _, route := range stack {
			registered[route.Method+" "+route.Path] = true
		}
	}
	seen := map[string]bool{}
	for _, a := range portalCatalog {
		if seen[a.Name] {
			t.Errorf("duplicate action %s", a.Name)
		}
		seen[a.Name] = true
		if !registered[a.Method+" /api/v1"+a.Path] {
			t.Errorf("action %s has no registered route: %s %s", a.Name, a.Method, a.Path)
		}
		if a.Method == "DELETE" && a.Mode != "confirm" {
			t.Errorf("destructive action %s missing confirmation", a.Name)
		}
		if strings.Contains(a.Path, "/credentials") || strings.Contains(a.Path, "/approvals/") || strings.Contains(a.Path, "/api-keys") {
			t.Errorf("secure interactive route exposed: %s", a.Name)
		}
	}
	p := &portalSession{server: s, authorization: "Bearer secret", host: "localhost"}
	for _, test := range []struct {
		mode, name   string
		params, body map[string]any
	}{
		{"read", "agents.delete", map[string]any{"id": "genie"}, nil},
		{"update", "system.restart", nil, nil},
		{"update", "providers.save", map[string]any{"id": "test"}, map[string]any{"api_key": "do-not-log"}},
		{"read", "agents.get", map[string]any{"id": "../config"}, nil},
		{"read", "agents.get", map[string]any{"id": "%2e%2e"}, nil},
		{"read", "agents.list", map[string]any{"api_key": "injected"}, nil},
		{"read", "not.registered", nil, nil},
	} {
		if _, err := p.PortalAction(portalTestContext(), test.mode, test.name, test.params, test.body); err == nil {
			t.Errorf("unsafe request accepted: %s %s", test.mode, test.name)
		}
	}
	if _, err := p.PortalAction(context.Background(), "read", "agents.list", nil, nil); err == nil {
		t.Fatal("missing active Genie accepted")
	}
}
func TestPortalAgentPartialEditPreservesAdvancedSettings(t *testing.T) {
	s := newTestGateway(t, "secret")
	p := &portalSession{server: s, authorization: "Bearer secret", host: "localhost"}
	create := `{"id":"portal-fixture","name":"Fixture","trigger":"channel","channels":["http"],"enabled":true,"system_prompt":"Keep my instructions","llm":{"provider":"test","model":"old-model","temperature":0.7,"max_tokens":8192},"max_turns":0,"budget":{"max_tokens":0,"max_llm_calls":0},"builtins":["web_search"],"memory":{"max_tokens":6000}}`
	if status, body := gatewayJSON(t, s, "POST", "/api/v1/agents", "secret", create); status != 201 {
		t.Fatalf("create %d %v", status, body)
	}
	result := portalTestCall(t, p, "update", "agents.update", map[string]any{"id": "portal-fixture"}, map[string]any{"llm": map[string]any{"model": "new-model"}, "description": "changed"})
	if result["ok"] != true {
		t.Fatalf("patch = %v", result)
	}
	def := s.loader.Get("portal-fixture")
	if def.LLM.Model != "new-model" || def.LLM.Provider != "test" || def.LLM.Temperature != 0.7 || def.LLM.MaxTokens != 8192 || def.SystemPrompt != "Keep my instructions" || def.MaxTurns != 0 || def.Memory.MaxTokens != 6000 || def.Builtins == nil || (*def.Builtins)[0] != "web_search" {
		t.Fatalf("unrelated fields changed: %+v", def)
	}
	disk, err := os.ReadFile(def.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(disk), "new-model") {
		t.Fatal("edit was not persisted")
	}
	_, err = p.PortalAction(portalTestContext(), "update", "agents.update", map[string]any{"id": "portal-fixture"}, map[string]any{"unknown_setting": true})
	if err == nil {
		t.Fatal("unknown field accepted")
	}

}
func TestPortalProviderSetupAndCredentialRedaction(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("llm:\n  providers:\n    ollama:\n      base_url: http://127.0.0.1:11434\n      keep_alive: 20m\n      api_key: existing-private-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	p := &portalSession{server: s, authorization: "Bearer secret", host: "localhost"}
	result := portalTestCall(t, p, "update", "providers.save", map[string]any{"id": "portal-local"}, map[string]any{"base_url": "http://127.0.0.1:11434/v1", "model": "fixture-model", "local": true})
	if result["ok"] != true {
		t.Fatalf("provider save = %v", result)
	}
	if result["result"].(map[string]any)["registered"] != true {
		t.Fatalf("local provider saved but not usable: %v", result)
	}
	if got := s.cfg.LLM.Providers["portal-local"]; got.Model != "fixture-model" || !got.Local {
		t.Fatalf("provider not saved: %+v", got)
	}
	if status, _ := gatewayJSON(t, s, "DELETE", "/api/v1/providers/portal-local", "secret", ""); status != 200 {
		t.Fatalf("delete status %d", status)
	}
	for _, id := range s.llmRouter.ProviderIDs() {
		if id == "portal-local" {
			t.Fatal("deleted provider remains callable")
		}
	}
	portalTestCall(t, p, "update", "providers.save", map[string]any{"id": "ollama"}, map[string]any{"model": "new-model"})
	saved, err := readRawConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	pc := saved["llm"].(map[string]any)["providers"].(map[string]any)["ollama"].(map[string]any)
	if pc["keep_alive"] != "20m" || pc["api_key"] != "existing-private-key" {
		t.Fatalf("provider edit clobbered settings")
	}
	redacted := redactPortalValue(map[string]any{"options": map[string]any{"headers": map[string]any{"Authorization": "secret"}}, "max_tokens": 0, "api_key": "secret"})
	data, _ := json.Marshal(redacted)
	if strings.Contains(string(data), "secret") || !strings.Contains(string(data), `"max_tokens":0`) {
		t.Fatalf("redaction = %s", data)
	}
}
func TestPortalForwardsCallerCredentialsAndRBAC(t *testing.T) {
	s := newTestGatewayWithRBAC(t)
	// The test middleware validates distinct caller identities; the portal must
	// replay the original credential, not replace it with the installation key.
	secured := fiber.New()
	secured.Use(func(c *fiber.Ctx) error {
		bearer := strings.TrimPrefix(c.Get("Authorization"), "Bearer ")
		if bearer == "" {
			bearer = c.Cookies("soulacy_access")
		}
		role := ""
		switch bearer {
		case "owner-token":
			role = "admin"
		case "viewer-token":
			role = "viewer"
		default:
			return c.Status(401).JSON(fiber.Map{"error": "unauthorized"})
		}
		auth.SetClaims(c, &auth.Claims{Role: role, RegisteredClaims: jwt.RegisteredClaims{Subject: bearer}})
		return c.Next()
	})
	secured.Get("/api/v1/config", s.rbacMW("config", "read"), func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"safe": true}) })
	secured.Post("/api/v1/providers/:id", s.rbacMW("providers", "write"), func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"saved": true}) })
	s.app = secured
	for _, tc := range []struct {
		credential string
		cookie     bool
		want       int
	}{{"owner-token", false, 200}, {"viewer-token", false, 403}, {"viewer-token", true, 403}, {"expired-token", false, 401}} {
		p := &portalSession{server: s, host: "localhost"}
		if tc.cookie {
			p.cookie = tc.credential
		} else {
			p.authorization = "Bearer " + tc.credential
		}
		result := portalTestCall(t, p, "update", "providers.save", map[string]any{"id": "new-provider"}, map[string]any{"model": "model"})
		if result["http_status"] != tc.want {
			t.Fatalf("%+v: %v", tc, result)
		}
	}
}
func TestPortalSearchFindsSavedPodcastAndPaginates(t *testing.T) {
	s := newTestGateway(t, "secret")
	p := &portalSession{server: s, authorization: "Bearer secret", host: "localhost"}
	base := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	events := []message.Event{}
	for i, out := range []string{"Podcast https://audio.example/api/podcasts/episodes/episode-123/audio", "Podcast https://audio.example/api/podcasts/episodes/episode-456/audio", "Unrelated newest result"} {
		session := []string{"first", "second", "third"}[i]
		at := base.Add(time.Duration(i) * time.Hour)
		events = append(events, message.Event{Type: "message.in", AgentID: "podcast", SessionID: session, Timestamp: at, Payload: message.Message{Channel: "http"}}, message.Event{Type: "message.out", AgentID: "podcast", SessionID: session, Timestamp: at.Add(time.Second), Payload: message.Message{Parts: message.Text(out)}})
	}
	s.actions = &fakeTailBackend{events: events}
	params := map[string]any{"agent_id": "podcast", "q": "audio.example", "limit": 1, "after": "2026-10-18T07:00:00-05:00"}
	result := portalTestCall(t, p, "read", "runs.list", params, nil)
	data, _ := json.Marshal(result)
	if !strings.Contains(string(data), "episode-456") || strings.Contains(string(data), "Unrelated newest") {
		t.Fatalf("search = %s", data)
	}
	inner := result["result"].(map[string]any)
	if inner["has_more"] != true || inner["matched"] != float64(2) {
		t.Fatalf("pagination = %v", inner)
	}
	params["offset"] = 1
	result = portalTestCall(t, p, "read", "runs.list", params, nil)
	data, _ = json.Marshal(result)
	if !strings.Contains(string(data), "episode-123") {
		t.Fatalf("second page = %s", data)
	}
	params["after"] = "yesterday"
	result = portalTestCall(t, p, "read", "runs.list", params, nil)
	if result["http_status"] != 400 {
		t.Fatalf("invalid date silently accepted: %v", result)
	}
}

func TestPortalCriticalSettingsCannotBeChangedEvenByOwner(t *testing.T) {
	s := newTestGateway(t, "secret")
	p := &portalSession{server: s, authorization: "Bearer secret", host: "localhost"}
	// A provider referenced by a disabled agent is still operationally important.
	s.loader.Register(&agent.Definition{ID: "saved-job", Enabled: false, LLM: agent.LLMConfig{Provider: "referenced"}})
	s.loader.Register(&agent.Definition{ID: "host-helper", SystemTools: true})
	for _, tc := range []struct {
		name         string
		params, body map[string]any
	}{
		{"agents.update", map[string]any{"id": "genie"}, map[string]any{"system_prompt": "change core"}},
		{"agents.update", map[string]any{"id": "system"}, map[string]any{"llm": map[string]any{"model": "bad"}}},
		{"agents.update", map[string]any{"id": "host-helper"}, map[string]any{"system_prompt": "change host"}},
		{"agents.create", nil, map[string]any{"id": "GENIE", "name": "replacement"}},
		{"agents.create", nil, map[string]any{"id": "new-agent", "system_tools": true}},
		{"agents.update", map[string]any{"id": "saved-job"}, map[string]any{"policy": map[string]any{"enabled": false}}},
		{"agents.update", map[string]any{"id": "saved-job"}, map[string]any{"confirm_tools": []any{}}},
		{"providers.save", map[string]any{"id": "openai"}, map[string]any{"base_url": "http://example.test/v1"}},
		{"providers.save", map[string]any{"id": "referenced"}, map[string]any{"model": "replacement"}},
	} {
		if _, err := p.PortalAction(portalTestContext(), "update", tc.name, tc.params, tc.body); err == nil {
			t.Errorf("protected operation succeeded: %s %v", tc.name, tc.params)
		}
	}
	// Defense in depth: future catalog additions must not unlock infrastructure.
	for _, name := range []string{"settings.update", "system.restart", "channels.update", "plugins.enable", "mcp.delete"} {
		if err := p.allowPortalMutation(portalActionSpec{Name: name, Mode: "update"}, nil, nil); err == nil {
			t.Errorf("policy allowed %s", name)
		}
	}
	for _, id := range []string{"genie", "system", "host-helper"} {
		if _, err := p.PortalAction(portalTestContext(), "confirm", "agents.delete", map[string]any{"id": id}, nil); err == nil {
			t.Errorf("even approved deletion must not change core agent %s", id)
		}
	}
	if _, err := p.PortalAction(portalTestContext(), "confirm", "providers.delete", map[string]any{"id": "openai"}, nil); err == nil {
		t.Fatal("approved deletion removed default provider")
	}
}

// Exercise the real chat handler -> runtime -> portal -> API round trip, for
// both HTTP replies and streams, with bearer and browser-cookie credentials.
type portalScenarioProvider struct {
	mu            sync.Mutex
	calls         int
	portalOffered bool
}

func (*portalScenarioProvider) ID() string { return "portal-scenario" }
func (*portalScenarioProvider) Models(context.Context) ([]string, error) {
	return []string{"fixture"}, nil
}
func (p *portalScenarioProvider) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	for _, tool := range req.Tools {
		if tool.Name == "portal_read" {
			p.portalOffered = true
		}
	}
	if p.calls == 1 {
		return &llm.CompletionResponse{ToolCalls: []message.ToolCall{{ID: "portal-read", Name: "portal_read", Arguments: map[string]any{"action": "providers.list"}}}}, nil
	}
	raw, _ := json.Marshal(req.Messages)
	if !strings.Contains(string(raw), "default_provider") {
		return nil, fmt.Errorf("portal result did not reach the model")
	}
	return &llm.CompletionResponse{Content: "Verified the configured providers from the portal."}, nil
}
func TestPortalAuthenticatedChatRoundTrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, cookie := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/cookie=%v", stream, cookie), func(t *testing.T) {
				s := newTestGateway(t, "secret")
				authEngine, err := auth.New(auth.Config{}, "secret", zap.NewNop())
				if err != nil {
					t.Fatal(err)
				}
				defer authEngine.Close()
				s.authEngine = authEngine
				s.app = s.buildApp()
				s.engine.SetTimeoutHierarchy(10*time.Second, 20*time.Second, 30*time.Second, time.Minute)
				provider := &portalScenarioProvider{}
				s.llmRouter.Register(provider)
				def := s.loader.Get("genie")
				def.LLM.Provider = provider.ID()
				def.LLM.Model = "fixture"
				s.loader.Register(def)
				endpoint := "/api/v1/chat"
				if stream {
					endpoint += "/stream"
				}
				req, _ := http.NewRequest("POST", endpoint, strings.NewReader(`{"agent_id":"genie","text":"Which providers are configured?"}`))
				req.Header.Set("Content-Type", "application/json")
				if cookie {
					req.AddCookie(&http.Cookie{Name: "soulacy_access", Value: "secret"})
				} else {
					req.Header.Set("Authorization", "Bearer secret")
				}
				resp, err := s.app.Test(httptestutil.WithHost(req), -1)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				data, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != 200 || !strings.Contains(string(data), "Verified the configured providers") {
					t.Fatalf("chat returned %d: %s", resp.StatusCode, data)
				}
				provider.mu.Lock()
				defer provider.mu.Unlock()
				if !provider.portalOffered || provider.calls != 2 {
					t.Fatalf("offered=%v calls=%d", provider.portalOffered, provider.calls)
				}
			})
		}
	}
}

func TestPortalCreateKeepsAgentDisabledAndCannotOverwrite(t *testing.T) {
	s := newTestGateway(t, "secret")
	p := &portalSession{server: s, authorization: "Bearer secret", host: "localhost"}
	body := map[string]any{"id": "paused-fixture", "name": "Paused fixture", "system_prompt": "Wait until enabled.", "trigger": "channel", "channels": []any{"http"}, "enabled": false}
	result := portalTestCall(t, p, "update", "agents.create", nil, body)
	if result["ok"] != true || s.loader.Get("paused-fixture").Enabled {
		t.Fatalf("explicit disabled state was not preserved: %v", result)
	}
	raw, err := os.ReadFile(s.loader.Get("paused-fixture").SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "enabled: false") {
		t.Fatal("disabled setting not persisted")
	}
	body["system_prompt"] = "Replacement"
	if _, err := p.PortalAction(portalTestContext(), "update", "agents.create", nil, body); err == nil {
		t.Fatal("create overwrote an existing agent")
	}
	if s.loader.Get("paused-fixture").SystemPrompt != "Wait until enabled." {
		t.Fatal("existing prompt changed")
	}
	delete(body, "enabled")
	body["id"] = "default-fixture"
	result = portalTestCall(t, p, "update", "agents.create", nil, body)
	if result["ok"] != true || !s.loader.Get("default-fixture").Enabled {
		t.Fatal("omitted enabled no longer defaults true")
	}
}
