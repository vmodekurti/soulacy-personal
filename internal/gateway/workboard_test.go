package gateway

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/workboard"
)

// newTestGatewayWithWorkboard wires a real SQLite-backed workboard store
// into a test gateway.
func newTestGatewayWithWorkboard(t *testing.T) *Server {
	t.Helper()
	s := newTestGateway(t, "secret")
	ws, err := workboard.NewStore(filepath.Join(t.TempDir(), "workboard.db"))
	if err != nil {
		t.Fatalf("workboard.NewStore: %v", err)
	}
	t.Cleanup(func() { ws.Close() })
	s.SetWorkboardStore(ws)
	return s
}

func wbCreate(t *testing.T, s *Server, body string) map[string]any {
	t.Helper()
	status, resp := gatewayJSON(t, s, http.MethodPost, "/api/v1/workboard/tasks", "secret", body)
	if status != http.StatusCreated {
		t.Fatalf("create task status = %d body=%v", status, resp)
	}
	return resp
}

func TestWorkboardNilStore_Returns503(t *testing.T) {
	s := newTestGateway(t, "secret") // no workboard store wired
	for _, probe := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/workboard/tasks", ""},
		{http.MethodPost, "/api/v1/workboard/tasks", `{"title":"x"}`},
		{http.MethodGet, "/api/v1/workboard/tasks/1", ""},
		{http.MethodPatch, "/api/v1/workboard/tasks/1", `{"status":"done"}`},
		{http.MethodDelete, "/api/v1/workboard/tasks/1", ""},
	} {
		status, body := gatewayJSON(t, s, probe.method, probe.path, "secret", probe.body)
		if status != http.StatusServiceUnavailable {
			t.Errorf("%s %s status = %d body=%v, want 503", probe.method, probe.path, status, body)
		}
	}
}

func TestWorkboardRequiresAuth(t *testing.T) {
	s := newTestGatewayWithWorkboard(t)
	status, _ := gatewayJSON(t, s, http.MethodGet, "/api/v1/workboard/tasks", "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list status = %d, want 401", status)
	}
}

func TestWorkboardCreate_DefaultsAndEcho(t *testing.T) {
	s := newTestGatewayWithWorkboard(t)
	task := wbCreate(t, s, `{"title":"ship story 5","description":"kanban","agent_id":"bot-1"}`)
	if task["title"] != "ship story 5" || task["description"] != "kanban" || task["agent_id"] != "bot-1" {
		t.Errorf("created task = %v", task)
	}
	if task["status"] != "todo" {
		t.Errorf("default status = %v, want todo", task["status"])
	}
	if task["id"] == nil || task["created_at"] == nil || task["updated_at"] == nil {
		t.Errorf("missing id/timestamps: %v", task)
	}
}

func TestWorkboardCreate_MissingTitle400(t *testing.T) {
	s := newTestGatewayWithWorkboard(t)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/workboard/tasks", "secret", `{"description":"no title"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d body=%v, want 400", status, body)
	}
}

func TestWorkboardCreate_InvalidStatus400(t *testing.T) {
	s := newTestGatewayWithWorkboard(t)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/workboard/tasks", "secret", `{"title":"x","status":"bogus"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d body=%v, want 400", status, body)
	}
}

func TestWorkboardCreate_MalformedJSON400(t *testing.T) {
	s := newTestGatewayWithWorkboard(t)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/workboard/tasks", "secret", `{not json`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d body=%v, want 400", status, body)
	}
}

func TestWorkboardGet_RoundTripAnd404(t *testing.T) {
	s := newTestGatewayWithWorkboard(t)
	created := wbCreate(t, s, `{"title":"find me"}`)
	id := fmt.Sprintf("%v", created["id"])

	status, task := gatewayJSON(t, s, http.MethodGet, "/api/v1/workboard/tasks/"+id, "secret", "")
	if status != http.StatusOK || task["title"] != "find me" {
		t.Fatalf("get task = %d %v", status, task)
	}

	status, _ = gatewayJSON(t, s, http.MethodGet, "/api/v1/workboard/tasks/99999", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("get missing status = %d, want 404", status)
	}

	status, _ = gatewayJSON(t, s, http.MethodGet, "/api/v1/workboard/tasks/not-a-number", "secret", "")
	if status != http.StatusBadRequest {
		t.Fatalf("get bad id status = %d, want 400", status)
	}
}

func TestWorkboardList_EmptyAndFiltered(t *testing.T) {
	s := newTestGatewayWithWorkboard(t)

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/workboard/tasks", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list status = %d body=%v", status, body)
	}
	tasks, ok := body["tasks"].([]any)
	if !ok {
		t.Fatalf("tasks should be an array (not null), body=%v", body)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected empty board, got %d", len(tasks))
	}

	wbCreate(t, s, `{"title":"a","agent_id":"bot-1"}`)
	wbCreate(t, s, `{"title":"b","agent_id":"bot-1","status":"done"}`)
	wbCreate(t, s, `{"title":"c","agent_id":"bot-2"}`)

	status, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/workboard/tasks?status=todo&agent_id=bot-1", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("filtered list status = %d body=%v", status, body)
	}
	tasks = body["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("filtered list = %d tasks, want 1 (body=%v)", len(tasks), body)
	}

	status, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/workboard/tasks?status=bogus", "secret", "")
	if status != http.StatusBadRequest {
		t.Fatalf("bogus status filter = %d body=%v, want 400", status, body)
	}
}

func TestWorkboardPatch_StatusTransition(t *testing.T) {
	s := newTestGatewayWithWorkboard(t)
	created := wbCreate(t, s, `{"title":"move me"}`)
	id := fmt.Sprintf("%v", created["id"])

	for _, next := range []string{"running", "needs_review", "done"} {
		status, task := gatewayJSON(t, s, http.MethodPatch, "/api/v1/workboard/tasks/"+id, "secret",
			fmt.Sprintf(`{"status":%q}`, next))
		if status != http.StatusOK {
			t.Fatalf("patch to %s status = %d body=%v", next, status, task)
		}
		if task["status"] != next {
			t.Fatalf("patch to %s: task status = %v", next, task["status"])
		}
	}
}

func TestWorkboardPatch_Errors(t *testing.T) {
	s := newTestGatewayWithWorkboard(t)
	created := wbCreate(t, s, `{"title":"x"}`)
	id := fmt.Sprintf("%v", created["id"])

	status, _ := gatewayJSON(t, s, http.MethodPatch, "/api/v1/workboard/tasks/"+id, "secret", `{"status":"bogus"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("patch bogus status = %d, want 400", status)
	}
	status, _ = gatewayJSON(t, s, http.MethodPatch, "/api/v1/workboard/tasks/99999", "secret", `{"status":"done"}`)
	if status != http.StatusNotFound {
		t.Fatalf("patch missing = %d, want 404", status)
	}
}

func TestWorkboardDelete(t *testing.T) {
	s := newTestGatewayWithWorkboard(t)
	created := wbCreate(t, s, `{"title":"bye"}`)
	id := fmt.Sprintf("%v", created["id"])

	status, _ := gatewayJSON(t, s, http.MethodDelete, "/api/v1/workboard/tasks/"+id, "secret", "")
	if status != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", status)
	}
	status, _ = gatewayJSON(t, s, http.MethodGet, "/api/v1/workboard/tasks/"+id, "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", status)
	}
	status, _ = gatewayJSON(t, s, http.MethodDelete, "/api/v1/workboard/tasks/"+id, "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("delete missing = %d, want 404", status)
	}
}
