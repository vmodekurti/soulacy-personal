package gateway

// Story 14 API tests: collaboration fields on tasks + comments endpoints.

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/workboard"
)

func collabGateway(t *testing.T) *Server {
	t.Helper()
	s := newTestGateway(t, "")
	store, err := workboard.NewStore(filepath.Join(t.TempDir(), "wb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.SetWorkboardStore(store)
	return s
}

func TestWorkboardCreate_CollabFields(t *testing.T) {
	s := collabGateway(t)
	status, body := gatewayJSON(t, s, "POST", "/api/v1/workboard/tasks", "",
		`{"title":"review q4","owner":"vasu","priority":"high","tags":["q4"," Finance "],"due_at":"2026-07-01T10:00:00Z"}`)
	if status != 201 {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["owner"] != "vasu" || body["priority"] != "high" {
		t.Fatalf("body = %v", body)
	}
	tags, _ := body["tags"].([]any)
	if len(tags) != 2 || tags[1] != "finance" {
		t.Fatalf("tags = %v", tags)
	}
	if body["due_at"] == nil {
		t.Fatalf("due_at missing: %v", body)
	}
}

func TestWorkboardCreate_BadPriority400(t *testing.T) {
	s := collabGateway(t)
	status, _ := gatewayJSON(t, s, "POST", "/api/v1/workboard/tasks", "",
		`{"title":"x","priority":"yesterday"}`)
	if status != 400 {
		t.Fatalf("status = %d, want 400", status)
	}
}

func TestWorkboardCreate_BadDueDate400(t *testing.T) {
	s := collabGateway(t)
	status, _ := gatewayJSON(t, s, "POST", "/api/v1/workboard/tasks", "",
		`{"title":"x","due_at":"next tuesday"}`)
	if status != 400 {
		t.Fatalf("status = %d, want 400", status)
	}
}

func TestWorkboardPatch_CollabFieldsAndClearDue(t *testing.T) {
	s := collabGateway(t)
	_, created := gatewayJSON(t, s, "POST", "/api/v1/workboard/tasks", "",
		`{"title":"x","due_at":"2026-07-01T10:00:00Z"}`)
	id := int64(created["id"].(float64))

	status, body := gatewayJSON(t, s, "PATCH", fmt.Sprintf("/api/v1/workboard/tasks/%d", id), "",
		`{"owner":"rev","priority":"urgent","tags":["ops"],"due_at":""}`)
	if status != 200 {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["owner"] != "rev" || body["priority"] != "urgent" {
		t.Fatalf("body = %v", body)
	}
	if body["due_at"] != nil {
		t.Fatalf("due_at should be cleared by empty string, got %v", body["due_at"])
	}
}

func TestWorkboardComments_Flow(t *testing.T) {
	s := collabGateway(t)
	_, created := gatewayJSON(t, s, "POST", "/api/v1/workboard/tasks", "", `{"title":"x"}`)
	id := int64(created["id"].(float64))

	status, c1 := gatewayJSON(t, s, "POST", fmt.Sprintf("/api/v1/workboard/tasks/%d/comments", id), "",
		`{"author":"vasu","body":"first pass looks fine"}`)
	if status != 201 {
		t.Fatalf("add status = %d body=%v", status, c1)
	}
	status, _ = gatewayJSON(t, s, "POST", fmt.Sprintf("/api/v1/workboard/tasks/%d/comments", id), "",
		`{"author":"reviewer","body":"add a summary section","kind":"review"}`)
	if status != 201 {
		t.Fatalf("add review status = %d", status)
	}

	status, list := gatewayJSON(t, s, "GET", fmt.Sprintf("/api/v1/workboard/tasks/%d/comments", id), "", "")
	if status != 200 {
		t.Fatalf("list status = %d", status)
	}
	comments, _ := list["comments"].([]any)
	if len(comments) != 2 {
		t.Fatalf("comments = %v", list)
	}
	second := comments[1].(map[string]any)
	if second["kind"] != "review" || second["author"] != "reviewer" {
		t.Fatalf("review note = %v", second)
	}

	cid := int64(second["id"].(float64))
	status, _ = gatewayJSON(t, s, "DELETE", fmt.Sprintf("/api/v1/workboard/comments/%d", cid), "", "")
	if status != 204 {
		t.Fatalf("delete status = %d", status)
	}
	_, list = gatewayJSON(t, s, "GET", fmt.Sprintf("/api/v1/workboard/tasks/%d/comments", id), "", "")
	if comments, _ := list["comments"].([]any); len(comments) != 1 {
		t.Fatalf("after delete = %v", list)
	}
}

func TestWorkboardComments_Errors(t *testing.T) {
	s := collabGateway(t)
	if st, _ := gatewayJSON(t, s, "POST", "/api/v1/workboard/tasks/9999/comments", "", `{"body":"x"}`); st != 404 {
		t.Fatalf("missing task = %d, want 404", st)
	}
	_, created := gatewayJSON(t, s, "POST", "/api/v1/workboard/tasks", "", `{"title":"x"}`)
	id := int64(created["id"].(float64))
	if st, _ := gatewayJSON(t, s, "POST", fmt.Sprintf("/api/v1/workboard/tasks/%d/comments", id), "", `{"body":"  "}`); st != 400 {
		t.Fatalf("blank body = %d, want 400", st)
	}
	if st, _ := gatewayJSON(t, s, "DELETE", "/api/v1/workboard/comments/9999", "", ""); st != 404 {
		t.Fatalf("delete missing = %d, want 404", st)
	}
}
