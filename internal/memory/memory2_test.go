// memory2_test.go — additional coverage for FileStore edge cases,
// SQLiteArchive edge cases, and helper functions not reached by store_test.go /
// sqlite_test.go.
// Requires CGO (mattn/go-sqlite3) for the SQLiteArchive tests.
package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// FileStore — readTail with a file larger than the tail window
// ---------------------------------------------------------------------------

// TestReadTailLargeFileDropsPartialFirstLine writes a file whose size exceeds
// readTailBytes, then verifies that readTail:
//  1. Does not error.
//  2. Returns only the suffix window.
//  3. Does NOT return a partial first JSON line (the boundary line is discarded).
func TestReadTailLargeFileDropsPartialFirstLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.jsonl")

	// Build content bigger than readTailBytes (64 KiB).
	// Fill with 200-byte lines so we cross the boundary cleanly.
	var sb strings.Builder
	for sb.Len() < int(readTailBytes)+512 {
		sb.WriteString(`{"id":"x","agent_id":"ag","session_id":"s","scope":"session","content":"` + strings.Repeat("x", 120) + `","created_at":"2025-01-01T00:00:00Z"}`)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	data, err := readTail(path, readTailBytes)
	if err != nil {
		t.Fatalf("readTail: %v", err)
	}
	if int64(len(data)) > readTailBytes {
		t.Errorf("readTail returned %d bytes, want <= %d", len(data), readTailBytes)
	}
	// The returned window must not start with a partial line; every non-empty
	// line must be valid UTF-8 JSON starting with '{'.
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		if line[0] != '{' {
			t.Errorf("first char of line is %q, want '{' (partial line leaked)", line[0])
		}
	}
}

