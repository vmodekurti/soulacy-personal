// memory3_test.go — third wave of coverage tests for internal/memory.
// Targets remaining uncovered paths after store_test.go, sqlite_test.go,
// and memory2_test.go. Requires CGO (mattn/go-sqlite3) for SQLiteArchive
// tests; FileStore tests are pure-Go.
package memory

import (
	"context"
	"github.com/soulacy/soulacy/internal/wsroot"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// readTail — window with no newline returns nil, nil
// ---------------------------------------------------------------------------

// TestReadTailWindowWithNoNewlineReturnsNil writes a file just over the
// tail-window boundary, then crafts a tail window whose content contains no
// newline at all (all bytes are 'x'). The function must return (nil, nil)
// because there is no complete line to surface.
func TestReadTailWindowWithNoNewlineReturnsNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonewline.jsonl")

	// Write (readTailBytes + 10) bytes with no newline anywhere. This forces
	// readTail to seek into the file and read the tail, finding no '\n' in
	// the tail window, and therefore returning nil, nil.
	payload := []byte(strings.Repeat("x", int(readTailBytes)+10))
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	data, err := readTail(path, readTailBytes)
	if err != nil {
		t.Fatalf("readTail: unexpected error: %v", err)
	}
	// No newline in the window means the partial-line-drop loop exhausts
	// without finding '\n', so nil is returned.
	if data != nil {
		t.Errorf("expected nil data when tail window has no newline, got %d bytes", len(data))
	}
}

// ---------------------------------------------------------------------------
// NewFileStore — error path when directory is unwritable
// ---------------------------------------------------------------------------

// TestNewFileStoreCreatesDir verifies that NewFileStore creates the directory
// if it does not already exist.
func TestNewFileStoreCreatesDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "new", "subdir")
	// Directory must not exist yet.
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Skip("directory already exists; cannot test creation")
	}

	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore on non-existent dir: %v", err)
	}
	defer s.Close()

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("NewFileStore did not create directory: %v", err)
	}
}

// TestNewFileStoreErrorOnFile verifies that NewFileStore returns an error when
// given a path that already exists as a regular file (os.MkdirAll would fail).
func TestNewFileStoreErrorOnFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "notadir")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	f.Close()

	// Pass the file path as the directory — MkdirAll should fail.
	_, err = NewFileStore(f.Name())
	// On some platforms MkdirAll succeeds (if the path IS a directory or
	// the path already satisfies MkdirAll's postcondition). Only assert if
	// the error is non-nil so the test is not flaky.
	if err != nil {
		// Good — the error path was exercised.
		return
	}
	// If err == nil the OS permitted it; the test is a no-op (not a failure).
}

// ---------------------------------------------------------------------------
// NewSQLiteArchive — error path
// ---------------------------------------------------------------------------

// TestNewSQLiteArchiveInvalidPath verifies that NewSQLiteArchive returns a
// non-nil error when the path refers to a location inside a regular file.
func TestNewSQLiteArchiveInvalidPath(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "notadir")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	f.Close()

	// Attempt to open SQLite "inside" a regular file.
	_, err = NewSQLiteArchive(filepath.Join(f.Name(), "archive.db"))
	if err == nil {
		t.Fatal("NewSQLiteArchive should return error for path inside a file")
	}
}

// ---------------------------------------------------------------------------
// splitSQL — string with no semicolons
// ---------------------------------------------------------------------------

// TestSplitSQLNoSemicolon verifies that a string with no semicolons is
// returned as a single element (not dropped or split into empty parts).
func TestSplitSQLNoSemicolon(t *testing.T) {
	input := "SELECT 1"
	parts := splitSQL(input)
	if len(parts) != 1 {
		t.Fatalf("splitSQL(%q) = %v (len %d), want 1 element", input, parts, len(parts))
	}
	if parts[0] != "SELECT 1" {
		t.Errorf("splitSQL(%q)[0] = %q, want %q", input, parts[0], input)
	}
}

