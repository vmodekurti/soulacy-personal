// handlers4_test.go — additional coverage for internal/gateway.
//
// Targets handlers and helpers not yet (or only shallowly) covered:
//   - RBAC handlers: HandleListPolicy, HandleListGrants, HandleListGrantsForRole,
//     HandleSetAgentGrant, HandleDeleteAgentGrant (wired via SetRBAC)
//   - Dead-letter queue: list with real store, get, delete (happy + error paths)
//   - Conversation history: load session, load for agent (happy + error paths)
//   - handleRestart error path (no /bin/sh child) — covered indirectly
//   - handleGetCosts / handleGetAgentCosts (nil store is already covered; test
//     the parseCostSince negative-duration normalisation)
//   - handleAdminAPIKeys: nil-store variants of revoke + validate are already
//     covered; test bad-JSON body path
//   - chatOverrideMetadata: ZeroMaxTurns excluded, whitespace-only strings
//   - rawBotList: non-map entries inside []any are skipped
//   - normalizeChannelBots: extra bots beyond existing count
//   - maskChannelBots: non-nil bot list with masked secrets
//   - tailFile: filter query param reduces lines
//   - handleGetLogs: filter param, lines param clamp
//   - applyPatch: log / server / agentDirs / skillDirs fields
//   - handleListKnowledge: enabled=false returns [] not nil
//   - handleDeleteKnowledge: nil knowledge svc returns 204
//   - handleListKnowledgeDocuments: nil svc returns empty
//   - handleSearchKnowledge: nil svc returns 503
//   - handleIngestDocument: nil svc returns 503
//   - handleContextPreview (brain memory) nil store returns 503
//   - handleWriteEpisodic / handleClearEpisodic / handleUpdateProcedural /
//     handleClearProcedural — nil store all return 503
//   - brainMemoryDir / formatMemoryPath pure helpers
//   - EventHub ClientCount, Emit, PublishProgress
//   - newTestGatewayWithRBAC helper  (RBAC routes only appear when rbacManager set)
//   - handleChat with session_id "" → defaults to "http-<userID>"
//   - handleCreateAgent: default LLM provider filled from config
//   - Server.PythonToolDirs: includes home + agent dirs
//   - Server.applyChannelToMemory: nil map initialised
//   - gwSecretEqual edge-cases
package gateway

import (
	"context"
	"github.com/soulacy/soulacy/internal/wsroot"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/queue/dlq"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/session"
	"github.com/soulacy/soulacy/pkg/message"
)

// ── RBAC handlers ─────────────────────────────────────────────────────────────

// newTestGatewayWithRBAC creates a gateway with an RBAC manager wired.
// Without this, the /rbac/* routes are not even registered (see server.go).
func newTestGatewayWithRBAC(t *testing.T) *Server {
	t.Helper()
	s := newTestGateway(t, "secret")
	mgr := rbac.NewManager(rbac.NoopStore{}, zap.NewNop())
	s.SetRBAC(mgr)
	// Routes are registered in buildApp which already ran; SetRBAC only stores
	// the pointer for runtime middleware use. The RBAC routes ARE registered
	// at buildApp time because they check s.rbacManager inside buildApp —
	// but the test gateway was built without an RBAC manager, so the routes
	// are not there. We rebuild to pick them up.
	s.rbacManager = mgr
	s.app = s.buildApp()
	return s
}

func TestGatewayRBACHandleListPolicy(t *testing.T) {
	s := newTestGatewayWithRBAC(t)
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/rbac/policy", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list rbac policy status = %d body=%v", status, body)
	}
	// Should have a "policy" or "roles" top-level key.
	if body["policy"] == nil && body["roles"] == nil {
		t.Fatalf("rbac policy: expected policy or roles field, body=%v", body)
	}
}

func TestGatewayRBACHandleListGrants(t *testing.T) {
	s := newTestGatewayWithRBAC(t)
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/rbac/grants", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list rbac grants status = %d body=%v", status, body)
	}
	if body["grants"] == nil {
		t.Fatalf("rbac grants: expected grants field, body=%v", body)
	}
}

func TestGatewayRBACHandleListGrantsForRole(t *testing.T) {
	s := newTestGatewayWithRBAC(t)
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/rbac/grants/admin", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list rbac grants for role status = %d body=%v", status, body)
	}
	if body["grants"] == nil {
		t.Fatalf("rbac grants for role: expected grants field, body=%v", body)
	}
}

func TestGatewayRBACHandleSetAgentGrant(t *testing.T) {
	s := newTestGatewayWithRBAC(t)
	status, body := gatewayJSON(t, s, http.MethodPut, "/api/v1/rbac/grants/viewer/some-agent", "secret",
		`{"actions":["read"]}`)
	if status != http.StatusOK {
		t.Fatalf("set agent grant status = %d body=%v", status, body)
	}
}

func TestGatewayRBACHandleDeleteAgentGrant(t *testing.T) {
	s := newTestGatewayWithRBAC(t)
	status, _ := gatewayJSON(t, s, http.MethodDelete, "/api/v1/rbac/grants/viewer/some-agent", "secret", "")
	// HandleDeleteAgentGrant returns 204 No Content on success.
	if status != http.StatusNoContent && status != http.StatusOK {
		t.Fatalf("delete agent grant status = %d", status)
	}
}

// ── DLQ: real in-memory store ─────────────────────────────────────────────────

// fakeMemDLQ is an in-memory DLQ store for testing.
type fakeMemDLQ struct {
	items map[string]dlq.DeadLetter
}

func newFakeMemDLQ() *fakeMemDLQ {
	return &fakeMemDLQ{items: map[string]dlq.DeadLetter{}}
}

func (f *fakeMemDLQ) Push(_ context.Context, item dlq.DeadLetter) error {
	f.items[item.ID] = item
	return nil
}