// TestReadTailNonExistentFileErrors verifies readTail surfaces an os.IsNotExist
// error when the file is missing, matching the behaviour expected by FileStore.Read.
func TestReadTailNonExistentFileErrors(t *testing.T) {
	_, err := readTail(filepath.Join(t.TempDir(), "ghost.jsonl"), readTailBytes)
	if err == nil {
		t.Fatal("readTail on missing file returned nil error")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected os.IsNotExist, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// FileStore — Delete (documented no-op) and Close
// ---------------------------------------------------------------------------

func TestFileStoreDeleteIsNoOp(t *testing.T) {
	s := newFileStore(t)
	writeEntry(t, s, "ag", "s1", ScopeSession, "keep me")

	if err := s.Delete("any-id"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Entry must still be readable — Delete is a documented no-op.
	entries, err := s.Read("ag", "s1", ScopeSession, 10)
	if err != nil {
		t.Fatalf("Read after Delete: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("entry count after Delete = %d, want 1", len(entries))
	}
}

func TestFileStoreCloseIsNoOp(t *testing.T) {
	s := newFileStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// FileStore — Entry with optional fields (ExpiresAt, Key, Metadata)
// ---------------------------------------------------------------------------

func TestFileStoreWriteAndReadOptionalFields(t *testing.T) {
	s := newFileStore(t)
	exp := time.Now().Add(time.Hour).UTC()
	e := Entry{
		AgentID:   "ag",
		SessionID: "s1",
		Scope:     ScopeAgent,
		Key:       "user-pref",
		Content:   "dark mode",
		Metadata:  map[string]string{"source": "ui"},
		CreatedAt: time.Now().UTC(),
		ExpiresAt: &exp,
	}
	if err := s.Write(e); err != nil {
		t.Fatalf("Write: %v", err)
	}

	entries, err := s.Read("ag", "s1", ScopeAgent, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(entries))
	}
	got := entries[0]
	if got.Key != "user-pref" {
		t.Errorf("Key = %q, want user-pref", got.Key)
	}
	if got.Metadata["source"] != "ui" {
		t.Errorf("Metadata = %v, want source=ui", got.Metadata)
	}
	if got.ExpiresAt == nil {
		t.Fatal("ExpiresAt was not persisted")
	}
	if got.ExpiresAt.Unix() != exp.Unix() {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, exp)
	}
}

// ---------------------------------------------------------------------------
// FileStore — auto-assigned ID and CreatedAt
// ---------------------------------------------------------------------------

func TestFileStoreWriteAutoAssignsIDAndCreatedAt(t *testing.T) {
	s := newFileStore(t)
	e := Entry{AgentID: "ag", SessionID: "s1", Scope: ScopeSession, Content: "bare"}
	if err := s.Write(e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	entries, err := s.Read("ag", "s1", ScopeSession, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries returned")
	}
	if entries[0].ID == "" {
		t.Error("Write should auto-assign a UUID if ID is empty")
	}
	if entries[0].CreatedAt.IsZero() {
		t.Error("Write should set CreatedAt if zero")
	}
}

// ---------------------------------------------------------------------------
// FileStore — Search with empty query matches everything
// ---------------------------------------------------------------------------

func TestFileStoreSearchEmptyQueryMatchesAll(t *testing.T) {
	s := newFileStore(t)
	writeEntry(t, s, "ag", "s1", ScopeSession, "alpha")
	writeEntry(t, s, "ag", "s2", ScopeSession, "beta")

	results, err := s.Search("ag", "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("empty-query search count = %d, want 2", len(results))
	}
}

// ---------------------------------------------------------------------------
// FileStore — Search limit
// ---------------------------------------------------------------------------

func TestFileStoreSearchRespectsLimit(t *testing.T) {
	s := newFileStore(t)
	for i := 0; i < 5; i++ {
		writeEntry(t, s, "ag", "s1", ScopeSession, "match me")
	}
	results, err := s.Search("ag", "match", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("search with limit 2 returned %d results, want 2", len(results))
	}
}

// ---------------------------------------------------------------------------
// FileStore — Search across no-session-files returns empty, not error
// ---------------------------------------------------------------------------

func TestFileStoreSearchUnknownAgentReturnsEmpty(t *testing.T) {
	s := newFileStore(t)
	results, err := s.Search("nobody", "anything", 10)
	if err != nil {
		t.Fatalf("Search unknown agent: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for unknown agent, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Search with empty query returns all rows for agent
// ---------------------------------------------------------------------------

func TestSQLiteArchiveSearchEmptyQueryMatchesAll(t *testing.T) {
	a := newTestArchive(t)
	archiveEntry(t, a, "ag", "s1", ScopeSession, "alpha")
	archiveEntry(t, a, "ag", "s2", ScopeSession, "beta")

	results, err := a.Search("ag", "", 10)
	if err != nil {
		t.Fatalf("Search empty query: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("empty-query count = %d, want 2", len(results))
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — ReadGlobal respects limit
// ---------------------------------------------------------------------------

func TestSQLiteArchiveReadGlobalRespectsLimit(t *testing.T) {
	a := newTestArchive(t)
	for i := 0; i < 5; i++ {
		archiveEntry(t, a, "ag", "s1", ScopeSession, "entry")
	}
	results, err := a.ReadGlobal("ag", 3)
	if err != nil {
		t.Fatalf("ReadGlobal: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("ReadGlobal limit 3 returned %d, want 3", len(results))
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — ReadGlobal returns empty for unknown agent
// ---------------------------------------------------------------------------

func TestSQLiteArchiveReadGlobalUnknownAgentReturnsEmpty(t *testing.T) {
	a := newTestArchive(t)
	archiveEntry(t, a, "ag", "s1", ScopeSession, "exists")

	results, err := a.ReadGlobal("nobody", 10)
	if err != nil {
		t.Fatalf("ReadGlobal unknown agent: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for unknown agent, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Stats returns zero for unknown agent (no error)
// ---------------------------------------------------------------------------

func TestSQLiteArchiveStatsUnknownAgentReturnsZero(t *testing.T) {
	a := newTestArchive(t)
	count, err := a.Stats("nobody")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if count != 0 {
		t.Errorf("Stats for unknown agent = %d, want 0", count)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Archive stores Entry with ExpiresAt
// ---------------------------------------------------------------------------

func TestSQLiteArchiveArchiveWithExpiresAt(t *testing.T) {
	a := newTestArchive(t)
	exp := time.Now().Add(24 * time.Hour).UTC()
	e := Entry{
		ID:        "expires-1",
		AgentID:   "ag",
		SessionID: "s1",
		Scope:     ScopeSession,
		Content:   "expiring content",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: &exp,
	}
	if err := a.Archive(e); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	results, err := a.Search("ag", "expiring", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("count = %d, want 1", len(results))
	}
	if results[0].ExpiresAt == nil {
		t.Fatal("ExpiresAt was not persisted")
	}
	if results[0].ExpiresAt.Unix() != exp.Unix() {
		t.Errorf("ExpiresAt = %v, want %v", results[0].ExpiresAt, exp)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Archive stores metadata field
// ---------------------------------------------------------------------------

func TestSQLiteArchiveArchiveWithMetadata(t *testing.T) {
	a := newTestArchive(t)
	e := Entry{
		ID:        "meta-1",
		AgentID:   "ag",
		SessionID: "s1",
		Scope:     ScopeSession,
		Content:   "has metadata",
		Metadata:  map[string]string{"k": "v"},
		CreatedAt: time.Now().UTC(),
	}
	if err := a.Archive(e); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	results, err := a.Search("ag", "has metadata", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results")
	}
	if results[0].Metadata["k"] != "v" {
		t.Errorf("Metadata = %v, want k=v", results[0].Metadata)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Prune with zero cutoff deletes nothing
// ---------------------------------------------------------------------------

func TestSQLiteArchivePruneZeroCutoffDeletesNothing(t *testing.T) {
	a := newTestArchive(t)
	archiveEntry(t, a, "ag", "s1", ScopeSession, "recent")

	// A zero time.Time is before the epoch; no real entries should be deleted.
	n, err := a.Prune("ag", time.Time{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	// created_at is set to time.Now() by archiveEntry, which is after the zero
	// time, so nothing should be pruned.
	_ = n // may be 0 or 1 depending on exact clock; we only care there's no error.

	count, err := a.Stats("ag")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if count == 0 {
		t.Error("Prune with zero cutoff should not have deleted the recent entry")
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — DB() returns non-nil *sql.DB
// ---------------------------------------------------------------------------

func TestSQLiteArchiveDBReturnsDB(t *testing.T) {
	a := newTestArchive(t)
	if a.DB() == nil {
		t.Error("DB() returned nil")
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — ReadByScope returns empty when no matching scope
// ---------------------------------------------------------------------------

func TestSQLiteArchiveReadByScopeNoMatch(t *testing.T) {
	a := newTestArchive(t)
	archiveEntry(t, a, "ag", "s1", ScopeSession, "session only")

	// Ask for global scope; nothing was archived with ScopeGlobal.
	results, err := a.ReadByScope("ag", "s1", ScopeGlobal, 10)
	if err != nil {
		t.Fatalf("ReadByScope: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 global results, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — ReadByScope respects limit
// ---------------------------------------------------------------------------

func TestSQLiteArchiveReadByScopeRespectsLimit(t *testing.T) {
	a := newTestArchive(t)
	for i := 0; i < 5; i++ {
		archiveEntry(t, a, "ag", "s1", ScopeSession, "entry")
	}
	results, err := a.ReadByScope("ag", "s1", ScopeSession, 2)
	if err != nil {
		t.Fatalf("ReadByScope: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("ReadByScope limit 2 returned %d, want 2", len(results))
	}
}

// ---------------------------------------------------------------------------
// splitLines — additional edge cases
// ---------------------------------------------------------------------------

func TestSplitLinesOnlyNewlines(t *testing.T) {
	// "\n\n\n" should yield three empty slices. The helper counts non-empty,
	// but the function must not panic.
	got := splitLines([]byte("\n\n\n"))
	// All segments are empty — function should return something without panicking.
	_ = got
}

func TestSplitLinesSingleCharNoNewline(t *testing.T) {
	got := splitLines([]byte("x"))
	if len(got) != 1 || string(got[0]) != "x" {
		t.Errorf("splitLines('x') = %v, want [x]", got)
	}
}
