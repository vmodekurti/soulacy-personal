package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/skillstore"
)

func skillRequestApp(t *testing.T, srv *Server, workspaceID, role string) *fiber.App {
	t.Helper()
	return appAsMember(t, workspaceID, role, func(app *fiber.App) {
		app.Get("/requests", srv.handleListSkillInstallRequests)
		app.Post("/requests", srv.handleCreateSkillInstallRequest)
		app.Post("/requests/:id/deny", srv.handleDenySkillInstallRequest)
	})
}

func skillRequestJSON(t *testing.T, app *fiber.App, method, path string, payload any) (int, map[string]any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func TestDeveloperSkillRequestIsVisibleToWorkspaceAdminAndCanBeDenied(t *testing.T) {
	store, err := skillstore.Open(filepath.Join(t.TempDir(), "skills.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := &Server{skillRequests: store}

	developer := skillRequestApp(t, srv, "ws_one", rbac.RoleDeveloper)
	status, created := skillRequestJSON(t, developer, http.MethodPost, "/requests", map[string]any{
		"source_url": "https://github.com/anthropics/skills/skill-creator",
		"reason":     "creating custom skills for workflow",
	})
	if status != http.StatusCreated {
		t.Fatalf("create status=%d body=%+v", status, created)
	}
	request, _ := created["request"].(map[string]any)
	id, _ := request["id"].(string)
	if id == "" || request["requested_by"] != "usr_ws_one" {
		t.Fatalf("created request=%+v", request)
	}

	status, own := skillRequestJSON(t, developer, http.MethodGet, "/requests", nil)
	if status != http.StatusOK || own["count"] != float64(1) {
		t.Fatalf("developer list status=%d body=%+v", status, own)
	}
	admin := skillRequestApp(t, srv, "ws_one", rbac.RoleAdmin)
	status, all := skillRequestJSON(t, admin, http.MethodGet, "/requests", nil)
	if status != http.StatusOK || all["count"] != float64(1) {
		t.Fatalf("admin list status=%d body=%+v", status, all)
	}
	status, denied := skillRequestJSON(t, admin, http.MethodPost, "/requests/"+id+"/deny", map[string]string{"reason": "not approved"})
	if status != http.StatusOK || denied["ok"] != true {
		t.Fatalf("deny status=%d body=%+v", status, denied)
	}
	requests, err := store.ListInstallRequests(t.Context(), "ws_one", "")
	if err != nil || len(requests) != 1 || requests[0].Status != skillstore.RequestDenied {
		t.Fatalf("denied requests=%+v err=%v", requests, err)
	}
}