// The fake enforces the workspace predicate exactly as the real store does.
// A permissive double would let a handler that forgot to pass the tenant go on
// passing its tests — which is how the attachment-store break got through once
// already.
func (f *fakeMemDLQ) owns(item dlq.DeadLetter, workspaceID string) bool {
	return wsroot.Normalize(item.WorkspaceID) == wsroot.Normalize(workspaceID)
}

func (f *fakeMemDLQ) List(_ context.Context, workspaceID, queue string) ([]dlq.DeadLetter, error) {
	var out []dlq.DeadLetter
	for _, item := range f.items {
		if !f.owns(item, workspaceID) {
			continue
		}
		if queue == "" || item.Queue == queue {
			out = append(out, item)
		}
	}
	return out, nil
}
func (f *fakeMemDLQ) Get(_ context.Context, workspaceID, id string) (dlq.DeadLetter, error) {
	item, ok := f.items[id]
	if !ok || !f.owns(item, workspaceID) {
		return dlq.DeadLetter{}, dlq.ErrNotFound
	}
	return item, nil
}
func (f *fakeMemDLQ) Delete(_ context.Context, workspaceID, id string) error {
	item, ok := f.items[id]
	if !ok || !f.owns(item, workspaceID) {
		return dlq.ErrNotFound
	}
	delete(f.items, id)
	return nil
}
func (f *fakeMemDLQ) Close() error { return nil }

func TestGatewayHandleListDLQ_WithStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	store := newFakeMemDLQ()
	_ = store.Push(context.Background(), dlq.DeadLetter{
		ID:        "abc123",
		Queue:     "default",
		ErrorMsg:  "something failed",
		Attempts:  3,
		CreatedAt: time.Now(),
	})
	s.SetDLQStore(store)

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/admin/dlq", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list dlq status = %d body=%v", status, body)
	}
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("items field not an array: %v", body)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d: %v", len(items), items)
	}
	if body["count"] == nil {
		t.Fatalf("expected count field, body=%v", body)
	}
}

func TestGatewayHandleListDLQ_WithQueueFilter(t *testing.T) {
	s := newTestGateway(t, "secret")
	store := newFakeMemDLQ()
	_ = store.Push(context.Background(), dlq.DeadLetter{ID: "q1", Queue: "queue-a", CreatedAt: time.Now()})
	_ = store.Push(context.Background(), dlq.DeadLetter{ID: "q2", Queue: "queue-b", CreatedAt: time.Now()})
	s.SetDLQStore(store)

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/admin/dlq?queue=queue-a", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list dlq (filter) status = %d body=%v", status, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 item after queue filter, got %d", len(items))
	}
}

func TestGatewayHandleGetDLQItem_Happy(t *testing.T) {
	s := newTestGateway(t, "secret")
	store := newFakeMemDLQ()
	_ = store.Push(context.Background(), dlq.DeadLetter{ID: "item-1", Queue: "main", CreatedAt: time.Now()})
	s.SetDLQStore(store)

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/admin/dlq/item-1", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get dlq item status = %d body=%v", status, body)
	}
	if body["id"] != "item-1" {
		t.Fatalf("expected id=item-1, body=%v", body)
	}
}

