package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/actionlog"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/requestctx"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/scheduler"
	storagesqlite "github.com/soulacy/soulacy/internal/storage/sqlite"
	"github.com/soulacy/soulacy/pkg/agent"
)

// scopedAgentServer builds a gateway whose loader holds one agent per
// workspace under the same ID, and an app that runs each request as a
// specified workspace.
func scopedAgentServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	loader := runtime.NewLoader([]string{dir})
	s := withCfg(&Server{
		loader: loader,
		log:    zap.NewNop(),
		// The update path keeps the cron table in step with the file, so the
		// handler needs a real scheduler even when nothing is scheduled.
		scheduler: scheduler.New(nil, loader, zap.NewNop(), nil),
	}, &config.Config{AgentDirs: []string{dir}})
	for _, ws := range []string{"ws_a", "ws_b"} {
		def := &agent.Definition{ID: "shared-bot", Name: ws + " bot", Enabled: true}
		if err := loader.UpsertInWorkspace(ws, dir, def, "usr_"+ws); err != nil {
			t.Fatal(err)
		}
	}
	return s, dir
}

func appAsWorkspace(t *testing.T, s *Server, workspaceID string, register func(*fiber.App)) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		identity, err := requestctx.New(requestctx.Input{
			Subject: "usr_" + workspaceID, OrganizationID: "org_a", WorkspaceID: workspaceID,
			MembershipID: "mem_" + workspaceID, Role: "owner", RequestID: "req_" + workspaceID,
			PrincipalKind: "user",
		})
		if err != nil {
			t.Fatal(err)
		}
		c.Locals(workspaceIdentityLocal, identity)
		c.SetUserContext(requestctx.With(c.UserContext(), identity))
		c.Locals("request_id", "req_"+workspaceID)
		return c.Next()
	})
	register(app)
	return app
}