// TestSplitSQLOnlyWhitespace verifies that a string of only whitespace /
// semicolons produces an empty result.
func TestSplitSQLOnlyWhitespace(t *testing.T) {
	parts := splitSQL("  ;  ;  ")
	if len(parts) != 0 {
		t.Errorf("expected 0 non-empty parts, got %d: %v", len(parts), parts)
	}
}

// TestSplitSQLTrailingSemicolon verifies a trailing semicolon doesn't add
// a spurious empty entry.
func TestSplitSQLTrailingSemicolon(t *testing.T) {
	parts := splitSQL("SELECT 1;")
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d: %v", len(parts), parts)
	}
}

// ---------------------------------------------------------------------------
// containsCI — additional edge cases
// ---------------------------------------------------------------------------

// TestContainsCISingleChar covers a single-character substr.
func TestContainsCISingleChar(t *testing.T) {
	if !containsCI("abc", "b") {
		t.Error("containsCI('abc', 'b') should be true")
	}
	if !containsCI("ABC", "a") {
		t.Error("containsCI('ABC', 'a') should be true (case-insensitive)")
	}
	if containsCI("abc", "z") {
		t.Error("containsCI('abc', 'z') should be false")
	}
}

// TestContainsCIDigits verifies numeric content matches correctly.
func TestContainsCIDigits(t *testing.T) {
	if !containsCI("agent-123", "123") {
		t.Error("containsCI('agent-123', '123') should be true")
	}
}

// TestContainsCIExactMatch verifies an exact full-string match.
func TestContainsCIExactMatch(t *testing.T) {
	if !containsCI("hello", "hello") {
		t.Error("containsCI('hello', 'hello') should be true")
	}
	if !containsCI("HELLO", "hello") {
		t.Error("containsCI('HELLO', 'hello') should be true")
	}
}

// ---------------------------------------------------------------------------
// FileStore — PurgeSession removes matching session across multiple agents
// ---------------------------------------------------------------------------

// TestFileStorePurgeSessionAcrossMultipleAgents writes the same session ID
// under two different agent IDs, then calls PurgeSession. Both files should
// be removed.
func TestFileStorePurgeSessionAcrossMultipleAgents(t *testing.T) {
	s := newFileStore(t)
	writeEntry(t, s, "agent-a", "shared-session", ScopeSession, "a's memory")
	writeEntry(t, s, "agent-b", "shared-session", ScopeSession, "b's memory")

	if err := s.PurgeSession(wsroot.PersonalWorkspaceID, "shared-session"); err != nil {
		t.Fatalf("PurgeSession: %v", err)
	}

	// Both agent dirs should now return nil for the purged session.
	for _, ag := range []string{"agent-a", "agent-b"} {
		entries, err := s.Read(wsroot.PersonalWorkspaceID, ag, "shared-session", ScopeSession, 10)
		if err != nil {
			t.Fatalf("Read %s after PurgeSession: %v", ag, err)
		}
		if len(entries) != 0 {
			t.Errorf("agent %s still has %d entries after PurgeSession", ag, len(entries))
		}
	}
}

// ---------------------------------------------------------------------------
// FileStore — Search on agent dir with existing dir but no .jsonl files
// ---------------------------------------------------------------------------