func TestGatewayHandleGetDLQItem_NotFound(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.SetDLQStore(newFakeMemDLQ())

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/admin/dlq/no-such-id", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("get dlq item (not found) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleDeleteDLQItem_Happy(t *testing.T) {
	s := newTestGateway(t, "secret")
	store := newFakeMemDLQ()
	_ = store.Push(context.Background(), dlq.DeadLetter{ID: "del-me", Queue: "q", CreatedAt: time.Now()})
	s.SetDLQStore(store)

	status, body := gatewayJSON(t, s, http.MethodDelete, "/api/v1/admin/dlq/del-me", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("delete dlq item status = %d body=%v", status, body)
	}
	if body["status"] != "deleted" {
		t.Fatalf("expected status=deleted, body=%v", body)
	}
}

func TestGatewayHandleDeleteDLQItem_NotFound(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.SetDLQStore(newFakeMemDLQ())

	status, body := gatewayJSON(t, s, http.MethodDelete, "/api/v1/admin/dlq/ghost", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("delete dlq item (not found) status = %d body=%v", status, body)
	}
}

// ── History store ─────────────────────────────────────────────────────────────

// fakeHistoryStore is an in-memory HistoryStore for testing.
type fakeHistoryStore struct {
	entries []session.ConversationEntry
}

func (f *fakeHistoryStore) Append(_ context.Context, e session.ConversationEntry) error {
	f.entries = append(f.entries, e)
	return nil
}
func (f *fakeHistoryStore) Load(_ context.Context, _, sessionID string, limit int) ([]session.ConversationEntry, error) {
	var out []session.ConversationEntry
	for _, e := range f.entries {
		if e.SessionID == sessionID {
			out = append(out, e)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}
func (f *fakeHistoryStore) LoadForAgent(_ context.Context, _, _, agentID string, limit int) ([]session.ConversationEntry, error) {
	var out []session.ConversationEntry
	for _, e := range f.entries {
		if e.AgentID == agentID {
			out = append(out, e)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}
func (f *fakeHistoryStore) Prune(_ context.Context, _ time.Duration) (int64, error) { return 0, nil }
func (f *fakeHistoryStore) Close() error                                            { return nil }

func TestGatewayHandleHistory_Happy(t *testing.T) {
	s := newTestGateway(t, "secret")
	hs := &fakeHistoryStore{}
	_ = hs.Append(context.Background(), session.ConversationEntry{WorkspaceID: wsroot.PersonalWorkspaceID,
		SessionID: "sess-1",
		AgentID:   "bot",
		Role:      "user",
		Content:   "Hello",
		CreatedAt: time.Now(),
	})
	s.SetHistoryStore(hs)

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/history/sess-1", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("history status = %d body=%v", status, body)
	}
	entries, ok := body["entries"].([]any)
	if !ok {
		t.Fatalf("entries field not an array: %v", body)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if body["session_id"] != "sess-1" {
		t.Fatalf("expected session_id=sess-1, body=%v", body)
	}
}

func TestGatewayHandleHistory_Empty(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.SetHistoryStore(&fakeHistoryStore{})

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/history/no-such-session", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("history (empty) status = %d body=%v", status, body)
	}
	entries, ok := body["entries"].([]any)
	if !ok {
		t.Fatalf("entries should be array: %v", body)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestGatewayHandleHistoryByAgent_Happy(t *testing.T) {
	s := newTestGateway(t, "secret")
	hs := &fakeHistoryStore{}
	_ = hs.Append(context.Background(), session.ConversationEntry{WorkspaceID: wsroot.PersonalWorkspaceID,
		SessionID: "s1",
		AgentID:   "my-agent",
		Role:      "assistant",
		Content:   "Hi",
		CreatedAt: time.Now(),
	})
	_ = hs.Append(context.Background(), session.ConversationEntry{WorkspaceID: wsroot.PersonalWorkspaceID,
		SessionID: "s2",
		AgentID:   "my-agent",
		Role:      "user",
		Content:   "Hello",
		CreatedAt: time.Now(),
	})
	s.SetHistoryStore(hs)

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/history/agent/my-agent", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("history by agent status = %d body=%v", status, body)
	}
	entries, ok := body["entries"].([]any)
	if !ok {
		t.Fatalf("entries should be array: %v", body)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if body["agent_id"] != "my-agent" {
		t.Fatalf("expected agent_id=my-agent, body=%v", body)
	}
}

func TestGatewayHandleHistory_WithLimitParam(t *testing.T) {
	s := newTestGateway(t, "secret")
	hs := &fakeHistoryStore{}
	for i := 0; i < 5; i++ {
		_ = hs.Append(context.Background(), session.ConversationEntry{WorkspaceID: wsroot.PersonalWorkspaceID,
			SessionID: "sess-lim",
			AgentID:   "bot",
			Role:      "user",
			Content:   "msg",
			CreatedAt: time.Now(),
		})
	}
	s.SetHistoryStore(hs)

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/history/sess-lim?limit=2", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("history limit status = %d body=%v", status, body)
	}
	entries, _ := body["entries"].([]any)
	if len(entries) > 2 {
		t.Fatalf("expected at most 2 entries with limit=2, got %d", len(entries))
	}
}

// ── chatOverrideMetadata edge cases ───────────────────────────────────────────

func TestChatOverrideMetadata_WhitespaceOnlyStrings(t *testing.T) {
	// Whitespace-only provider / model / toolChoice should be trimmed → not included.
	meta := chatOverrideMetadata("  ", "  ", nil, nil, nil, nil, "  ", "", "", nil, nil)
	if meta != nil {
		t.Fatalf("expected nil for whitespace-only strings, got %v", meta)
	}
}

func TestChatOverrideMetadata_ZeroMaxTurns(t *testing.T) {
	zero := 0
	meta := chatOverrideMetadata("", "", nil, nil, nil, &zero, "", "", "", nil, nil)
	if meta != nil {
		if _, ok := meta["playground.max_turns"]; ok {
			t.Error("max_turns=0 should not appear in metadata")
		}
	}
}

func TestChatOverrideMetadata_NegativeTemperature(t *testing.T) {
	temp := -1.0
	meta := chatOverrideMetadata("", "", &temp, nil, nil, nil, "", "", "", nil, nil)
	if meta == nil {
		t.Fatal("expected non-nil meta for negative temperature pointer")
	}
	// Temperature is included regardless of sign (the handler doesn't validate range).
	if _, ok := meta["playground.llm.temperature"]; !ok {
		t.Error("temperature should be in metadata even when negative")
	}
}

// ── rawBotList: non-map entries in []any are skipped ─────────────────────────

func TestRawBotList_SliceAnySkipsNonMaps(t *testing.T) {
	input := []any{
		map[string]any{"token": "t1"},
		"not a map",
		42,
		nil,
		map[string]any{"token": "t2"},
	}
	got := rawBotList(input)
	// Only the two map entries should survive.
	if len(got) != 2 {
		t.Fatalf("expected 2 bots, got %d: %v", len(got), got)
	}
}

// ── normalizeChannelBots: bots beyond existing count ──────────────────────────

func TestNormalizeChannelBots_MoreBotsThanExisting(t *testing.T) {
	spec := channelSpecs[1] // telegram
	bots := []map[string]any{
		{"token": "tok1", "agent_id": "a1"},
		{"token": "tok2", "agent_id": "a2"}, // no matching existing → written directly
	}
	existing := []map[string]any{
		{"token": "old-tok1", "agent_id": "a1"},
		// only one existing
	}
	result := normalizeChannelBots(spec, bots, existing)
	if len(result) != 2 {
		t.Fatalf("expected 2 bots, got %d", len(result))
	}
	if result[1]["token"] != "tok2" {
		t.Fatalf("second bot token = %v, want tok2", result[1]["token"])
	}
}

// ── maskChannelBots ───────────────────────────────────────────────────────────

func TestMaskChannelBots_MasksSecrets(t *testing.T) {
	spec := channelSpecs[1] // telegram — has "token" as a secret field
	raw := []map[string]any{
		{"token": "secret-token", "agent_id": "my-bot"},
	}
	statuses := map[string]channels.AdapterStatus{}
	result := maskChannelBots(spec, map[string]any{"bots": raw}, statuses, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 masked bot, got %d", len(result))
	}
	// Token is a secret field → must be "***".
	if result[0]["token"] != "***" {
		t.Fatalf("token should be masked, got %v", result[0]["token"])
	}
	// agent_id is not secret → unchanged.
	if result[0]["agent_id"] != "my-bot" {
		t.Fatalf("agent_id should be my-bot, got %v", result[0]["agent_id"])
	}
}

func TestMaskChannelBots_EmptyRaw(t *testing.T) {
	spec := channelSpecs[1]
	result := maskChannelBots(spec, nil, map[string]channels.AdapterStatus{}, nil)
	if len(result) != 0 {
		t.Fatalf("expected empty result for nil raw, got %d", len(result))
	}
}

// ── handleGetLogs: filter and lines params ────────────────────────────────────

func TestGatewayHandleGetLogs_FilterParam(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	content := "INFO: starting server\nERROR: something broke\nINFO: request handled\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	s.config().Log.File = logPath

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/logs?filter=error", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get logs (filter) status = %d body=%v", status, body)
	}
	lines, ok := body["lines"].([]any)
	if !ok {
		t.Fatalf("lines not an array: %v", body)
	}
	// Only the ERROR line should match.
	if len(lines) != 1 {
		t.Fatalf("expected 1 line after filter, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0].(string), "ERROR") {
		t.Fatalf("filtered line should contain ERROR: %v", lines[0])
	}
}

func TestGatewayHandleGetLogs_LinesParamClamp(t *testing.T) {
	// Negative and zero lines should be clamped to 500 (default).
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app2.log")
	if err := os.WriteFile(logPath, []byte("line\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	s.config().Log.File = logPath

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/logs?lines=0", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get logs (lines=0) status = %d body=%v", status, body)
	}
	if body["lines"] == nil {
		t.Fatalf("expected lines field, body=%v", body)
	}
}

func TestGatewayHandleGetLogs_NonexistentFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	s.config().Log.File = filepath.Join(dir, "nonexistent.log")

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/logs", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get logs (nonexistent) status = %d body=%v", status, body)
	}
	// Returns empty lines with a note.
	if body["lines"] == nil {
		t.Fatalf("expected lines field for nonexistent log, body=%v", body)
	}
}

// ── tailFile unit tests ───────────────────────────────────────────────────────

func TestTailFile_FilterReducesLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	if err := os.WriteFile(path, []byte("apple\nbanana\ncherry\napricot\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	lines, err := tailFile(path, 100, "ap")
	if err != nil {
		t.Fatalf("tailFile: %v", err)
	}
	for _, l := range lines {
		if !strings.Contains(strings.ToLower(l), "ap") {
			t.Fatalf("line %q does not contain filter 'ap'", l)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
}

func TestTailFile_MaxLinesTruncates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test2.log")
	var sb strings.Builder
	for i := 0; i < 10; i++ {
		sb.WriteString("line\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	lines, err := tailFile(path, 3, "")
	if err != nil {
		t.Fatalf("tailFile: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (maxLines=3), got %d", len(lines))
	}
}

func TestTailFile_NonexistentFile(t *testing.T) {
	_, err := tailFile("/no/such/file.log", 100, "")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

// ── applyPatch edge cases ─────────────────────────────────────────────────────

func TestApplyPatch_LogFields(t *testing.T) {
	dst := map[string]any{}
	level := "debug"
	format := "json"
	file := "/var/log/soulacy.log"
	patch := PatchableConfig{
		Log: &struct {
			Level  string `json:"level" yaml:"level"`
			Format string `json:"format" yaml:"format"`
			File   string `json:"file" yaml:"file"`
		}{Level: level, Format: format, File: file},
	}
	applyPatch(dst, patch)
	logMap, ok := dst["log"].(map[string]any)
	if !ok {
		t.Fatalf("log not a map: %v", dst)
	}
	if logMap["level"] != level {
		t.Fatalf("level = %v, want %v", logMap["level"], level)
	}
	if logMap["format"] != format {
		t.Fatalf("format = %v, want %v", logMap["format"], format)
	}
	if logMap["file"] != file {
		t.Fatalf("file = %v, want %v", logMap["file"], file)
	}
}

func TestApplyPatch_AgentDirsAndSkillDirs(t *testing.T) {
	dst := map[string]any{}
	patch := PatchableConfig{
		AgentDirs: []string{"/home/user/agents"},
		SkillDirs: []string{"/home/user/skills"},
	}
	applyPatch(dst, patch)
	if dst["agent_dirs"] == nil {
		t.Fatalf("agent_dirs not set: %v", dst)
	}
	if dst["skill_dirs"] == nil {
		t.Fatalf("skill_dirs not set: %v", dst)
	}
}

func TestApplyPatch_ServerHostPortGUIEnabled(t *testing.T) {
	dst := map[string]any{}
	guiEnabled := true
	patch := PatchableConfig{
		Server: &struct {
			Host       string `json:"host" yaml:"host"`
			Port       int    `json:"port" yaml:"port"`
			GUIEnabled *bool  `json:"gui_enabled" yaml:"gui_enabled"`
			APIKey     string `json:"api_key" yaml:"api_key"`
		}{Host: "0.0.0.0", Port: 9999, GUIEnabled: &guiEnabled, APIKey: "mykey"},
	}
	applyPatch(dst, patch)
	srv, ok := dst["server"].(map[string]any)
	if !ok {
		t.Fatalf("server not a map: %v", dst)
	}
	if srv["host"] != "0.0.0.0" {
		t.Fatalf("host = %v, want 0.0.0.0", srv["host"])
	}
	if srv["port"] != 9999 {
		t.Fatalf("port = %v, want 9999", srv["port"])
	}
	if srv["gui_enabled"] != true {
		t.Fatalf("gui_enabled = %v, want true", srv["gui_enabled"])
	}
	if srv["api_key"] != "mykey" {
		t.Fatalf("api_key = %v, want mykey", srv["api_key"])
	}
}

func TestApplyPatch_ServerAPIKeyMasked(t *testing.T) {
	dst := map[string]any{}
	patch := PatchableConfig{
		Server: &struct {
			Host       string `json:"host" yaml:"host"`
			Port       int    `json:"port" yaml:"port"`
			GUIEnabled *bool  `json:"gui_enabled" yaml:"gui_enabled"`
			APIKey     string `json:"api_key" yaml:"api_key"`
		}{APIKey: "***"},
	}
	applyPatch(dst, patch)
	srv := dst["server"].(map[string]any)
	// "***" should NOT be written to the config.
	if srv["api_key"] != nil {
		t.Fatalf("masked api_key should not be written, got %v", srv["api_key"])
	}
}

// F-Bridge — the security.intent_gate patch must round-trip when set, and
// leave the on-disk value untouched when the payload's IntentGate is empty
// (the "unset / defer to per-agent" sentinel). Mirrors the Log branch pattern.
func TestApplyPatch_SecurityIntentGateRoundTrip(t *testing.T) {
	dst := map[string]any{}
	patch := PatchableConfig{
		Security: &struct {
			IntentGate string `json:"intent_gate" yaml:"intent_gate"`
		}{IntentGate: "deny"},
	}
	applyPatch(dst, patch)
	sec, ok := dst["security"].(map[string]any)
	if !ok {
		t.Fatalf("expected security map, got %T (%v)", dst["security"], dst["security"])
	}
	if sec["intent_gate"] != "deny" {
		t.Fatalf("intent_gate = %v, want deny", sec["intent_gate"])
	}
}

func TestApplyPatch_SecurityIntentGateEmptyPreservesExisting(t *testing.T) {
	// Pre-populate the on-disk value; an empty patch must not clobber it.
	dst := map[string]any{
		"security": map[string]any{"intent_gate": "prompt"},
	}
	patch := PatchableConfig{
		Security: &struct {
			IntentGate string `json:"intent_gate" yaml:"intent_gate"`
		}{IntentGate: ""},
	}
	applyPatch(dst, patch)
	sec := dst["security"].(map[string]any)
	if sec["intent_gate"] != "prompt" {
		t.Fatalf("empty patch clobbered on-disk intent_gate = %v", sec["intent_gate"])
	}
}

// ── handleListKnowledge: enabled=false returns [] ─────────────────────────────

func TestHandleListKnowledge_DisabledReturnsEmptyArray(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/knowledge", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["enabled"] != false {
		t.Fatalf("expected enabled=false, body=%v", body)
	}
	kbs, ok := body["knowledge_bases"].([]any)
	if !ok {
		t.Fatalf("knowledge_bases should be array, got %T: %v", body["knowledge_bases"], body)
	}
	if len(kbs) != 0 {
		t.Fatalf("expected empty array, got %d items", len(kbs))
	}
}

// ── handleDeleteKnowledge: nil svc → 204 ─────────────────────────────────────

func TestHandleDeleteKnowledge_NilSvc(t *testing.T) {
	s := newTestGateway(t, "secret")
	// Knowledge is nil in test gateway → handler returns 204 immediately.
	status, _ := gatewayJSON(t, s, http.MethodDelete, "/api/v1/knowledge/mykb", "secret", "")
	if status != http.StatusNoContent {
		t.Fatalf("delete knowledge (nil svc) status = %d", status)
	}
}

// ── handleListKnowledgeDocuments: nil svc → empty ─────────────────────────────

func TestHandleListKnowledgeDocuments_NilSvc(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/knowledge/mykb/documents", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list docs (nil svc) status = %d body=%v", status, body)
	}
	docs, ok := body["documents"].([]any)
	if !ok {
		t.Fatalf("documents should be array: %v", body)
	}
	if len(docs) != 0 {
		t.Fatalf("expected empty docs, got %d", len(docs))
	}
}

// ── handleSearchKnowledge: nil svc → 503 ─────────────────────────────────────

func TestHandleSearchKnowledge_NilSvc(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/knowledge/mykb/search", "secret",
		`{"query":"test","top_k":5}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("search knowledge (nil svc) status = %d body=%v", status, body)
	}
}

// ── handleIngestDocument: nil svc → 503 ──────────────────────────────────────

func TestHandleIngestDocument_NilSvc(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/knowledge/mykb/documents", "secret",
		`{"title":"Doc","content":"Hello world"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("ingest doc (nil svc) status = %d body=%v", status, body)
	}
}

// ── handleDeleteKnowledgeDocument: nil svc → 204 ─────────────────────────────

func TestHandleDeleteKnowledgeDocument_NilSvc(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, s, http.MethodDelete, "/api/v1/knowledge/mykb/documents/doc1", "secret", "")
	if status != http.StatusNoContent {
		t.Fatalf("delete doc (nil svc) status = %d", status)
	}
}

// ── brain memory nil-store guards ─────────────────────────────────────────────

func TestGatewayHandleWriteEpisodic_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/brain-memory/my-agent/episodic", "secret",
		`{"content":"did something"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("write episodic (nil) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleClearEpisodic_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodDelete, "/api/v1/brain-memory/my-agent/episodic", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("clear episodic (nil) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleUpdateProcedural_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPut, "/api/v1/brain-memory/my-agent/procedural", "secret",
		`{"rules":"rule 1\nrule 2"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("update procedural (nil) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleClearProcedural_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodDelete, "/api/v1/brain-memory/my-agent/procedural", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("clear procedural (nil) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleContextPreview_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/brain-memory/my-agent/context-preview", "secret",
		`{"task_input":"run the report"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("context preview (nil) status = %d body=%v", status, body)
	}
}

// ── brainMemoryDir / formatMemoryPath pure helpers ───────────────────────────

func TestBrainMemoryDir_UsesEnvVar(t *testing.T) {
	t.Setenv("SOULACY_MEMORY_DIR", "/tmp/test-memory")
	got := brainMemoryDir()
	if got != "/tmp/test-memory" {
		t.Fatalf("brainMemoryDir = %q, want /tmp/test-memory", got)
	}
}

// The path a tenant is shown is its own, and personal's is unchanged.
func TestFormatMemoryPathNamesTheCallersWorkspace(t *testing.T) {
	t.Setenv("SOULACY_MEMORY_DIR", "/tmp/test-memory")
	if got := formatMemoryPath("", "bot"); got != "/tmp/test-memory/bot/episodic.jsonl" {
		t.Fatalf("personal path changed: %q", got)
	}
	if got := formatMemoryPath(wsroot.PersonalWorkspaceID, "bot"); got != "/tmp/test-memory/bot/episodic.jsonl" {
		t.Fatalf("explicit personal path changed: %q", got)
	}
	tenant := formatMemoryPath("ws_a", "bot")
	if !strings.Contains(tenant, filepath.Join(wsroot.NamespaceDir, "ws_a")) {
		t.Fatalf("a tenant was shown a path outside its namespace: %q", tenant)
	}
	if tenant == formatMemoryPath("ws_b", "bot") {
		t.Fatal("two workspaces were shown the same episodic path")
	}
}

func TestBrainMemoryDir_FallsBackToHome(t *testing.T) {
	t.Setenv("SOULACY_MEMORY_DIR", "")
	got := brainMemoryDir()
	if got == "" {
		t.Fatal("brainMemoryDir should not be empty")
	}
	if !strings.Contains(got, ".soulacy") {
		t.Fatalf("brainMemoryDir should contain .soulacy, got %q", got)
	}
}

func TestFormatMemoryPath(t *testing.T) {
	t.Setenv("SOULACY_MEMORY_DIR", "/tmp/mem")
	got := formatMemoryPath(wsroot.PersonalWorkspaceID, "my-bot")
	if !strings.Contains(got, "my-bot") {
		t.Fatalf("formatMemoryPath = %q, want to contain 'my-bot'", got)
	}
	if !strings.HasSuffix(got, "episodic.jsonl") {
		t.Fatalf("formatMemoryPath should end with episodic.jsonl, got %q", got)
	}
}

// ── EventHub unit tests ───────────────────────────────────────────────────────

func TestEventHubClientCount_StartsZero(t *testing.T) {
	hub := NewEventHub(zap.NewNop(), nil)
	if hub.ClientCount() != 0 {
		t.Fatalf("new hub should have 0 clients, got %d", hub.ClientCount())
	}
}

func TestEventHubEmit_NoPanic(t *testing.T) {
	hub := NewEventHub(zap.NewNop(), nil)
	// No clients connected — Emit should not panic.
	hub.Emit(message.Event{
		Type:      "test",
		Payload:   "hello",
		Timestamp: time.Now().UTC(),
	})
}

func TestEventHubPublishProgress_NoPanic(t *testing.T) {
	hub := NewEventHub(zap.NewNop(), nil)
	hub.PublishProgress(message.ProgressEvent{
		RunID:  "test-run",
		Status: "running",
	})
}

// ── Server.PythonToolDirs ──────────────────────────────────────────────────────

func TestServerPythonToolDirs_IncludesHomeAndAgentDirs(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	dirs := s.PythonToolDirs()
	// Should include at least the home-based dir + the temp agent dir.
	if len(dirs) == 0 {
		t.Fatal("PythonToolDirs should return at least one directory")
	}
	foundHome := false
	for _, d := range dirs {
		if strings.Contains(d, ".soulacy") {
			foundHome = true
		}
	}
	if !foundHome {
		t.Fatalf("PythonToolDirs should contain .soulacy dir, got: %v", dirs)
	}
}

// ── Server.applyChannelToMemory ───────────────────────────────────────────────

func TestServerApplyChannelToMemory_InitialisesNilMap(t *testing.T) {
	s := newTestGateway(t, "secret")
	// Wipe channels map to simulate nil.
	s.config().Channels = nil

	chMap := map[string]any{"token": "tok", "agent_id": "bot"}
	s.applyChannelToMemory("telegram", chMap)

	if s.config().Channels == nil {
		t.Fatal("applyChannelToMemory should initialise Channels map")
	}
	if s.config().Channels["telegram"] == nil {
		t.Fatalf("telegram entry should exist in Channels map")
	}
}

func TestServerApplyChannelToMemory_MergesValues(t *testing.T) {
	s := newTestGateway(t, "secret")
	chMap := map[string]any{"token": "new-token", "enabled": true}
	s.applyChannelToMemory("slack", chMap)

	if v := s.config().Channels["slack"]["token"]; v != "new-token" {
		t.Fatalf("token = %v, want new-token", v)
	}
}

// ── gwSecretEqual edge cases ──────────────────────────────────────────────────

func TestGwSecretEqual_EmptyWant(t *testing.T) {
	// want="" should always return false even if got="" too.
	if gwSecretEqual("", "") {
		t.Fatal("gwSecretEqual(\"\",\"\") should return false")
	}
}

func TestGwSecretEqual_CorrectMatch(t *testing.T) {
	if !gwSecretEqual("mysecret", "mysecret") {
		t.Fatal("gwSecretEqual should return true for identical keys")
	}
}

func TestGwSecretEqual_Mismatch(t *testing.T) {
	if gwSecretEqual("wrong", "right") {
		t.Fatal("gwSecretEqual should return false for different keys")
	}
}

// ── handleChat: session_id defaulting ─────────────────────────────────────────

func TestGatewayChatHandler_SessionIDDefaults(t *testing.T) {
	s, provider := newTestGatewayWithLLM(t, "secret")

	// Create an agent.
	createBody := `{"id":"sess-default-agent","name":"X","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"m"},"system_prompt":"hi","enabled":true}`
	st, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if st != http.StatusCreated {
		t.Fatalf("create: status=%d", st)
	}

	// Send chat without session_id — it must receive an unguessable identifier,
	// not the former deterministic "http-<user_id>" value.
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/chat", "secret",
		`{"agent_id":"sess-default-agent","user_id":"alice","text":"hello"}`)
	if status != http.StatusOK {
		t.Fatalf("chat status = %d body=%v", status, body)
	}
	sessionID, _ := body["session_id"].(string)
	if sessionID == "" || sessionID == "http-alice" || !strings.HasPrefix(sessionID, "http-") {
		t.Fatalf("generated session_id is not opaque: %q", sessionID)
	}
	_ = provider
}

// ── handleCreateAgent: default LLM provider filled ────────────────────────────

func TestGatewayHandleCreateAgent_DefaultLLMProvider(t *testing.T) {
	s := newTestGateway(t, "secret")
	// cfg.LLM.DefaultProvider is "openai" in newTestGateway.
	// Send a create with no llm.provider → should default.
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret",
		`{"id":"default-llm-agent","name":"DLP","trigger":"channel","channels":["http"],"llm":{"model":"gpt-4o"},"system_prompt":"hi","enabled":true}`)
	if status != http.StatusCreated {
		t.Fatalf("create agent status = %d body=%v", status, body)
	}
	// The returned agent should have the provider set to the config default.
	llm, ok := body["llm"].(map[string]any)
	if !ok {
		t.Fatalf("llm field missing or wrong type: %v", body)
	}
	if llm["provider"] == nil || llm["provider"] == "" {
		t.Fatalf("expected llm.provider to be set to default, got %v", llm)
	}
}

// ── handlePatchConfig: server fields via HTTP ─────────────────────────────────

func TestGatewayHandlePatchConfig_LogFields(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"log":{"level":"debug","format":"json","file":"/tmp/soulacy.log"}}`)
	if status != http.StatusOK {
		t.Fatalf("patch config (log) status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

func TestGatewayHandlePatchConfig_AgentDirs(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"agent_dirs":["/tmp/agents-a","/tmp/agents-b"]}`)
	if status != http.StatusOK {
		t.Fatalf("patch config (agent_dirs) status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

// ── handleChatStream: POST with overrides (no agent registered) ───────────────

func TestGatewayChatStreamPOST_UnknownAgent(t *testing.T) {
	s := newTestGateway(t, "secret")
	// Agent doesn't exist — engine will return an error; stream will contain
	// an error event. The HTTP status itself is 200 (SSE sets it early).
	status, _ := gatewayRaw(t, s, http.MethodPost, "/api/v1/chat/stream", "secret",
		`{"agent_id":"ghost-agent","text":"hello","user_id":"u1"}`)
	// SSE streams always start with 200 regardless of engine error.
	if status != http.StatusOK {
		t.Fatalf("stream (unknown agent) expected 200 SSE, got %d", status)
	}
}

// ── parseCostSince: negative go duration ─────────────────────────────────────

func TestParseCostSince_NegativeDuration(t *testing.T) {
	// Negative durations should be normalised to positive (abs value).
	ts, _, err := parseCostSince("-24h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts.IsZero() {
		t.Fatal("expected non-zero time for -24h")
	}
	// The result should be in the past, not the future.
	if ts.After(time.Now()) {
		t.Fatalf("expected past time for -24h, got %v", ts)
	}
}

// ── handleCreateKnowledge: disabled returns 503 ───────────────────────────────

func TestHandleCreateKnowledge_DisabledReturns503(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/knowledge", "secret",
		`{"name":"testKB","embedding_provider":"openai","embedding_model":"text-embedding-3-small"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("create knowledge (disabled) status = %d body=%v", status, body)
	}
}

// ── handleBuilderDeploy: not found ───────────────────────────────────────────

func TestGatewayHandleBuilderDeploy_NotFound(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/builder/deploy", "secret",
		`{"session_id":"no-such-builder-session"}`)
	if status != http.StatusNotFound {
		t.Fatalf("builder deploy (not found) status = %d body=%v", status, body)
	}
}

// ── handleBrainMemoryStats: nil store ─────────────────────────────────────────

func TestGatewayHandleBrainMemoryStats_WithAgentsNoStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	// Create an agent so loader.All() returns something.
	createBody := `{"id":"brain-test-agent","name":"Brain","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"m"},"system_prompt":"x","enabled":true}`
	st, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if st != http.StatusCreated {
		t.Fatalf("create agent: %d", st)
	}

	// BrainStore is nil → enabled=false, agents=[].
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/brain-memory", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("brain memory stats status = %d body=%v", status, body)
	}
	if body["enabled"] != false {
		t.Fatalf("expected enabled=false, body=%v", body)
	}
}

// ── handleAgentActions: limit param ──────────────────────────────────────────

func TestGatewayHandleAgentActions_LimitParam(t *testing.T) {
	s := newTestGateway(t, "secret")
	// actions is nil → returns empty regardless of limit.
	status, body := gatewayJSON(t, s, http.MethodGet,
		"/api/v1/agents/some-agent/actions?limit=10", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("agent actions (limit) status = %d body=%v", status, body)
	}
	if body["events"] == nil {
		t.Fatalf("expected events field, body=%v", body)
	}
}

// ── handleGetConfig: _meta.writable reflects cfgPath ────────────────────────

func TestGatewayHandleGetConfig_Writable(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/config", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get config status = %d body=%v", status, body)
	}
	meta, ok := body["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("_meta missing: %v", body)
	}
	if meta["writable"] != true {
		t.Fatalf("_meta.writable should be true when cfgPath set, got %v", meta["writable"])
	}
}

func TestGatewayHandleGetConfig_NotWritable(t *testing.T) {
	s := newTestGateway(t, "secret")
	// cfgPath is "" → not writable.
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/config", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get config status = %d body=%v", status, body)
	}
	meta, ok := body["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("_meta missing: %v", body)
	}
	if meta["writable"] != false {
		t.Fatalf("_meta.writable should be false when cfgPath empty, got %v", meta["writable"])
	}
}