// The Runs page must expose the same workspace-owned mirror that its scoped
// event query reads. Returning ActionLogBackend.EventFilePath here used to
// display the legacy Personal-mode path even for a Team/Scale workspace.
func TestAgentActionsReturnsWorkspaceLogPath(t *testing.T) {
	root := t.TempDir()
	log, err := actionlog.New(filepath.Join(root, "logs"), filepath.Join(root, "actions.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })

	s := &Server{actions: storagesqlite.NewActionLog(log), log: zap.NewNop()}
	app := appAsWorkspace(t, s, "ws_a", func(app *fiber.App) {
		app.Get("/agents/:id/actions", s.handleAgentActions)
	})
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/agents/weather-advice-agent/actions", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "logs", ".workspaces", "ws_a", "weather-advice-agent.log")
	if payload.Path != want {
		t.Fatalf("workspace log path = %q, want %q", payload.Path, want)
	}
}

// A handler resolves agents through the request's verified workspace, so the
// same URL returns each tenant its own agent.
func TestAgentHandlersResolveThroughRequestWorkspace(t *testing.T) {
	s, _ := scopedAgentServer(t)
	for _, ws := range []string{"ws_a", "ws_b"} {
		app := appAsWorkspace(t, s, ws, func(app *fiber.App) {
			app.Get("/agents/:id", s.handleGetAgent)
		})
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/agents/shared-bot", nil))
		if err != nil {
			t.Fatal(err)
		}
		var def agent.Definition
		if err := json.NewDecoder(resp.Body).Decode(&def); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if def.Name != ws+" bot" {
			t.Fatalf("workspace %s received %q", ws, def.Name)
		}
	}
}

// An agent that exists only in another workspace must read as absent, with no
// metadata about the target in the response.
func TestCrossWorkspaceAgentReadsAsNotFound(t *testing.T) {
	s, dir := scopedAgentServer(t)
	if err := s.loader.UpsertInWorkspace("ws_a", dir, &agent.Definition{ID: "a-only", Name: "Confidential A"}, "usr_a"); err != nil {
		t.Fatal(err)
	}
	app := appAsWorkspace(t, s, "ws_b", func(app *fiber.App) {
		app.Get("/agents/:id", s.handleGetAgent)
	})
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/agents/a-only", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
	body := make([]byte, 512)
	n, _ := resp.Body.Read(body)
	if strings.Contains(string(body[:n]), "Confidential") {
		t.Fatalf("the 404 leaked the target's metadata: %s", body[:n])
	}
}

// A listing shows only the caller's own workspace.
func TestAgentListingIsScopedToTheRequestWorkspace(t *testing.T) {
	s, dir := scopedAgentServer(t)
	if err := s.loader.UpsertInWorkspace("ws_a", dir, &agent.Definition{ID: "a-only", Name: "A only"}, "usr_a"); err != nil {
		t.Fatal(err)
	}
	app := appAsWorkspace(t, s, "ws_b", func(app *fiber.App) {
		app.Get("/agents", s.handleListAgents)
	})
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/agents", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(payload)
	if strings.Contains(string(encoded), "a-only") {
		t.Fatalf("another workspace's agent appeared in the listing: %s", encoded)
	}
	if !strings.Contains(string(encoded), "shared-bot") {
		t.Fatalf("the caller's own agent is missing: %s", encoded)
	}
}

// A write lands in the caller's own workspace and leaves the other alone, even
// when both use the same agent ID.
func TestAgentWriteStaysInsideTheRequestWorkspace(t *testing.T) {
	s, dir := scopedAgentServer(t)
	app := appAsWorkspace(t, s, "ws_b", func(app *fiber.App) {
		app.Put("/agents/:id", s.handleUpdateAgent)
	})
	body := `{"id":"shared-bot","name":"Renamed by B","enabled":true}`
	req := httptest.NewRequest(http.MethodPut, "/agents/shared-bot", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := s.loader.GetInWorkspace("ws_b", "shared-bot"); got == nil || got.Name != "Renamed by B" {
		t.Fatalf("the write did not land in the caller's workspace: %+v", got)
	}
	if got := s.loader.GetInWorkspace("ws_a", "shared-bot"); got == nil || got.Name != "ws_a bot" {
		t.Fatalf("the write reached another workspace: %+v", got)
	}
	// And on disk, each workspace has its own file.
	if _, err := os.Stat(filepath.Join(dir, ".workspaces", "ws_a", "shared-bot", "SOUL.yaml")); err != nil {
		t.Fatalf("ws_a's file is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".workspaces", "ws_b", "shared-bot", "SOUL.yaml")); err != nil {
		t.Fatalf("ws_b's file is missing: %v", err)
	}
}

// Deleting an ID the caller's workspace does not own must not remove the
// workspace that does own it.
func TestAgentDeleteCannotReachAnotherWorkspace(t *testing.T) {
	s, dir := scopedAgentServer(t)
	if err := s.loader.UpsertInWorkspace("ws_a", dir, &agent.Definition{ID: "a-only", Name: "A only"}, "usr_a"); err != nil {
		t.Fatal(err)
	}
	app := appAsWorkspace(t, s, "ws_b", func(app *fiber.App) {
		app.Delete("/agents/:id", s.handleDeleteAgent)
	})
	resp, err := app.Test(httptest.NewRequest(http.MethodDelete, "/agents/a-only", nil))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := s.loader.GetInWorkspace("ws_a", "a-only"); got == nil {
		t.Fatal("a delete from another workspace removed the owning workspace's agent")
	}
}

// Version history is reached through the request's workspace too.
func TestAgentVersionsAreScopedToTheRequestWorkspace(t *testing.T) {
	s, dir := scopedAgentServer(t)
	// Two writes in ws_a produce one snapshot there; ws_b has none.
	if err := s.loader.UpsertInWorkspace("ws_a", dir, &agent.Definition{ID: "shared-bot", Name: "ws_a bot v2"}, "usr_a"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		workspace string
		wantAny   bool
	}{{"ws_a", true}, {"ws_b", false}} {
		app := appAsWorkspace(t, s, tc.workspace, func(app *fiber.App) {
			app.Get("/agents/:id/versions", s.handleListAgentVersions)
		})
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/agents/shared-bot/versions", nil))
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Versions []runtime.AgentVersion `json:"versions"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if tc.wantAny && len(payload.Versions) == 0 {
			t.Errorf("%s: expected its own history", tc.workspace)
		}
		if !tc.wantAny && len(payload.Versions) != 0 {
			t.Errorf("%s: saw another workspace's history: %+v", tc.workspace, payload.Versions)
		}
	}
}

// The cross-workspace scope may aggregate but must never resolve one agent,
// so it cannot become a back door into a tenant.
func TestCrossWorkspaceScopeAggregatesButCannotResolve(t *testing.T) {
	s, _ := scopedAgentServer(t)
	across := s.agentsAcrossWorkspaces()
	if got := len(across.All()); got < 2 {
		t.Fatalf("deployment-wide listing returned %d agents, want every workspace's", got)
	}
	if got := across.Get("shared-bot"); got != nil {
		t.Fatalf("the cross-workspace scope resolved a single agent: %+v", got)
	}
	if scoped := s.agentsForWorkspace("ws_a").Get("shared-bot"); scoped == nil {
		t.Fatal("a named workspace scope failed to resolve its own agent")
	}
}

// With no request identity — a directly constructed gateway, or Personal's
// open development mode — the scope is the personal workspace, which is the
// set those deployments have always seen.
func TestScopeWithoutIdentityIsPersonal(t *testing.T) {
	s, _ := scopedAgentServer(t)
	if got := s.agents(nil).WorkspaceID(); got != runtime.PersonalWorkspaceID {
		t.Fatalf("scope without identity = %q, want the personal workspace", got)
	}
}
