package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/auth/apikeys"
	"github.com/soulacy/soulacy/internal/httptestutil"
	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/pkg/agent"
	"go.uber.org/zap"
)

func notebookGateway(t *testing.T, key string) (*Server, *learning.Notebook) {
	t.Helper()
	s, p := newTestGatewayWithLLM(t, key)
	s.loader.Register(&agent.Definition{ID: "learner", Name: "Learner", Enabled: true, Learning: agent.LearningConfig{Enabled: true, AutoPropose: true}, LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"}})
	s.loader.Register(&agent.Definition{ID: "other", Name: "Other"})
	d := learning.Draft{Key: "units", Kind: "preference", Title: "Measurement units", Trigger: "Writing measurements", Content: "Use metric units.", Verification: "Check the user's current request for exceptions.", Citations: []learning.Citation{{SourceID: "user", Quote: "Use metric units for measurements."}}}
	b, _ := json.Marshal(map[string]any{"lesson": d})
	p.content = string(b)
	n, err := learning.OpenNotebook(filepath.Join(t.TempDir(), "notebook.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	s.engine.SetLearningNotebook(n, true)
	return s, n
}

const notebookAPI = "/api/v1/agents/learner/learning"

func TestLearningNotebookRefusesMissingNonAdminIdentity(t *testing.T) {
	s, _ := notebookGateway(t, "secret")
	app := fiber.New()
	app.Get("/agents/:id/learning/lessons", func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{Role: "viewer"})
		return s.handleLearningNotebook(c)
	})
	req := httptest.NewRequest("GET", "/agents/learner/learning/lessons", nil)
	req.Header.Set("Authorization", "Bearer validated-fixture")
	resp, err := app.Test(httptestutil.WithHost(req))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("incomplete identity inherited admin notebook", resp.StatusCode)
	}
}

func TestLearningNotebookExportsOnlyApprovedPrivateSkill(t *testing.T) {
	s, n := notebookGateway(t, "secret")
	scope := learning.Scope{Owner: "admin", AgentID: "learner"}
	d := learning.Draft{Key: "staging-checks", Kind: "skill", Title: "Staging checks", Trigger: "Before a release", Content: "Check the migration log, then run staging smoke tests.", Verification: "Stop on errors or failed tests.", Citations: []learning.Citation{{SourceID: "user", Quote: "Check staging logs and smoke tests before release."}}}
	l, _, err := n.Propose(context.Background(), scope, "export", "session", d, []learning.Source{{ID: "user", Kind: "user", Text: d.Citations[0].Quote}})
	if err != nil {
		t.Fatal(err)
	}
	path := notebookAPI + "/lessons/" + l.ID + "/export"
	undoRequest(t, s, "GET", path, "secret", "", 409)
	_, err = n.Review(context.Background(), scope, l.ID, "approve")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := s.app.Test(httptestutil.WithHost(req))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 || string(body) != learning.ExportSkill(l) || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Content-Disposition") != `attachment; filename="staging-checks-SKILL.md"` {
		t.Fatal("invalid export", resp.StatusCode, string(body), err)
	}
}

