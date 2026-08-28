package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/mcpstore"
	"github.com/soulacy/soulacy/internal/rbac"
)

func mcpRequestApp(t *testing.T, srv *Server, workspaceID, role string) *fiber.App {
	t.Helper()
	return appAsMember(t, workspaceID, role, func(app *fiber.App) {
		app.Get("/requests", srv.handleListMCPInstallRequests)
		app.Post("/requests", srv.handleCreateMCPInstallRequest)
		app.Post("/requests/:id/deny", srv.handleDenyMCPInstallRequest)
	})
}

func mcpRequestJSON(t *testing.T, app *fiber.App, method, path string, payload any) (int, map[string]any) {
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

func TestDeveloperMCPRequestIsVisibleToWorkspaceAdminAndCanBeDenied(t *testing.T) {
	store, err := mcpstore.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := &Server{mcpServers: store}

	developer := mcpRequestApp(t, srv, "ws_one", rbac.RoleDeveloper)
	status, created := mcpRequestJSON(t, developer, http.MethodPost, "/requests", map[string]any{
		"source_url":  "https://github.com/wshobson/maverick-mcp",
		"reason":      "market research agent",
		"permissions": map[string]any{"network": "public", "workspace": "none"},
	})
	if status != http.StatusCreated {
		t.Fatalf("create status=%d body=%+v", status, created)
	}
	request, _ := created["request"].(map[string]any)
	id, _ := request["id"].(string)
	if id == "" || request["requested_by"] != "usr_ws_one" {
		t.Fatalf("created request=%+v", request)
	}

	status, own := mcpRequestJSON(t, developer, http.MethodGet, "/requests", nil)
	if status != http.StatusOK || own["count"] != float64(1) {
		t.Fatalf("developer list status=%d body=%+v", status, own)
	}
	admin := mcpRequestApp(t, srv, "ws_one", rbac.RoleAdmin)
	status, all := mcpRequestJSON(t, admin, http.MethodGet, "/requests", nil)
	if status != http.StatusOK || all["count"] != float64(1) {
		t.Fatalf("admin list status=%d body=%+v", status, all)
	}
	status, denied := mcpRequestJSON(t, admin, http.MethodPost, "/requests/"+id+"/deny", map[string]string{"reason": "not approved"})
	if status != http.StatusOK || denied["ok"] != true {
		t.Fatalf("deny status=%d body=%+v", status, denied)
	}
	got, err := store.GetInstallRequest(t.Context(), "ws_one", id)
	if err != nil || got.Status != mcpstore.RequestDenied || got.DecidedBy != "usr_ws_one" {
		t.Fatalf("denied request=%+v err=%v", got, err)
	}
}

func TestDeveloperCannotSeeAnotherDevelopersMCPRequests(t *testing.T) {
	store, err := mcpstore.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateInstallRequest(t.Context(), mcpstore.InstallRequest{
		ID: "mcp_req_other", WorkspaceID: "ws_one", SourceURL: "https://github.com/acme/private", RequestedBy: "usr_other",
	}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{mcpServers: store}
	developer := mcpRequestApp(t, srv, "ws_one", rbac.RoleDeveloper)
	status, body := mcpRequestJSON(t, developer, http.MethodGet, "/requests", nil)
	if status != http.StatusOK || body["count"] != float64(0) {
		t.Fatalf("developer saw another request: status=%d body=%+v", status, body)
	}
}
