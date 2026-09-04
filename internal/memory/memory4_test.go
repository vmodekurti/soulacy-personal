// memory4_test.go — fourth wave of coverage tests for internal/memory.
// Targets paths remaining after store_test.go, sqlite_test.go,
// memory2_test.go, and memory3_test.go.
// Requires CGO (mattn/go-sqlite3) for the SQLiteArchive tests.
// FileStore tests are pure-Go.
package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// FileStore — concurrent writes to the SAME (agent, session) shard
// ---------------------------------------------------------------------------

// TestFileStoreShardSerializationSameSession verifies that concurrent writes
// to the same (agent, session) pair do not corrupt the JSONL file. Every
// entry written must be readable back with a valid ID.
func TestFileStoreShardSerializationSameSession(t *testing.T) {
	s := newFileStore(t)
	const goroutines = 10
	const msgs = 10

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < msgs; j++ {
				writeEntry(t, s, "agent", "shared-session", ScopeSession,
					fmt.Sprintf("g%d-m%d", n, j))
			}
		}(i)
	}
	wg.Wait()

	entries, err := s.Read("agent", "shared-session", ScopeSession, goroutines*msgs+1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != goroutines*msgs {
		t.Errorf("entry count = %d, want %d", len(entries), goroutines*msgs)
	}
	for _, e := range entries {
		if e.ID == "" {
			t.Error("found entry with empty ID — possible corruption")
		}
	}
}

// ---------------------------------------------------------------------------
// FileStore — shardFor lazily initialises and reuses the same mutex
// ---------------------------------------------------------------------------

// TestFileStoreShardForReusesMutex verifies that two calls to shardFor with
// the same (agent, session) return the exact same *sync.Mutex pointer, while
// a different pair returns a different one.
func TestFileStoreShardForReusesMutex(t *testing.T) {
	s := newFileStore(t)

	m1 := s.shardFor("ag", "s1")
	m2 := s.shardFor("ag", "s1")
	if m1 != m2 {
		t.Error("shardFor with the same key should return the same mutex")
	}

	m3 := s.shardFor("ag", "s2")
	if m1 == m3 {
		t.Error("shardFor with a different session should return a different mutex")
	}
}

// ---------------------------------------------------------------------------
// FileStore — sessionPath helper
// ---------------------------------------------------------------------------