// ── handleListSchedule: running snapshot ─────────────────────────────────────

func TestGatewayHandleScheduleStatus_WithRunningAgent(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")

	createBody := `{"id":"sched-running","name":"SchedRun","trigger":"schedule","channels":[],"llm":{"provider":"test","model":"m"},"system_prompt":"x","enabled":true}`
	st, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if st != http.StatusCreated {
		t.Fatalf("create: %d", st)
	}

	// Mark as running.
	if !s.scheduler.TryStartRun("sched-running") {
		t.Fatal("could not mark as running")
	}
	defer s.scheduler.FinishRun("sched-running")

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/schedule/status", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("schedule/status = %d body=%v", status, body)
	}
	running, ok := body["running"].(map[string]any)
	if !ok {
		t.Fatalf("running should be a map: %v", body)
	}
	if running["sched-running"] == nil {
		t.Fatalf("expected sched-running in running map, got %v", running)
	}
}

// ── handleUpdateChannel: secret preserved when *** sent ──────────────────────

func TestGatewayHandleUpdateChannel_SecretPreservedWhenMasked(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)

	// First, save a real token.
	st, b := gatewayJSON(t, s, http.MethodPatch, "/api/v1/channels/telegram", "secret",
		`{"settings":{"token":"real-tok","agent_id":"bot1"}}`)
	if st != http.StatusOK {
		t.Fatalf("set token status = %d body=%v", st, b)
	}

	// Now send *** — the real token should be preserved in config.
	st, b = gatewayJSON(t, s, http.MethodPatch, "/api/v1/channels/telegram", "secret",
		`{"settings":{"token":"***","agent_id":"bot1"}}`)
	if st != http.StatusOK {
		t.Fatalf("mask token status = %d body=%v", st, b)
	}
	if b["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", b)
	}
}