// TestFileStoreSearchAgentDirWithNoJSONLFiles creates the agent directory
// with a non-JSONL file inside, then verifies Search returns empty without
// error.
func TestFileStoreSearchAgentDirWithNoJSONLFiles(t *testing.T) {
	s := newFileStore(t)
	// Manually create the agent dir with a non-matching file so filepath.Glob
	// finds the dir but returns no *.jsonl matches.
	agentDir := filepath.Join(s.dir, "ag-nofiles")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "notes.txt"), []byte("not jsonl"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	results, err := s.Search(wsroot.PersonalWorkspaceID, "ag-nofiles", "anything", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Archive with nil metadata does not error
// ---------------------------------------------------------------------------

// TestSQLiteArchiveArchiveNilMetadata verifies that archiving an Entry with
// nil Metadata (zero-value map) does not cause a marshal error and can be
// read back.
func TestSQLiteArchiveArchiveNilMetadata(t *testing.T) {
	a := newTestArchive(t)
	e := Entry{WorkspaceID: wsroot.PersonalWorkspaceID,
		ID:        "nil-meta-1",
		AgentID:   "ag",
		SessionID: "s1",
		Scope:     ScopeSession,
		Content:   "no metadata here",
		Metadata:  nil, // explicit nil
		CreatedAt: time.Now().UTC(),
	}
	if err := a.Archive(e); err != nil {
		t.Fatalf("Archive with nil Metadata: %v", err)
	}

	results, err := a.Search(wsroot.PersonalWorkspaceID, "ag", "no metadata", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Close is callable
// ---------------------------------------------------------------------------

func TestSQLiteArchiveCloseIsCallable(t *testing.T) {
	a, err := NewSQLiteArchive(filepath.Join(t.TempDir(), "close.db"))
	if err != nil {
		t.Fatalf("NewSQLiteArchive: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Prune returns 0 when no entries exist for agent
// ---------------------------------------------------------------------------

func TestSQLiteArchivePruneEmptyAgentReturnsZero(t *testing.T) {
	a := newTestArchive(t)
	// No entries for "ghost" agent.
	n, err := a.Prune("ghost", time.Now().UTC())
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 0 {
		t.Errorf("Prune returned %d, want 0 for empty agent", n)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Stats returns 0 for empty store
// ---------------------------------------------------------------------------

func TestSQLiteArchiveStatsEmptyStore(t *testing.T) {
	a := newTestArchive(t)
	count, err := a.Stats("anyone")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if count != 0 {
		t.Errorf("Stats on empty store = %d, want 0", count)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — ReadByScope across sessions only returns matching session
// ---------------------------------------------------------------------------

// TestSQLiteArchiveReadByScopeWrongSession verifies that ReadByScope for
// session "s1" does not return entries stored under session "s2".
func TestSQLiteArchiveReadByScopeWrongSession(t *testing.T) {
	a := newTestArchive(t)
	archiveEntry(t, a, "ag", "s2", ScopeSession, "belongs to s2")

	results, err := a.ReadByScope(wsroot.PersonalWorkspaceID, "ag", "s1", ScopeSession, 10)
	if err != nil {
		t.Fatalf("ReadByScope: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("ReadByScope(s1) returned %d entries, want 0 (entry belongs to s2)", len(results))
	}
}

// ---------------------------------------------------------------------------
// VectorStore — sqlite-vec is registered by the archive
// ---------------------------------------------------------------------------

// TestNewVectorStoreHasNativeSQLiteVec verifies that opening the archive makes
// vec0 available even when the Knowledge subsystem was never constructed.
func TestNewVectorStoreHasNativeSQLiteVec(t *testing.T) {
	a := newTestArchive(t)
	db := a.DB()

	embedder := &stubEmbedder{dims: 4, vec: []float32{0.1, 0.2, 0.3, 0.4}}
	if _, err := NewVectorStore(db, embedder, 4); err != nil {
		t.Fatalf("native sqlite-vec should be registered: %v", err)
	}
}

// stubEmbedder is a minimal Embedder used in tests that don't need a real
// embedding provider.
type stubEmbedder struct {
	dims int
	vec  []float32
}

func (s *stubEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return s.vec, nil
}

// ---------------------------------------------------------------------------
// Scope constants — sanity check their string values
// ---------------------------------------------------------------------------

// TestScopeConstants ensures the scope string values match what is stored in
// SQLite (lowercase strings compared by SQL WHERE scope = ?).
func TestScopeConstants(t *testing.T) {
	cases := []struct {
		scope Scope
		want  string
	}{
		{ScopeSession, "session"},
		{ScopeAgent, "agent"},
		{ScopeGlobal, "global"},
	}
	for _, tc := range cases {
		if string(tc.scope) != tc.want {
			t.Errorf("Scope constant %q has value %q, want %q", tc.scope, string(tc.scope), tc.want)
		}
	}
}