func TestLearningNotebookHTTPTeachReviewFeedbackAndVersionBoundaries(t *testing.T) {
	s, n := notebookGateway(t, "secret")
	undoRequest(t, s, "GET", notebookAPI+"/lessons", "", "", 401)
	undoRequest(t, s, "GET", notebookAPI+"/lessons?api_key=secret", "", "", 403)
	res := undoRequest(t, s, "POST", notebookAPI+"/teach", "secret", `{"note":"Use metric units for measurements."}`, 200)
	l := res["lesson"].(map[string]any)
	id := l["id"].(string)
	path := notebookAPI + "/lessons/" + id
	if l["status"] != "pending" || res["created"] != true {
		t.Fatal(res)
	}
	if active, _ := n.List(context.Background(), learning.Scope{Owner: "admin", AgentID: "learner"}, "active"); len(active) != 0 {
		t.Fatal("teach activated guidance")
	}
	undoRequest(t, s, "POST", path+"/review", "secret", `{"action":"approve","confirmed":false}`, 400)
	for _, body := range []string{`{"action":"approve","confirmed":true,"confirmed":false}`, `{"action":"approve","Confirmed":true}`, `{"action":"approve","confirmed":true,"owner":"bob"}`, `null`, `[]`, `{} {}`} {
		undoRequest(t, s, "POST", path+"/review", "secret", body, 400)
	}
	for range 2 {
		undoRequest(t, s, "POST", path+"/review", "secret", `{"action":"approve","confirmed":true}`, 200)
	}
	undoRequest(t, s, "GET", "/api/v1/agents/other/learning/lessons/"+id, "secret", "", 404)
	undoRequest(t, s, "POST", path+"/feedback", "secret", `{"rating":1}`, 200)
	undoRequest(t, s, "POST", path+"/feedback", "secret", `{"rating":9}`, 400)
	undoRequest(t, s, "POST", path+"/review", "secret", `{"action":"archive","confirmed":true}`, 200)
	undoRequest(t, s, "POST", path+"/review", "secret", `{"action":"approve","confirmed":true}`, 409)
	undoRequest(t, s, "POST", path+"/review", "secret", `{"action":"restore","confirmed":true}`, 200)
	undoRequest(t, s, "GET", notebookAPI+"/lessons?offset=-1", "secret", "", 400)
	undoRequest(t, s, "GET", notebookAPI+"/lessons?status=madeup", "secret", "", 400)
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := s.app.Test(httptestutil.WithHost(req))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("private lessons cacheable")
	}
}

func TestLearningNotebookAuthScopesAndObjectDeny(t *testing.T) {
	s, _ := notebookGateway(t, "")
	undoRequest(t, s, "GET", notebookAPI+"/lessons", "", "", 503)
	s, _ = notebookGateway(t, "secret")
	engine := lateWiredTestEngine(t, "secret")
	keys, err := apikeys.NewSQLiteStore(filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { keys.Close() })
	engine.SetAPIKeyStore(keys)
	s.SetAuth(engine)
	owner, _, err := keys.Create(context.Background(), "Owner", []string{"agents:read", "memory"})
	if err != nil {
		t.Fatal(err)
	}
	other, _, _ := keys.Create(context.Background(), "Other", []string{"agents:read", "memory"})
	narrow, _, _ := keys.Create(context.Background(), "No memory", []string{"agents"})
	res := undoRequest(t, s, "POST", notebookAPI+"/teach", owner, `{"note":"Use metric units for measurements."}`, 200)
	id := res["lesson"].(map[string]any)["id"].(string)
	undoRequest(t, s, "GET", notebookAPI+"/lessons/"+id, other, "", 404)
	undoRequest(t, s, "POST", notebookAPI+"/lessons/"+id+"/review", other, `{"action":"approve","confirmed":true}`, 404)
	undoRequest(t, s, "GET", notebookAPI+"/lessons", narrow, "", 403)
	// Private memory permission must not implicitly authorize global skill
	// installation through the older promotion endpoint.
	legacy, err := learning.NewStore(filepath.Join(t.TempDir(), "legacy.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	s.engine.SetLearningStore(legacy)
	draft, err := legacy.Add(learning.Proposal{AgentID: "learner", Kind: "skill", Title: "Legacy procedure", Content: "Inspect the staging logs."})
	if err != nil {
		t.Fatal(err)
	}
	undoRequest(t, s, "POST", "/api/v1/learning/proposals/"+draft.ID+"/accept", owner, `{}`, 403)
	stored, err := legacy.List("learner", learning.StatusPending, 10)
	if err != nil || len(stored) != 1 || stored[0].ID != draft.ID {
		t.Fatal("unauthorized promotion changed state", stored, err)
	}
	s.SetRBAC(rbac.NewManager(publishedDenyStore{}, zap.NewNop()))
	undoRequest(t, s, "GET", notebookAPI+"/lessons", owner, "", 403)
	undoRequest(t, s, "POST", notebookAPI+"/teach", owner, `{"note":"Use metric units for measurements."}`, 403)
}