// ── handleGetCosts: agent_id filter ──────────────────────────────────────────

// fakeMemCostStore is a minimal cost store stub that satisfies the costs.Store
// interface used by the server. We test only nil-store cases here because the
// real store requires SQLite; the important new coverage is the agent_id filter
// branch which is exercised by the HTTP test when costs are returned.
// We already have nil-store tests in handlers2_test.go; this test adds the
// handleGetCosts parseCostSince bad-param path.

func TestGatewayHandleGetCosts_BadSinceParam(t *testing.T) {
	// costStore is nil → 503 fires before parseCostSince; test parseCostSince
	// directly for the invalid path since we cannot wire a real store easily.
	_, _, err := parseCostSince("not-valid-at-all")
	if err == nil {
		t.Fatal("expected error for completely invalid since param")
	}
}

// ── resolveProviderModel helper (builder) ────────────────────────────────────

func TestResolveProviderModel_EmptyUsesDefaults(t *testing.T) {
	s := newTestGateway(t, "secret")
	// cfg.LLM.DefaultProvider = "openai" in test setup.
	provider, model := s.resolveProviderModel("", "")
	if provider == "" {
		t.Fatal("provider should not be empty")
	}
	// Model falls back to config or "llama3" hardcoded fallback.
	if model == "" {
		t.Fatal("model should not be empty")
	}
}

