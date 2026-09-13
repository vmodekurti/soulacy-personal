package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/safeundo"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestSafeUndoToolsAreExplicitReviewedAndNeverExecute(t *testing.T) {
	var reads, writes atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes.Add(1)
			t.Error("unexpected external write")
			w.WriteHeader(500)
			return
		}
		reads.Add(1)
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "Private before value")
	}))
	defer remote.Close()
	def := checkedDefinition()
	def.Mission = nil
	def.Builtins = strListPtr("safe_undo.resources", "safe_undo.prepare")
	e, _ := newHandleTestEngine(t, def)
	cfg := safeundo.Config{Resources: []safeundo.Resource{{ID: "document", AgentID: def.ID, Name: "Document", Kind: "webdav_text", URL: remote.URL, ConditionalWrites: true, AllowLoopbackHTTP: true}}}
	store, err := safeundo.NewStore(filepath.Join(t.TempDir(), "undo.db"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e.SetSafeUndo(store, true)
	inbound := testUserMessage(def.ID, "s", "prepare")
	ctx := context.WithValue(WithPrincipal(context.Background(), Principal{Subject: "alice", Role: "operator"}), inboundMsgKey{}, inbound)
	ctx = llm.WithCallMetadata(ctx, llm.CallMetadata{RunID: "run-123"})
	call := message.ToolCall{ID: "tool", Name: "safe_undo.prepare", Arguments: map[string]any{"title": "Edit document", "changes": []any{map[string]any{"resource_id": "document", "text": "After"}}}}
	schemas := e.allToolSchemasForContext(ctx, def, "http")
	if !toolSchemasContain(schemas, "safe_undo.prepare") || toolSchemasContain(schemas, "safe_undo.execute") || toolSchemasContain(schemas, "safe_undo.undo") {
		t.Fatal("unsafe schema catalog")
	}
	raw, err := e.runToolDispatch(ctx, def, "s", call)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if json.Unmarshal([]byte(raw), &result) != nil || result["applied"] != false || result["review_required"] != true || writes.Load() != 0 || reads.Load() != 1 {
		t.Fatal(raw, reads.Load(), writes.Load())
	}
	if strings.Contains(raw, "Private before value") || strings.Contains(raw, remote.URL) {
		t.Fatal("private review content sent to model")
	}
	j, err := store.Get(ctx, "alice", def.ID, result["job_id"].(string))
	if err != nil || j.RunID != "run-123" || j.Status != "draft" {
		t.Fatal(j, err)
	}
	for _, allowlist := range []*[]string{nil, strListPtr(), strListPtr("*"), strListPtr("all"), strListPtr("safe_undo.*")} {
		copy := *def
		copy.Builtins = allowlist
		if toolSchemasContain(e.allToolSchemasForContext(ctx, &copy, "http"), "safe_undo.prepare") {
			t.Fatal("wildcard opts in")
		}
		if _, err := e.runToolDispatch(ctx, &copy, "s", call); err == nil {
			t.Fatal("missing explicit opt-in allowed")
		}
	}
	for _, principal := range []Principal{{Subject: "reader", Role: "viewer"}, {Subject: "limited", Role: "operator", Scopes: []string{"chat"}}, {Subject: "", Role: "admin"}} {
		blocked := WithPrincipal(ctx, principal)
		if _, err := e.runToolDispatch(blocked, def, "s", call); err == nil {
			t.Fatal("principal bypass", principal)
		}
	}
	for _, channel := range []string{"telegram", "scheduler"} {
		m := inbound
		m.Channel = channel
		m.UserID = "admin"
		blocked := context.WithValue(context.Background(), inboundMsgKey{}, m)
		if _, err := e.runToolDispatch(blocked, def, "s", call); err == nil {
			t.Fatal("invented channel identity accepted")
		}
	}
	e.SetSafeUndo(store, false)
	if _, err := e.runToolDispatch(ctx, def, "s", call); err == nil {
		t.Fatal("unauthenticated gateway enabled")
	}
	e.SetSafeUndo(store, true)
	before := reads.Load()
	if _, err := e.runToolDispatch(WithDryRun(ctx, true), def, "s", call); err != nil {
		t.Fatal(err)
	}
	if reads.Load() != before || writes.Load() != 0 {
		t.Fatal("simulation used real resources")
	}
	jobs, err := store.List(ctx, "alice", def.ID)
	if err != nil || len(jobs) != 1 {
		t.Fatal("simulation prepared another job", jobs, err)
	}
}
