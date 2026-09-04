// history_test.go — tests for the session history store.
package session

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// NoopHistoryStore
// ---------------------------------------------------------------------------

func TestNoopHistoryStoreAllMethodsReturnNil(t *testing.T) {
	var s NoopHistoryStore
	ctx := context.Background()

	if err := s.Append(ctx, ConversationEntry{SessionID: "s1", Role: "user", Content: "hi"}); err != nil {
		t.Errorf("Append: %v", err)
	}
	entries, err := s.Load(ctx, "s1", 10)
	if err != nil || entries != nil {
		t.Errorf("Load: entries=%v err=%v", entries, err)
	}
	entries, err = s.LoadForAgent(ctx, "ag", 10)
	if err != nil || entries != nil {
		t.Errorf("LoadForAgent: entries=%v err=%v", entries, err)
	}
	n, err := s.Prune(ctx, time.Hour)
	if err != nil || n != 0 {
		t.Errorf("Prune: n=%d err=%v", n, err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SQLiteHistoryStore helpers
// ---------------------------------------------------------------------------

func newHistoryStore(t *testing.T) *SQLiteHistoryStore {
	t.Helper()
	s, err := NewSQLiteHistoryStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("NewSQLiteHistoryStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func appendEntry(t *testing.T, s *SQLiteHistoryStore, sessionID, agentID, role, content string) {
	t.Helper()
	if err := s.Append(context.Background(), ConversationEntry{
		SessionID: sessionID, AgentID: agentID, Role: role, Content: content,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

// ---------------------------------------------------------------------------
// NewSQLiteHistoryStore
// ---------------------------------------------------------------------------

func TestNewSQLiteHistoryStoreCreates(t *testing.T) {
	s := newHistoryStore(t)
	if s == nil {
		t.Fatal("NewSQLiteHistoryStore returned nil")
	}
}

func TestNewSQLiteHistoryStoreBadPath(t *testing.T) {
	_, err := NewSQLiteHistoryStore("/no/such/dir/history.db")
	if err == nil {
		t.Fatal("expected error for bad path, got nil")
	}
}

// ---------------------------------------------------------------------------
// Append / Load
// ---------------------------------------------------------------------------

func TestAppendAndLoadAllEntries(t *testing.T) {
	s := newHistoryStore(t)
	appendEntry(t, s, "sess-1", "ag", "user", "hello")
	appendEntry(t, s, "sess-1", "ag", "assistant", "hi there")

	entries, err := s.Load(context.Background(), "sess-1", 0)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Load count = %d, want 2", len(entries))
	}
	if entries[0].Role != "user" || entries[0].Content != "hello" {
		t.Errorf("entry 0: %+v", entries[0])
	}
	if entries[1].Role != "assistant" {
		t.Errorf("entry 1: %+v", entries[1])
	}
}

func TestLoadWithLimit(t *testing.T) {
	s := newHistoryStore(t)
	for i := 0; i < 5; i++ {
		appendEntry(t, s, "sess-2", "ag", "user", "msg")
	}

	entries, err := s.Load(context.Background(), "sess-2", 3)
	if err != nil {
		t.Fatalf("Load limit: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("Load limit count = %d, want 3", len(entries))
	}
}

func TestLoadEmptySession(t *testing.T) {
	s := newHistoryStore(t)
	entries, err := s.Load(context.Background(), "no-such-session", 10)
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestLoadDifferentSessionsIsolated(t *testing.T) {
	s := newHistoryStore(t)
	appendEntry(t, s, "sess-a", "ag", "user", "session a")
	appendEntry(t, s, "sess-b", "ag", "user", "session b")

	entriesA, _ := s.Load(context.Background(), "sess-a", 0)
	if len(entriesA) != 1 || entriesA[0].Content != "session a" {
		t.Errorf("sess-a entries: %+v", entriesA)
	}
}

// ---------------------------------------------------------------------------
// LoadForAgent
// ---------------------------------------------------------------------------

func TestLoadForAgentReturnsAllSessions(t *testing.T) {
	s := newHistoryStore(t)
	appendEntry(t, s, "s1", "my-agent", "user", "first session")
	appendEntry(t, s, "s2", "my-agent", "user", "second session")
	appendEntry(t, s, "s3", "other-agent", "user", "different agent")

	entries, err := s.LoadForAgent(context.Background(), "my-agent", 0)
	if err != nil {
		t.Fatalf("LoadForAgent: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("LoadForAgent count = %d, want 2", len(entries))
	}
}

func TestLoadForAgentWithLimit(t *testing.T) {
	s := newHistoryStore(t)
	for i := 0; i < 10; i++ {
		appendEntry(t, s, "s1", "heavy-agent", "user", "msg")
	}
	entries, err := s.LoadForAgent(context.Background(), "heavy-agent", 3)
	if err != nil {
		t.Fatalf("LoadForAgent limit: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("LoadForAgent limit count = %d, want 3", len(entries))
	}
}

func TestLoadForAgentZeroLimitCapAt1000(t *testing.T) {
	// Just verify it doesn't error with limit=0 (capped to 1000 internally).
	s := newHistoryStore(t)
	appendEntry(t, s, "s1", "ag", "user", "msg")
	entries, err := s.LoadForAgent(context.Background(), "ag", 0)
	if err != nil {
		t.Fatalf("LoadForAgent zero limit: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}

func TestSQLiteHistoryStoreSearch(t *testing.T) {
	s := newHistoryStore(t)
	ctx := context.Background()
	appendEntry(t, s, "sess-a", "agent-a", "user", "Find stock momentum breakouts for tomorrow")
	appendEntry(t, s, "sess-a", "agent-a", "assistant", "I found a momentum screen with AAPL and MSFT.")
	appendEntry(t, s, "sess-b", "agent-b", "user", "Summarize weather in Chicago")

	hits, err := s.Search(ctx, "agent-a", "momentum screen", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %#v, want 1", hits)
	}
	if hits[0].SessionID != "sess-a" || hits[0].Snippet == "" {
		t.Fatalf("hit = %#v, want sess-a with snippet", hits[0])
	}

	hits, err = s.Search(ctx, "", "Chicago", 10)
	if err != nil {
		t.Fatalf("Search all: %v", err)
	}
	if len(hits) != 1 || hits[0].AgentID != "agent-b" {
		t.Fatalf("all-agent hits = %#v, want agent-b", hits)
	}
}

// ---------------------------------------------------------------------------
// Prune
// ---------------------------------------------------------------------------

func TestPruneRemovesOldEntries(t *testing.T) {
	s := newHistoryStore(t)

	// Insert with current time (will be kept).
	appendEntry(t, s, "keep-sess", "ag", "user", "recent")

	// Insert a row with a very old timestamp directly via the DB.
	old := time.Now().UTC().Add(-48 * time.Hour).Format("2006-01-02 15:04:05")
	_, err := s.db.Exec(
		`INSERT INTO conversation_history (session_id, agent_id, role, content, tokens, created_at)
		 VALUES ('old-sess', 'ag', 'user', 'old content', 0, ?)`, old)
	if err != nil {
		t.Fatalf("insert old row: %v", err)
	}

	n, err := s.Prune(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Errorf("Prune deleted %d rows, want 1", n)
	}
}

func TestPruneEmptyDB(t *testing.T) {
	s := newHistoryStore(t)
	n, err := s.Prune(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("Prune empty: %v", err)
	}
	if n != 0 {
		t.Errorf("Prune empty: deleted %d rows, want 0", n)
	}
}

// ---------------------------------------------------------------------------
// Additional SQLiteHistoryStore tests
// ---------------------------------------------------------------------------

func TestAppend_WithTokens(t *testing.T) {
	s := newHistoryStore(t)
	if err := s.Append(context.Background(), ConversationEntry{
		SessionID: "s1", AgentID: "ag", Role: "assistant",
		Content: "response", Tokens: 42,
	}); err != nil {
		t.Fatalf("Append with tokens: %v", err)
	}
	entries, err := s.Load(context.Background(), "s1", 0)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Tokens != 42 {
		t.Errorf("Tokens: got %d, want 42", entries[0].Tokens)
	}
}

func TestLoad_OldestFirstWithLimit(t *testing.T) {
	s := newHistoryStore(t)
	// Append 5 entries: 0..4
	for i := 0; i < 5; i++ {
		appendEntry(t, s, "ordered-sess", "ag", "user", string(rune('a'+i)))
	}
	// Limit=3 should return the LAST 3 in chronological (oldest-first) order.
	entries, err := s.Load(context.Background(), "ordered-sess", 3)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	// Should be "c", "d", "e" (entries 2,3,4) in order.
	if entries[0].Content > entries[1].Content || entries[1].Content > entries[2].Content {
		t.Errorf("Load with limit should be oldest-first among last 3; got: %v %v %v",
			entries[0].Content, entries[1].Content, entries[2].Content)
	}
}

func TestLoadForAgent_NewestFirst(t *testing.T) {
	s := newHistoryStore(t)
	appendEntry(t, s, "s1", "ord-agent", "user", "first")
	appendEntry(t, s, "s2", "ord-agent", "user", "second")
	appendEntry(t, s, "s3", "ord-agent", "user", "third")

	entries, err := s.LoadForAgent(context.Background(), "ord-agent", 0)
	if err != nil {
		t.Fatalf("LoadForAgent: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	// LoadForAgent returns newest first (ORDER BY id DESC).
	if entries[0].Content != "third" {
		t.Errorf("expected newest first, got %q", entries[0].Content)
	}
}

func TestLoadForAgent_EmptyAgent(t *testing.T) {
	s := newHistoryStore(t)
	entries, err := s.LoadForAgent(context.Background(), "no-such-agent", 10)
	if err != nil {
		t.Fatalf("LoadForAgent empty: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestPrune_KeepsRecentEntries(t *testing.T) {
	s := newHistoryStore(t)
	// These should survive pruning (they were just inserted = now).
	appendEntry(t, s, "keep", "ag", "user", "recent-1")
	appendEntry(t, s, "keep", "ag", "user", "recent-2")

	n, err := s.Prune(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 0 {
		t.Errorf("Prune should keep recent entries, deleted %d", n)
	}

	remaining, err := s.Load(context.Background(), "keep", 0)
	if err != nil {
		t.Fatalf("Load after prune: %v", err)
	}
	if len(remaining) != 2 {
		t.Errorf("expected 2 remaining entries after prune, got %d", len(remaining))
	}
}

func TestEntry_IDAndCreatedAtSetOnLoad(t *testing.T) {
	s := newHistoryStore(t)
	appendEntry(t, s, "meta-sess", "ag", "user", "hello")

	entries, err := s.Load(context.Background(), "meta-sess", 0)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected entry")
	}
	e := entries[0]
	if e.ID == 0 {
		t.Error("expected non-zero auto-increment ID")
	}
	if e.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
	if e.SessionID != "meta-sess" {
		t.Errorf("SessionID: got %q, want meta-sess", e.SessionID)
	}
}

// ---------------------------------------------------------------------------
// SQLiteStore (resource store) — not covered by any existing test
// ---------------------------------------------------------------------------

func newResourceStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "resources.db"), DefaultConfig())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestResourceStore_PutAndGet(t *testing.T) {
	s := newResourceStore(t)
	ctx := context.Background()
	data := []byte("hello world")

	if err := s.Put(ctx, "res-1", "text/plain", data, time.Hour); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, mimeType, err := s.Get(ctx, "res-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("Get data: got %q, want %q", got, data)
	}
	if mimeType != "text/plain" {
		t.Errorf("Get mimeType: got %q, want text/plain", mimeType)
	}
}

func TestResourceStore_Get_NotFound(t *testing.T) {
	s := newResourceStore(t)
	_, _, err := s.Get(context.Background(), "does-not-exist")
	if err == nil {
		t.Error("expected error for nonexistent resource, got nil")
	}
}

func TestResourceStore_Put_TooLarge(t *testing.T) {
	cfg := Config{
		AttachmentTTL:     time.Hour,
		MaxAttachmentSize: 10, // very small limit
	}
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "res.db"), cfg)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	bigData := make([]byte, 11)
	if err := s.Put(context.Background(), "big", "text/plain", bigData, time.Hour); err == nil {
		t.Error("expected error for oversized payload, got nil")
	}
}

func TestResourceStore_Put_ZeroTTLUsesDefault(t *testing.T) {
	s := newResourceStore(t)
	ctx := context.Background()
	if err := s.Put(ctx, "ttl-default", "text/plain", []byte("data"), 0); err != nil {
		t.Fatalf("Put with zero TTL: %v", err)
	}
	// Should be retrievable immediately.
	got, _, err := s.Get(ctx, "ttl-default")
	if err != nil || string(got) != "data" {
		t.Errorf("Get after zero-TTL Put: got=%q err=%v", got, err)
	}
}

func TestResourceStore_Delete(t *testing.T) {
	s := newResourceStore(t)
	ctx := context.Background()

	_ = s.Put(ctx, "del-me", "text/plain", []byte("bye"), time.Hour)

	if err := s.Delete(ctx, "del-me"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, _, err := s.Get(ctx, "del-me")
	if err == nil {
		t.Error("expected error after Delete, got nil")
	}
}

func TestResourceStore_Delete_NonExistent(t *testing.T) {
	s := newResourceStore(t)
	// Safe to call Delete on non-existent id.
	if err := s.Delete(context.Background(), "ghost"); err != nil {
		t.Errorf("Delete nonexistent: unexpected error %v", err)
	}
}

func TestResourceStore_Prune_RemovesExpired(t *testing.T) {
	s := newResourceStore(t)
	ctx := context.Background()

	// Insert a resource with a past expiry by using a negative TTL. But Put
	// requires ttl > 0. Use SQL directly via the store's internal db to plant
	// an expired row.
	_ = s.Put(ctx, "expired-res", "text/plain", []byte("old"), time.Hour)
	// Overwrite the expires_at to the past directly.
	_, err := s.db.ExecContext(ctx,
		`UPDATE session_resources SET expires_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-2*time.Hour),
		"expired-res",
	)
	if err != nil {
		t.Fatalf("manual expires_at update: %v", err)
	}

	_ = s.Put(ctx, "fresh-res", "text/plain", []byte("new"), time.Hour)

	n, err := s.Prune(ctx)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Errorf("Prune: deleted %d rows, want 1", n)
	}

	// fresh-res should still be retrievable.
	if _, _, err := s.Get(ctx, "fresh-res"); err != nil {
		t.Errorf("fresh-res should survive Prune: %v", err)
	}
}

func TestResourceStore_Prune_EmptyDB(t *testing.T) {
	s := newResourceStore(t)
	n, err := s.Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune empty: %v", err)
	}
	if n != 0 {
		t.Errorf("Prune empty: deleted %d rows, want 0", n)
	}
}

func TestResourceStore_Put_Replace(t *testing.T) {
	s := newResourceStore(t)
	ctx := context.Background()
	_ = s.Put(ctx, "replace-me", "text/plain", []byte("v1"), time.Hour)
	_ = s.Put(ctx, "replace-me", "image/png", []byte("v2"), time.Hour)

	got, mimeType, err := s.Get(ctx, "replace-me")
	if err != nil {
		t.Fatalf("Get after replace: %v", err)
	}
	if string(got) != "v2" {
		t.Errorf("expected 'v2' after replace, got %q", got)
	}
	if mimeType != "image/png" {
		t.Errorf("expected image/png after replace, got %q", mimeType)
	}
}

func TestResourceStore_AttachmentMetadataRoundTrip(t *testing.T) {
	s := newResourceStore(t)
	ctx := context.Background()
	att := Attachment{
		ID:        "att-1",
		SessionID: "sess-1",
		AgentID:   "agent-1",
		Filename:  "notes.md",
		MIMEType:  "text/markdown",
		Text:      "# Notes",
	}
	if err := s.PutAttachment(ctx, att, []byte("# Notes"), time.Hour); err != nil {
		t.Fatalf("PutAttachment: %v", err)
	}
	list, err := s.ListAttachments(ctx, "agent-1", "sess-1")
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(list) != 1 || list[0].Filename != "notes.md" || list[0].Text != "# Notes" {
		t.Fatalf("ListAttachments = %+v", list)
	}
	got, data, err := s.GetAttachment(ctx, "att-1")
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}
	if got.SizeBytes != int64(len(data)) || string(data) != "# Notes" {
		t.Fatalf("GetAttachment got=%+v data=%q", got, data)
	}
}

func TestResourceStore_ListAttachmentsFiltersSession(t *testing.T) {
	s := newResourceStore(t)
	ctx := context.Background()
	_ = s.PutAttachment(ctx, Attachment{ID: "a1", AgentID: "agent-1", SessionID: "s1", Filename: "a.txt"}, []byte("a"), time.Hour)
	_ = s.PutAttachment(ctx, Attachment{ID: "a2", AgentID: "agent-1", SessionID: "s2", Filename: "b.txt"}, []byte("b"), time.Hour)
	_ = s.PutAttachment(ctx, Attachment{ID: "a3", AgentID: "agent-2", SessionID: "s1", Filename: "c.txt"}, []byte("c"), time.Hour)
	list, err := s.ListAttachments(ctx, "agent-1", "s1")
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(list) != 1 || list[0].ID != "a1" {
		t.Fatalf("filtered list = %+v", list)
	}
}

func TestNewSQLiteStoreBadPath(t *testing.T) {
	_, err := NewSQLiteStore("/no/such/dir/resources.db", DefaultConfig())
	if err == nil {
		t.Fatal("expected error for bad path, got nil")
	}
}

// ---------------------------------------------------------------------------
// Config helpers
// ---------------------------------------------------------------------------

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.AttachmentTTL != DefaultAttachmentTTL {
		t.Errorf("AttachmentTTL: got %v, want %v", cfg.AttachmentTTL, DefaultAttachmentTTL)
	}
	if cfg.MaxAttachmentSize != DefaultMaxAttachmentSize {
		t.Errorf("MaxAttachmentSize: got %d, want %d", cfg.MaxAttachmentSize, DefaultMaxAttachmentSize)
	}
}

// ---------------------------------------------------------------------------
// splitStmts, trim, truncate helpers (package-internal, tested in-package)
// ---------------------------------------------------------------------------

func TestSplitStmts_Basic(t *testing.T) {
	sql := "CREATE TABLE a (id INT); CREATE TABLE b (id INT)"
	stmts := splitStmts(sql)
	if len(stmts) != 2 {
		t.Fatalf("splitStmts: got %d statements, want 2", len(stmts))
	}
}

func TestSplitStmts_SkipsEmpty(t *testing.T) {
	sql := "  ; ; SELECT 1  "
	stmts := splitStmts(sql)
	if len(stmts) != 1 {
		t.Fatalf("splitStmts: got %d, want 1 (empty parts skipped)", len(stmts))
	}
}

func TestSplitStmts_EmptyInput(t *testing.T) {
	stmts := splitStmts("")
	if len(stmts) != 0 {
		t.Errorf("splitStmts empty: got %d", len(stmts))
	}
}

func TestSplitStmts_NoSemicolon(t *testing.T) {
	stmts := splitStmts("SELECT 1")
	if len(stmts) != 1 || stmts[0] != "SELECT 1" {
		t.Errorf("splitStmts no semicolon: got %v", stmts)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello world", 5); got != "hello" {
		t.Errorf("truncate: got %q, want 'hello'", got)
	}
	if got := truncate("short", 100); got != "short" {
		t.Errorf("truncate no-op: got %q, want 'short'", got)
	}
	if got := truncate("exact", 5); got != "exact" {
		t.Errorf("truncate exact fit: got %q, want 'exact'", got)
	}
}