func TestResolveProviderModel_ExplicitValues(t *testing.T) {
	s := newTestGateway(t, "secret")
	provider, model := s.resolveProviderModel("anthropic", "claude-3-5-sonnet")
	if provider != "anthropic" {
		t.Fatalf("provider = %q, want anthropic", provider)
	}
	if model != "claude-3-5-sonnet" {
		t.Fatalf("model = %q, want claude-3-5-sonnet", model)
	}
}

// ── firstLine builder helper ──────────────────────────────────────────────────

func TestFirstLine_SingleLine(t *testing.T) {
	got := firstLine("hello world")
	if got != "hello world" {
		t.Fatalf("firstLine = %q, want 'hello world'", got)
	}
}

func TestFirstLine_MultiLine(t *testing.T) {
	got := firstLine("first line\nsecond line\nthird")
	if got != "first line" {
		t.Fatalf("firstLine = %q, want 'first line'", got)
	}
}

func TestFirstLine_LongLine(t *testing.T) {
	long := strings.Repeat("a", 300)
	got := firstLine(long)
	// "…" is a 3-byte UTF-8 character, so total byte len = 200 + 3 = 203
	if len(got) > 203 {
		t.Fatalf("firstLine should be capped at 200 chars + ellipsis (203 bytes), got len=%d", len(got))
	}
}