// TestFileStoreSessionPathReturnsExpectedPath checks that sessionPath produces
// the expected directory structure: <dir>/<agentID>/<sessionID>.jsonl.
func TestFileStoreSessionPathReturnsExpectedPath(t *testing.T) {
	s := newFileStore(t)
	got := s.sessionPath("agent-x", "sess-y")
	want := filepath.Join(s.dir, "agent-x", "sess-y.jsonl")
	if got != want {
		t.Errorf("sessionPath = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// FileStore — Read with scope "" returns all entries regardless of scope
// ---------------------------------------------------------------------------

// TestFileStoreReadEmptyScopeReturnsAll verifies that passing an empty scope
// string to Read does not filter by scope and returns all entries.
func TestFileStoreReadEmptyScopeReturnsAll(t *testing.T) {
	s := newFileStore(t)
	writeEntry(t, s, "ag", "s1", ScopeSession, "session msg")
	writeEntry(t, s, "ag", "s1", ScopeAgent, "agent msg")
	writeEntry(t, s, "ag", "s1", ScopeGlobal, "global msg")

	all, err := s.Read("ag", "s1", "", 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("Read with empty scope count = %d, want 3", len(all))
	}
}

// ---------------------------------------------------------------------------
// FileStore — Search stops at exactly the limit boundary
// ---------------------------------------------------------------------------

// TestFileStoreSearchLimitBoundary writes exactly `limit` matching entries and
// verifies that the search returns all of them (no off-by-one in the loop exit
// condition `len(results) >= limit`).
func TestFileStoreSearchLimitBoundary(t *testing.T) {
	s := newFileStore(t)
	const limit = 4
	for i := 0; i < limit; i++ {
		writeEntry(t, s, "ag", fmt.Sprintf("s%d", i), ScopeSession, "boundary match")
	}
	// One extra non-matching entry to make sure it's genuinely capped.
	writeEntry(t, s, "ag", "extra", ScopeSession, "no match here")

	results, err := s.Search("ag", "boundary match", limit)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != limit {
		t.Errorf("Search limit boundary count = %d, want %d", len(results), limit)
	}
}

// ---------------------------------------------------------------------------
// FileStore — PurgeSession with multiple agent dirs
// ---------------------------------------------------------------------------

// TestFileStorePurgeSessionNoMatchSessionDoesNothing verifies PurgeSession is
// harmless when no file matches the given sessionID pattern.
func TestFileStorePurgeSessionNoMatchSessionDoesNothing(t *testing.T) {
	s := newFileStore(t)
	writeEntry(t, s, "ag", "real-session", ScopeSession, "keep")

	if err := s.PurgeSession("nonexistent-session"); err != nil {
		t.Fatalf("PurgeSession non-existent: %v", err)
	}

	entries, err := s.Read("ag", "real-session", ScopeSession, 10)
	if err != nil {
		t.Fatalf("Read after PurgeSession: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("entry count after harmless PurgeSession = %d, want 1", len(entries))
	}
}

// ---------------------------------------------------------------------------
// FileStore — ScopeAgent round-trip
// ---------------------------------------------------------------------------

// TestFileStoreWriteAndReadScopeAgent verifies that ScopeAgent entries survive
// a write/read cycle without being lost by scope filtering.
func TestFileStoreWriteAndReadScopeAgent(t *testing.T) {
	s := newFileStore(t)
	writeEntry(t, s, "ag", "s1", ScopeAgent, "agent-scoped content")

	entries, err := s.Read("ag", "s1", ScopeAgent, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(entries))
	}
	if entries[0].Scope != ScopeAgent {
		t.Errorf("Scope = %q, want ScopeAgent", entries[0].Scope)
	}
}

// ---------------------------------------------------------------------------
// FileStore — Close is idempotent (call twice without panic)
// ---------------------------------------------------------------------------

func TestFileStoreCloseIdempotent(t *testing.T) {
	s := newFileStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Prune returns count > 0 when entries are deleted
// ---------------------------------------------------------------------------

// TestSQLiteArchivePruneDeletesMultipleEntries verifies that Prune returns the
// count of deleted rows (not just 0 or 1) when multiple old entries exist.
func TestSQLiteArchivePruneDeletesMultipleEntries(t *testing.T) {
	a := newTestArchive(t)
	cutoff := time.Now().Add(-time.Hour).UTC()

	// Insert 3 old entries.
	for i := 0; i < 3; i++ {
		seq := atomic.AddInt64(&archiveSeq, 1)
		e := Entry{
			ID:        fmt.Sprintf("old-multi-%d", seq),
			AgentID:   "ag",
			SessionID: "s1",
			Scope:     ScopeSession,
			Content:   fmt.Sprintf("old-%d", i),
			CreatedAt: time.Now().Add(-2 * time.Hour).UTC(),
		}
		if err := a.Archive(e); err != nil {
			t.Fatalf("Archive old %d: %v", i, err)
		}
	}
	// One recent entry that should survive.
	archiveEntry(t, a, "ag", "s1", ScopeSession, "recent")

	n, err := a.Prune("ag", cutoff)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 3 {
		t.Errorf("Prune deleted %d rows, want 3", n)
	}

	count, err := a.Stats("ag")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if count != 1 {
		t.Errorf("post-prune count = %d, want 1", count)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — ReadGlobal returns newest entries first
// ---------------------------------------------------------------------------

// TestSQLiteArchiveReadGlobalOrderedNewestFirst inserts two entries with
// deliberately separated timestamps and verifies that ReadGlobal returns
// the newer one at index 0.
func TestSQLiteArchiveReadGlobalOrderedNewestFirst(t *testing.T) {
	a := newTestArchive(t)
	seq1 := atomic.AddInt64(&archiveSeq, 1)
	seq2 := atomic.AddInt64(&archiveSeq, 1)

	old := Entry{
		ID:        fmt.Sprintf("test-%d", seq1),
		AgentID:   "ag",
		SessionID: "s1",
		Scope:     ScopeSession,
		Content:   "older entry",
		CreatedAt: time.Now().Add(-time.Minute).UTC(),
	}
	newer := Entry{
		ID:        fmt.Sprintf("test-%d", seq2),
		AgentID:   "ag",
		SessionID: "s1",
		Scope:     ScopeSession,
		Content:   "newer entry",
		CreatedAt: time.Now().UTC(),
	}
	_ = a.Archive(old)
	_ = a.Archive(newer)

	results, err := a.ReadGlobal("ag", 10)
	if err != nil {
		t.Fatalf("ReadGlobal: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("count = %d, want 2", len(results))
	}
	if results[0].Content != "newer entry" {
		t.Errorf("first entry = %q, want 'newer entry' (newest first)", results[0].Content)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — ReadByScope returns newest entries first
// ---------------------------------------------------------------------------

// TestSQLiteArchiveReadByScopeOrderedNewestFirst verifies that ReadByScope
// also returns entries newest-first (ORDER BY created_at DESC).
func TestSQLiteArchiveReadByScopeOrderedNewestFirst(t *testing.T) {
	a := newTestArchive(t)
	seq1 := atomic.AddInt64(&archiveSeq, 1)
	seq2 := atomic.AddInt64(&archiveSeq, 1)

	old := Entry{
		ID:        fmt.Sprintf("test-%d", seq1),
		AgentID:   "ag",
		SessionID: "s1",
		Scope:     ScopeSession,
		Content:   "old-scope",
		CreatedAt: time.Now().Add(-time.Minute).UTC(),
	}
	newer := Entry{
		ID:        fmt.Sprintf("test-%d", seq2),
		AgentID:   "ag",
		SessionID: "s1",
		Scope:     ScopeSession,
		Content:   "new-scope",
		CreatedAt: time.Now().UTC(),
	}
	_ = a.Archive(old)
	_ = a.Archive(newer)

	results, err := a.ReadByScope("ag", "s1", ScopeSession, 10)
	if err != nil {
		t.Fatalf("ReadByScope: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("count = %d, want 2", len(results))
	}
	if results[0].Content != "new-scope" {
		t.Errorf("first entry = %q, want 'new-scope' (newest first)", results[0].Content)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Archive preserves Key field
// ---------------------------------------------------------------------------

// TestSQLiteArchiveArchiveWithKey verifies that the optional Key field
// survives an archive→search round-trip.
func TestSQLiteArchiveArchiveWithKey(t *testing.T) {
	a := newTestArchive(t)
	seq := atomic.AddInt64(&archiveSeq, 1)
	e := Entry{
		ID:        fmt.Sprintf("test-%d", seq),
		AgentID:   "ag",
		SessionID: "s1",
		Scope:     ScopeSession,
		Key:       "pref:theme",
		Content:   "dark mode enabled",
		CreatedAt: time.Now().UTC(),
	}
	if err := a.Archive(e); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	results, err := a.Search("ag", "dark mode", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("count = %d, want 1", len(results))
	}
	if results[0].Key != "pref:theme" {
		t.Errorf("Key = %q, want 'pref:theme'", results[0].Key)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Stats counts only the requested agent's entries
// ---------------------------------------------------------------------------

// TestSQLiteArchiveStatsIsolation archives entries for two agents and
// verifies Stats returns the per-agent count, not the global count.
func TestSQLiteArchiveStatsIsolation(t *testing.T) {
	a := newTestArchive(t)
	for i := 0; i < 5; i++ {
		archiveEntry(t, a, "alpha", "s1", ScopeSession, "alpha entry")
	}
	for i := 0; i < 2; i++ {
		archiveEntry(t, a, "beta", "s1", ScopeSession, "beta entry")
	}

	alphaCount, err := a.Stats("alpha")
	if err != nil {
		t.Fatalf("Stats alpha: %v", err)
	}
	if alphaCount != 5 {
		t.Errorf("Stats(alpha) = %d, want 5", alphaCount)
	}

	betaCount, err := a.Stats("beta")
	if err != nil {
		t.Fatalf("Stats beta: %v", err)
	}
	if betaCount != 2 {
		t.Errorf("Stats(beta) = %d, want 2", betaCount)
	}
}

// ---------------------------------------------------------------------------
// splitSQL — single-statement without semicolon passes through as-is
// ---------------------------------------------------------------------------

// TestSplitSQLMultipleStatements verifies that exactly the right number of
// non-empty statements are produced from a multi-statement SQL string.
func TestSplitSQLMultipleStatements(t *testing.T) {
	sql := `CREATE TABLE a (id TEXT);CREATE TABLE b (id TEXT);CREATE INDEX idx ON a(id)`
	parts := splitSQL(sql)
	if len(parts) != 3 {
		t.Fatalf("splitSQL count = %d, want 3; parts = %v", len(parts), parts)
	}
	if !strings.Contains(parts[2], "CREATE INDEX") {
		t.Errorf("third statement = %q, want CREATE INDEX", parts[2])
	}
}

// ---------------------------------------------------------------------------
// VectorStore — Write returns error when embedder returns wrong dim
// ---------------------------------------------------------------------------

// TestVectorStoreWriteDimMismatch exercises the dim-mismatch error path in
// VectorStore.Write without needing sqlite-vec to be loaded. We construct a
// VectorStore by directly populating its fields (the struct is unexported-
// fields only but is in the same package) and call Write with an embedder
// that returns a vector of the wrong length.
//
// Since NewVectorStore requires the vec0 extension which is not available in
// unit tests, we validate the dim-mismatch guard via a unit-level stub that
// pre-constructs the VectorStore without calling ensureSchema.
func TestVectorStoreWriteDimMismatch(t *testing.T) {
	// directVS bypasses ensureSchema (which requires sqlite-vec) to let us test
	// the Write-level dim check without needing the full extension.
	vs := &VectorStore{
		db:       nil,      // not needed — error is surfaced before any DB call
		embedder: &stubEmbedder{dims: 2, vec: []float32{0.1, 0.2}},
		dims:     4, // mismatch: embedder produces 2, VectorStore expects 4
	}
	err := vs.Write(context.Background(), Entry{
		ID:        "x",
		AgentID:   "ag",
		SessionID: "s1",
		Scope:     ScopeSession,
		Content:   "dim mismatch test",
		CreatedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("Write should return error on embedding dimension mismatch")
	}
	if !strings.Contains(err.Error(), "dims") && !strings.Contains(err.Error(), "dimension") &&
		!strings.Contains(err.Error(), "expected") {
		t.Errorf("error message should mention dimension mismatch, got: %v", err)
	}
}

// TestVectorStoreWriteEmbedderError verifies that an embedder error is
// propagated by Write and contains the "vector memory" prefix.
func TestVectorStoreWriteEmbedderError(t *testing.T) {
	vs := &VectorStore{
		db:       nil,
		embedder: &errorEmbedder{},
		dims:     4,
	}
	err := vs.Write(context.Background(), Entry{
		ID:      "e1",
		AgentID: "ag",
		Content: "embed error",
	})
	if err == nil {
		t.Fatal("Write should return error when embedder fails")
	}
	if !strings.Contains(err.Error(), "embed") {
		t.Errorf("error should mention embed failure, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Embedder stubs used by VectorStore tests
// ---------------------------------------------------------------------------

// errorEmbedder always returns an error from Embed.
type errorEmbedder struct{}

func (e *errorEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, fmt.Errorf("stub: embed failed")
}

// ---------------------------------------------------------------------------
// containsCI — boundary: substr longer than s
// ---------------------------------------------------------------------------

// TestContainsCISubstrLongerThanString covers the early-exit path in
// containsCI where `i+len(subl) > len(sl)` terminates the outer loop before
// any inner comparison occurs.
func TestContainsCISubstrLongerThanString(t *testing.T) {
	cases := []struct {
		s, sub string
	}{
		{"ab", "abc"},
		{"", "x"},
		{"hi", "hello"},
	}
	for _, tc := range cases {
		if containsCI(tc.s, tc.sub) {
			t.Errorf("containsCI(%q, %q) = true, want false (substr longer than s)", tc.s, tc.sub)
		}
	}
}

// ---------------------------------------------------------------------------
// Entry — zero-value Scope round-trips through FileStore
// ---------------------------------------------------------------------------

// TestFileStoreReadZeroScopeEntry writes an entry with an explicitly empty
// Scope (not one of the named constants) and verifies it can be read back via
// the empty-scope Read (which should return all entries regardless of scope).
func TestFileStoreReadZeroScopeEntry(t *testing.T) {
	s := newFileStore(t)
	e := Entry{
		AgentID:   "ag",
		SessionID: "s1",
		Scope:     "", // explicitly empty / zero-value
		Content:   "bare scope",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.Write(e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Read with scope="" returns everything.
	all, err := s.Read("ag", "s1", "", 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("count = %d, want 1", len(all))
	}
}
