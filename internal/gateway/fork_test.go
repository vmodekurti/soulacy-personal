package gateway

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/session"
)

// newTestGatewayWithHistory wires a real SQLite history store (engine +
// server) and seeds one conversation.
func newTestGatewayWithHistory(t *testing.T) (*Server, []session.ConversationEntry) {
	t.Helper()
	s := newTestGateway(t, "secret")
	hs, err := session.NewSQLiteHistoryStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("history store: %v", err)
	}
	t.Cleanup(func() { hs.Close() })
	s.SetHistoryStore(hs)
	s.engine.SetHistoryStore(hs)

	ctx := context.Background()
	for _, e := range []session.ConversationEntry{
		{SessionID: "gui-main", AgentID: "bot", Role: "user", Content: "q1"},
		{SessionID: "gui-main", AgentID: "bot", Role: "assistant", Content: "a1"},
		{SessionID: "gui-main", AgentID: "bot", Role: "user", Content: "q2"},
		{SessionID: "gui-main", AgentID: "bot", Role: "assistant", Content: "a2"},
	} {
		if err := hs.Append(ctx, e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	entries, err := hs.Load(ctx, "gui-main", 0)
	if err != nil || len(entries) != 4 {
		t.Fatalf("seed Load = %d, err=%v", len(entries), err)
	}
	return s, entries
}

func TestForkSession_HappyPath(t *testing.T) {
	s, entries := newTestGatewayWithHistory(t)

	body := fmt.Sprintf(`{"agent_id":"bot","upto_entry_id":%d}`, entries[1].ID)
	status, resp := gatewayJSON(t, s, http.MethodPost, "/api/v1/history/gui-main/fork", "secret", body)
	if status != http.StatusCreated {
		t.Fatalf("fork status = %d body=%v", status, resp)
	}
	newSession, _ := resp["session_id"].(string)
	if newSession == "" || newSession == "gui-main" {
		t.Fatalf("session_id = %q", newSession)
	}
	if resp["copied"] != float64(2) {
		t.Errorf("copied = %v, want 2", resp["copied"])
	}
	es, ok := resp["entries"].([]any)
	if !ok || len(es) != 2 {
		t.Fatalf("entries = %v, want 2 entries", resp["entries"])
	}
	first, _ := es[0].(map[string]any)
	if first["Role"] != "user" && first["role"] != "user" {
		t.Errorf("first forked entry = %v", first)
	}

	// The fork is persisted under the new session id.
	status, hist := gatewayJSON(t, s, http.MethodGet, "/api/v1/history/"+newSession, "secret", "")
	if status != http.StatusOK {
		t.Fatalf("history status = %d", status)
	}
	if got, _ := hist["entries"].([]any); len(got) != 2 {
		t.Errorf("persisted fork = %d entries, want 2", len(got))
	}
}

func TestForkSession_CustomSessionID(t *testing.T) {
	s, entries := newTestGatewayWithHistory(t)
	body := fmt.Sprintf(`{"agent_id":"bot","upto_entry_id":%d,"new_session_id":"my-branch"}`, entries[3].ID)
	status, resp := gatewayJSON(t, s, http.MethodPost, "/api/v1/history/gui-main/fork", "secret", body)
	if status != http.StatusCreated {
		t.Fatalf("status = %d body=%v", status, resp)
	}
	if resp["session_id"] != "my-branch" {
		t.Errorf("session_id = %v", resp["session_id"])
	}
	if resp["copied"] != float64(4) {
		t.Errorf("copied = %v, want 4", resp["copied"])
	}
}

func TestForkSession_Errors(t *testing.T) {
	s, entries := newTestGatewayWithHistory(t)

	// Unknown source session → 404.
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/history/no-such-session/fork", "secret",
		`{"agent_id":"bot","upto_entry_id":1}`)
	if status != http.StatusNotFound {
		t.Errorf("unknown source = %d, want 404", status)
	}

	// Missing upto_entry_id → 400.
	status, _ = gatewayJSON(t, s, http.MethodPost, "/api/v1/history/gui-main/fork", "secret",
		`{"agent_id":"bot"}`)
	if status != http.StatusBadRequest {
		t.Errorf("missing checkpoint = %d, want 400", status)
	}

	// Malformed body → 400.
	status, _ = gatewayJSON(t, s, http.MethodPost, "/api/v1/history/gui-main/fork", "secret", `{nope`)
	if status != http.StatusBadRequest {
		t.Errorf("malformed = %d, want 400", status)
	}

	// Fork into an existing session → 409.
	body := fmt.Sprintf(`{"agent_id":"bot","upto_entry_id":%d,"new_session_id":"gui-main"}`, entries[1].ID)
	status, _ = gatewayJSON(t, s, http.MethodPost, "/api/v1/history/gui-main/fork", "secret", body)
	if status != http.StatusConflict {
		t.Errorf("fork onto source = %d, want 409", status)
	}
}

func TestForkSession_NoStore503(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/history/x/fork", "secret",
		`{"agent_id":"bot","upto_entry_id":1}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
}

func TestHistorySearchRoute(t *testing.T) {
	s, _ := newTestGatewayWithHistory(t)
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/history/search?q=q2&agent_id=bot", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	hits, _ := body["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("hits = %v, want 1", body["hits"])
	}
	first, _ := hits[0].(map[string]any)
	if first["session_id"] != "gui-main" || first["snippet"] == "" {
		t.Fatalf("first hit = %v", first)
	}
}