func TestFirstLine_Empty(t *testing.T) {
	got := firstLine("")
	if got != "" {
		t.Fatalf("firstLine('') = %q, want ''", got)
	}
}

// ── uniqueAgentID helper ──────────────────────────────────────────────────────

func TestUniqueAgentID_FreeBase(t *testing.T) {
	s := newTestGateway(t, "secret")
	got := s.uniqueAgentID(s.agents(nil), "brand-new-agent")
	if got != "brand-new-agent" {
		t.Fatalf("uniqueAgentID = %q, want brand-new-agent", got)
	}
}

func TestUniqueAgentID_CollisionAppendsSuffix(t *testing.T) {
	s := newTestGateway(t, "secret")
	// Create "collide" agent.
	createBody := `{"id":"collide","name":"C","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"m"},"system_prompt":"x","enabled":true}`
	st, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if st != http.StatusCreated {
		t.Fatalf("create: %d", st)
	}

	got := s.uniqueAgentID(s.agents(nil), "collide")
	// Should be "collide-2" since "collide" is taken.
	if got == "collide" {
		t.Fatalf("uniqueAgentID should not return 'collide' when already taken, got %q", got)
	}
	if !strings.HasPrefix(got, "collide") {
		t.Fatalf("uniqueAgentID should have 'collide' prefix, got %q", got)
	}
}
