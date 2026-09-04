// sqlite_test.go — tests for the SQLiteArchive long-term memory backend.
// Requires CGO (mattn/go-sqlite3). Each test uses a fresh temp-dir DB.
package memory

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// archiveSeq generates unique IDs across the test binary lifetime.
var archiveSeq int64

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestArchive(t *testing.T) *SQLiteArchive {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	a, err := NewSQLiteArchive(path)
	if err != nil {
		t.Fatalf("NewSQLiteArchive: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func archiveEntry(t *testing.T, a *SQLiteArchive, agentID, sessionID string, scope Scope, content string) Entry {
	t.Helper()
	seq := atomic.AddInt64(&archiveSeq, 1)
	e := Entry{
		ID:        fmt.Sprintf("test-%d", seq),
		AgentID:   agentID,
		SessionID: sessionID,
		Scope:     scope,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	if err := a.Archive(e); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	return e
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Archive / Search
// ---------------------------------------------------------------------------

// TestSQLiteArchiveWriteAndSearch verifies the basic archive-then-search cycle.
func TestSQLiteArchiveWriteAndSearch(t *testing.T) {
	a := newTestArchive(t)
	archiveEntry(t, a, "ag", "s1", ScopeSession, "the cat sat on the mat")
	archiveEntry(t, a, "ag", "s2", ScopeSession, "the dog barked")

	results, err := a.Search("ag", "cat", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("search 'cat' count = %d, want 1", len(results))
	}
	if results[0].Content != "the cat sat on the mat" {
		t.Errorf("content = %q", results[0].Content)
	}
}

// TestSQLiteArchiveSearchMultipleHits verifies multiple matching rows are all returned.
func TestSQLiteArchiveSearchMultipleHits(t *testing.T) {
	a := newTestArchive(t)
	archiveEntry(t, a, "ag", "s1", ScopeSession, "python tutorial")
	archiveEntry(t, a, "ag", "s2", ScopeSession, "python cookbook")
	archiveEntry(t, a, "ag", "s3", ScopeSession, "go programming")

	results, err := a.Search("ag", "python", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("search 'python' count = %d, want 2", len(results))
	}
}

// TestSQLiteArchiveSearchRespectsLimit confirms limit is applied.
func TestSQLiteArchiveSearchRespectsLimit(t *testing.T) {
	a := newTestArchive(t)
	for i := 0; i < 5; i++ {
		archiveEntry(t, a, "ag", "s1", ScopeSession, "match keyword here")
	}

	results, err := a.Search("ag", "match", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("search with limit 3 returned %d results", len(results))
	}
}

// TestSQLiteArchiveSearchWrongAgent verifies that entries for a different agent
// are not returned.
func TestSQLiteArchiveSearchWrongAgent(t *testing.T) {
	a := newTestArchive(t)
	archiveEntry(t, a, "agent-a", "s1", ScopeSession, "secret data")

	results, err := a.Search("agent-b", "secret", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for wrong agent, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — ReadByScope
// ---------------------------------------------------------------------------

// TestSQLiteArchiveReadByScope verifies that ReadByScope returns only entries
// matching the given scope and session.
func TestSQLiteArchiveReadByScope(t *testing.T) {
	a := newTestArchive(t)
	archiveEntry(t, a, "ag", "s1", ScopeSession, "session content")
	archiveEntry(t, a, "ag", "s1", ScopeGlobal, "global content")
	archiveEntry(t, a, "ag", "s2", ScopeSession, "other session")

	results, err := a.ReadByScope("ag", "s1", ScopeSession, 10)
	if err != nil {
		t.Fatalf("ReadByScope: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("ReadByScope count = %d, want 1", len(results))
	}
	if results[0].Content != "session content" {
		t.Errorf("content = %q", results[0].Content)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — ReadGlobal
// ---------------------------------------------------------------------------

// TestSQLiteArchiveReadGlobal verifies ReadGlobal returns entries across
// all sessions for a given agent.
func TestSQLiteArchiveReadGlobal(t *testing.T) {
	a := newTestArchive(t)
	archiveEntry(t, a, "ag", "s1", ScopeSession, "session one")
	archiveEntry(t, a, "ag", "s2", ScopeSession, "session two")
	archiveEntry(t, a, "ag", "s3", ScopeGlobal, "global one")
	archiveEntry(t, a, "other", "s1", ScopeSession, "different agent")

	results, err := a.ReadGlobal("ag", 10)
	if err != nil {
		t.Fatalf("ReadGlobal: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("ReadGlobal count = %d, want 3", len(results))
	}
	for _, r := range results {
		if r.AgentID != "ag" {
			t.Errorf("unexpected agentID %q in ReadGlobal results", r.AgentID)
		}
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — duplicate ID idempotency
// ---------------------------------------------------------------------------

// TestSQLiteArchiveDuplicateIDIsIgnored confirms INSERT OR IGNORE means a
// second archive call with the same entry ID is silently dropped.
func TestSQLiteArchiveDuplicateIDIsIgnored(t *testing.T) {
	a := newTestArchive(t)
	e := archiveEntry(t, a, "ag", "s1", ScopeSession, "original content")

	// Archive again with same ID.
	e.Content = "modified content"
	if err := a.Archive(e); err != nil {
		t.Fatalf("second Archive: %v", err)
	}

	results, err := a.Search("ag", "content", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 row after duplicate insert, got %d", len(results))
	}
	// The original content should be preserved (INSERT OR IGNORE).
	if results[0].Content != "original content" {
		t.Errorf("content = %q, want 'original content'", results[0].Content)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Prune
// ---------------------------------------------------------------------------

// TestSQLiteArchivePrune verifies that Prune deletes entries older than
// the cutoff time and leaves newer entries untouched.
func TestSQLiteArchivePrune(t *testing.T) {
	a := newTestArchive(t)

	old := Entry{
		ID:        "old-1",
		AgentID:   "ag",
		SessionID: "s1",
		Scope:     ScopeSession,
		Content:   "old entry",
		CreatedAt: time.Now().Add(-2 * time.Hour).UTC(),
	}
	if err := a.Archive(old); err != nil {
		t.Fatalf("Archive old: %v", err)
	}
	archiveEntry(t, a, "ag", "s1", ScopeSession, "recent entry")

	cutoff := time.Now().Add(-time.Hour).UTC() // must match UTC storage format
	n, err := a.Prune("ag", cutoff)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Errorf("Prune deleted %d rows, want 1", n)
	}

	// Only the recent entry should survive.
	remaining, err := a.ReadGlobal("ag", 10)
	if err != nil {
		t.Fatalf("ReadGlobal after prune: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("after prune count = %d, want 1", len(remaining))
	}
	if remaining[0].Content != "recent entry" {
		t.Errorf("surviving content = %q, want 'recent entry'", remaining[0].Content)
	}
}

// TestSQLiteArchivePruneWrongAgent confirms Prune only touches the specified agent.
func TestSQLiteArchivePruneWrongAgent(t *testing.T) {
	a := newTestArchive(t)
	old := Entry{
		ID:        "other-old",
		AgentID:   "other-ag",
		SessionID: "s1",
		Scope:     ScopeSession,
		Content:   "other agent old data",
		CreatedAt: time.Now().Add(-2 * time.Hour).UTC(),
	}
	_ = a.Archive(old)

	n, err := a.Prune("ag", time.Now().UTC()) // prune "ag", not "other-ag"
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 0 {
		t.Errorf("Prune deleted %d rows from wrong agent, want 0", n)
	}

	// other-ag entry should still exist.
	results, err := a.ReadGlobal("other-ag", 10)
	if err != nil {
		t.Fatalf("ReadGlobal: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("other-ag entry was unexpectedly removed")
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Stats
// ---------------------------------------------------------------------------

// TestSQLiteArchiveStats verifies the row count returned by Stats.
func TestSQLiteArchiveStats(t *testing.T) {
	a := newTestArchive(t)
	for i := 0; i < 4; i++ {
		archiveEntry(t, a, "ag", "s1", ScopeSession, "entry")
	}
	archiveEntry(t, a, "other", "s1", ScopeSession, "other agent")

	count, err := a.Stats("ag")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if count != 4 {
		t.Errorf("Stats count = %d, want 4", count)
	}

	otherCount, err := a.Stats("other")
	if err != nil {
		t.Fatalf("Stats other: %v", err)
	}
	if otherCount != 1 {
		t.Errorf("Stats other count = %d, want 1", otherCount)
	}
}

// ---------------------------------------------------------------------------
// Helper unit tests
// ---------------------------------------------------------------------------

// TestSplitSQL verifies the SQL statement splitter used during schema init.
func TestSplitSQL(t *testing.T) {
	sql := `
		CREATE TABLE IF NOT EXISTS foo (id TEXT PRIMARY KEY);
		CREATE INDEX IF NOT EXISTS idx_foo ON foo(id);
	`
	parts := splitSQL(sql)
	if len(parts) != 2 {
		t.Fatalf("splitSQL count = %d, want 2; parts = %v", len(parts), parts)
	}
	if !strings.Contains(parts[0], "CREATE TABLE") {
		t.Errorf("first statement should contain CREATE TABLE: %q", parts[0])
	}
	if !strings.Contains(parts[1], "CREATE INDEX") {
		t.Errorf("second statement should contain CREATE INDEX: %q", parts[1])
	}
}

// TestSplitSQLEmptyAndWhitespace confirms empty/whitespace-only parts are dropped.
func TestSplitSQLEmptyAndWhitespace(t *testing.T) {
	parts := splitSQL("   ;  ; SELECT 1  ;  ")
	if len(parts) != 1 {
		t.Fatalf("expected 1 non-empty statement, got %d: %v", len(parts), parts)
	}
	if parts[0] != "SELECT 1" {
		t.Errorf("statement = %q, want 'SELECT 1'", parts[0])
	}
}

// TestMinInt covers the tiny helper used in error message truncation.
func TestMinInt(t *testing.T) {
	if minInt(3, 5) != 3 {
		t.Error("minInt(3, 5) should be 3")
	}
	if minInt(7, 2) != 2 {
		t.Error("minInt(7, 2) should be 2")
	}
	if minInt(4, 4) != 4 {
		t.Error("minInt(4, 4) should be 4")
	}
}
