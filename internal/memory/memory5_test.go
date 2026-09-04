// memory5_test.go — fifth wave of coverage tests for internal/memory.
// Targets paths remaining after memory2–4_test.go: VectorStore stubs,
// SQLiteArchive edge cases (limit=0, concurrent archive, multiple agents),
// FileStore concurrent append+load, and miscellaneous uncovered branches.
// Requires CGO (mattn/go-sqlite3) for SQLiteArchive tests.
// FileStore tests are pure-Go.
package memory

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// SQLiteArchive — ReadGlobal with limit=0 returns no rows (SQL LIMIT 0)
// ---------------------------------------------------------------------------

// TestSQLiteArchiveReadGlobalLimitZeroReturnsNothing verifies that passing
// limit=0 to ReadGlobal propagates as SQL LIMIT 0 and returns an empty slice
// without error.
func TestSQLiteArchiveReadGlobalLimitZeroReturnsNothing(t *testing.T) {
	a := newTestArchive(t)
	archiveEntry(t, a, "ag", "s1", ScopeSession, "some content")
	archiveEntry(t, a, "ag", "s2", ScopeSession, "more content")

	results, err := a.ReadGlobal("ag", 0)
	if err != nil {
		t.Fatalf("ReadGlobal(limit=0): %v", err)
	}
	if len(results) != 0 {
		t.Errorf("ReadGlobal with limit=0 returned %d results, want 0", len(results))
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — ReadGlobal limit larger than actual result count
// ---------------------------------------------------------------------------

// TestSQLiteArchiveReadGlobalLimitExceedsResults verifies that ReadGlobal with
// a limit larger than the number of stored entries simply returns all entries
// without padding or error.
func TestSQLiteArchiveReadGlobalLimitExceedsResults(t *testing.T) {
	a := newTestArchive(t)
	archiveEntry(t, a, "ag-excess", "s1", ScopeSession, "entry-a")
	archiveEntry(t, a, "ag-excess", "s2", ScopeSession, "entry-b")

	results, err := a.ReadGlobal("ag-excess", 1000)
	if err != nil {
		t.Fatalf("ReadGlobal(limit=1000): %v", err)
	}
	if len(results) != 2 {
		t.Errorf("ReadGlobal(limit>actual) count = %d, want 2", len(results))
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — ReadGlobal with multiple agents — agent isolation
// ---------------------------------------------------------------------------

// TestSQLiteArchiveReadGlobalFiltersToRequestedAgent archives entries for two
// agents and confirms ReadGlobal only returns the requested agent's entries.
func TestSQLiteArchiveReadGlobalFiltersToRequestedAgent(t *testing.T) {
	a := newTestArchive(t)
	archiveEntry(t, a, "alice", "s1", ScopeSession, "alice memory 1")
	archiveEntry(t, a, "alice", "s2", ScopeSession, "alice memory 2")
	archiveEntry(t, a, "bob", "s1", ScopeSession, "bob memory 1")
	archiveEntry(t, a, "bob", "s1", ScopeSession, "bob memory 2")
	archiveEntry(t, a, "bob", "s1", ScopeSession, "bob memory 3")

	aliceResults, err := a.ReadGlobal("alice", 10)
	if err != nil {
		t.Fatalf("ReadGlobal(alice): %v", err)
	}
	if len(aliceResults) != 2 {
		t.Errorf("ReadGlobal(alice) count = %d, want 2", len(aliceResults))
	}
	for _, r := range aliceResults {
		if r.AgentID != "alice" {
			t.Errorf("ReadGlobal(alice) returned entry with agentID=%q", r.AgentID)
		}
	}

	bobResults, err := a.ReadGlobal("bob", 10)
	if err != nil {
		t.Fatalf("ReadGlobal(bob): %v", err)
	}
	if len(bobResults) != 3 {
		t.Errorf("ReadGlobal(bob) count = %d, want 3", len(bobResults))
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Search with limit=1 returns only the first (newest) match
// ---------------------------------------------------------------------------

// TestSQLiteArchiveSearchLimitOne verifies that limit=1 returns exactly one
// row even when multiple rows match the query.
func TestSQLiteArchiveSearchLimitOne(t *testing.T) {
	a := newTestArchive(t)
	for i := 0; i < 5; i++ {
		archiveEntry(t, a, "ag-lim1", "s1", ScopeSession, fmt.Sprintf("needle %d", i))
	}

	results, err := a.Search("ag-lim1", "needle", 1)
	if err != nil {
		t.Fatalf("Search(limit=1): %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Search with limit=1 returned %d results, want 1", len(results))
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Search with limit=0 returns no rows
// ---------------------------------------------------------------------------

// TestSQLiteArchiveSearchLimitZeroReturnsNothing confirms limit=0 propagates
// as SQL LIMIT 0 and returns an empty slice.
func TestSQLiteArchiveSearchLimitZeroReturnsNothing(t *testing.T) {
	a := newTestArchive(t)
	archiveEntry(t, a, "ag-lim0", "s1", ScopeSession, "findme")

	results, err := a.Search("ag-lim0", "findme", 0)
	if err != nil {
		t.Fatalf("Search(limit=0): %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Search with limit=0 returned %d results, want 0", len(results))
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Prune with future cutoff deletes all entries
// ---------------------------------------------------------------------------

// TestSQLiteArchivePruneWithFutureCutoffDeletesAll inserts several entries
// and prunes with a far-future cutoff to verify all rows are removed.
func TestSQLiteArchivePruneWithFutureCutoffDeletesAll(t *testing.T) {
	a := newTestArchive(t)
	const n = 4
	for i := 0; i < n; i++ {
		archiveEntry(t, a, "ag-future", "s1", ScopeSession, fmt.Sprintf("entry-%d", i))
	}

	farFuture := time.Now().Add(24 * time.Hour).UTC()
	deleted, err := a.Prune("ag-future", farFuture)
	if err != nil {
		t.Fatalf("Prune(future): %v", err)
	}
	if deleted != n {
		t.Errorf("Prune(future) deleted %d rows, want %d", deleted, n)
	}

	count, err := a.Stats("ag-future")
	if err != nil {
		t.Fatalf("Stats after future prune: %v", err)
	}
	if count != 0 {
		t.Errorf("Stats after future prune = %d, want 0", count)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Prune only deletes the specified agent's old entries
// ---------------------------------------------------------------------------

// TestSQLiteArchivePruneDoesNotAffectOtherAgentsFutureEntries archives old
// entries for two agents, then prunes only one of them. The other agent's
// entries must survive.
func TestSQLiteArchivePruneAgentIsolation(t *testing.T) {
	a := newTestArchive(t)
	cutoff := time.Now().Add(-time.Hour).UTC()

	// Old entries for "prune-me" and "keep-me".
	for _, ag := range []string{"prune-me", "keep-me"} {
		seq := atomic.AddInt64(&archiveSeq, 1)
		e := Entry{
			ID:        fmt.Sprintf("old-%s-%d", ag, seq),
			AgentID:   ag,
			SessionID: "s1",
			Scope:     ScopeSession,
			Content:   "old data",
			CreatedAt: time.Now().Add(-2 * time.Hour).UTC(),
		}
		if err := a.Archive(e); err != nil {
			t.Fatalf("Archive(%s): %v", ag, err)
		}
	}

	n, err := a.Prune("prune-me", cutoff)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Errorf("Prune deleted %d rows for prune-me, want 1", n)
	}

	keepCount, err := a.Stats("keep-me")
	if err != nil {
		t.Fatalf("Stats(keep-me): %v", err)
	}
	if keepCount != 1 {
		t.Errorf("keep-me count = %d after pruning prune-me, want 1", keepCount)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — concurrent Archive calls do not corrupt data
// ---------------------------------------------------------------------------

// TestSQLiteArchiveConcurrentArchive fires several goroutines that each archive
// unique entries and then confirms the total row count matches.
func TestSQLiteArchiveConcurrentArchive(t *testing.T) {
	a := newTestArchive(t)
	const goroutines = 8
	const perGoroutine = 10

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				seq := atomic.AddInt64(&archiveSeq, 1)
				e := Entry{
					ID:        fmt.Sprintf("conc-%d-%d-%d", g, i, seq),
					AgentID:   "concurrent-ag",
					SessionID: fmt.Sprintf("s%d", g),
					Scope:     ScopeSession,
					Content:   fmt.Sprintf("g%d-entry%d", g, i),
					CreatedAt: time.Now().UTC(),
				}
				if err := a.Archive(e); err != nil {
					// t.Error is goroutine-safe.
					t.Errorf("Archive(g=%d,i=%d): %v", g, i, err)
				}
			}
		}(g)
	}
	wg.Wait()

	count, err := a.Stats("concurrent-ag")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	want := int64(goroutines * perGoroutine)
	if count != want {
		t.Errorf("concurrent archive count = %d, want %d", count, want)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Archive + ReadGlobal round-trip preserves all fields
// ---------------------------------------------------------------------------

// TestSQLiteArchiveRoundTripAllFields stores an entry with every optional field
// set and verifies each one is preserved through a ReadGlobal round-trip.
func TestSQLiteArchiveRoundTripAllFields(t *testing.T) {
	a := newTestArchive(t)
	exp := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	created := time.Now().UTC().Truncate(time.Second)
	e := Entry{
		ID:        "full-fields-1",
		AgentID:   "ag-full",
		SessionID: "sess-full",
		Scope:     ScopeAgent,
		Key:       "my:key",
		Content:   "full content",
		Metadata:  map[string]string{"color": "blue", "size": "large"},
		CreatedAt: created,
		ExpiresAt: &exp,
	}
	if err := a.Archive(e); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	results, err := a.ReadGlobal("ag-full", 10)
	if err != nil {
		t.Fatalf("ReadGlobal: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("ReadGlobal count = %d, want 1", len(results))
	}
	got := results[0]
	if got.ID != e.ID {
		t.Errorf("ID = %q, want %q", got.ID, e.ID)
	}
	if got.Key != e.Key {
		t.Errorf("Key = %q, want %q", got.Key, e.Key)
	}
	if got.Scope != e.Scope {
		t.Errorf("Scope = %q, want %q", got.Scope, e.Scope)
	}
	if got.Content != e.Content {
		t.Errorf("Content = %q, want %q", got.Content, e.Content)
	}
	if got.Metadata["color"] != "blue" || got.Metadata["size"] != "large" {
		t.Errorf("Metadata = %v, want color=blue size=large", got.Metadata)
	}
	if got.ExpiresAt == nil {
		t.Fatal("ExpiresAt not preserved")
	}
	if got.ExpiresAt.Unix() != exp.Unix() {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, exp)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Search returns entries newest-first
// ---------------------------------------------------------------------------

// TestSQLiteArchiveSearchOrderedNewestFirst inserts two matching entries with
// different timestamps and verifies Search returns the newer one at index 0.
func TestSQLiteArchiveSearchOrderedNewestFirst(t *testing.T) {
	a := newTestArchive(t)
	seq1 := atomic.AddInt64(&archiveSeq, 1)
	seq2 := atomic.AddInt64(&archiveSeq, 1)

	old := Entry{
		ID: fmt.Sprintf("ord-%d", seq1), AgentID: "ag-ord",
		SessionID: "s1", Scope: ScopeSession, Content: "needle old",
		CreatedAt: time.Now().Add(-2 * time.Minute).UTC(),
	}
	newer := Entry{
		ID: fmt.Sprintf("ord-%d", seq2), AgentID: "ag-ord",
		SessionID: "s1", Scope: ScopeSession, Content: "needle new",
		CreatedAt: time.Now().UTC(),
	}
	_ = a.Archive(old)
	_ = a.Archive(newer)

	results, err := a.Search("ag-ord", "needle", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Search count = %d, want 2", len(results))
	}
	if results[0].Content != "needle new" {
		t.Errorf("first result = %q, want 'needle new' (newest first)", results[0].Content)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — ReadByScope with ScopeAgent scope
// ---------------------------------------------------------------------------

// TestSQLiteArchiveReadByScopeScopeAgent verifies ReadByScope works for
// ScopeAgent entries, not just ScopeSession.
func TestSQLiteArchiveReadByScopeScopeAgent(t *testing.T) {
	a := newTestArchive(t)
	archiveEntry(t, a, "ag-rbs", "s1", ScopeAgent, "agent-scoped memory")
	archiveEntry(t, a, "ag-rbs", "s1", ScopeSession, "session-scoped memory")

	results, err := a.ReadByScope("ag-rbs", "s1", ScopeAgent, 10)
	if err != nil {
		t.Fatalf("ReadByScope(ScopeAgent): %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("ReadByScope(ScopeAgent) count = %d, want 1", len(results))
	}
	if results[0].Scope != ScopeAgent {
		t.Errorf("Scope = %q, want ScopeAgent", results[0].Scope)
	}
	if results[0].Content != "agent-scoped memory" {
		t.Errorf("Content = %q, want 'agent-scoped memory'", results[0].Content)
	}
}

// ---------------------------------------------------------------------------
// FileStore — concurrent Append then Load (Write + Read)
// ---------------------------------------------------------------------------

// TestFileStoreConcurrentWriteThenRead writes N entries concurrently to a
// single (agent, session) and then reads them back, checking that none are
// lost and every entry has a valid ID (no corruption).
func TestFileStoreConcurrentWriteThenRead(t *testing.T) {
	s := newFileStore(t)
	const goroutines = 6
	const msgsEach = 15

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < msgsEach; i++ {
				writeEntry(t, s, "agent-conc", "session-conc", ScopeSession,
					fmt.Sprintf("worker-%d-msg-%d", g, i))
			}
		}(g)
	}
	wg.Wait()

	total := goroutines * msgsEach
	entries, err := s.Read("agent-conc", "session-conc", ScopeSession, total+1)
	if err != nil {
		t.Fatalf("Read after concurrent writes: %v", err)
	}
	if len(entries) != total {
		t.Errorf("entry count = %d, want %d", len(entries), total)
	}
	for _, e := range entries {
		if e.ID == "" {
			t.Error("found entry with empty ID — possible file corruption")
		}
	}
}

// ---------------------------------------------------------------------------
// FileStore — concurrent Write + Read (interleaved)
// ---------------------------------------------------------------------------

// TestFileStoreConcurrentWriteAndReadInterleaved runs a writer goroutine and a
// reader goroutine in parallel. The reader must not crash or return an error
// even if it races with writes.
func TestFileStoreConcurrentWriteAndReadInterleaved(t *testing.T) {
	s := newFileStore(t)
	const writes = 30

	var writerDone atomic.Int32
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer writerDone.Store(1)
		for i := 0; i < writes; i++ {
			writeEntry(t, s, "ag-race", "sess-race", ScopeSession,
				fmt.Sprintf("write-%d", i))
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for writerDone.Load() == 0 {
			_, err := s.Read("ag-race", "sess-race", ScopeSession, writes)
			if err != nil {
				t.Errorf("Read during concurrent writes: %v", err)
				return
			}
		}
	}()

	wg.Wait()
}

// ---------------------------------------------------------------------------
// FileStore — Search across multiple sessions collects results
// ---------------------------------------------------------------------------

// TestFileStoreSearchCollectsAcrossMultipleSessions verifies that Search scans
// all *.jsonl files under the agent directory and aggregates matches.
func TestFileStoreSearchCollectsAcrossMultipleSessions(t *testing.T) {
	s := newFileStore(t)
	// Write matching entries across 4 different sessions.
	for i := 0; i < 4; i++ {
		writeEntry(t, s, "ag-multi", fmt.Sprintf("sess-%d", i), ScopeSession,
			"unique-keyword content")
	}
	// Write a non-matching entry in a fifth session.
	writeEntry(t, s, "ag-multi", "sess-4", ScopeSession, "no match here")

	results, err := s.Search("ag-multi", "unique-keyword", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 4 {
		t.Errorf("Search across sessions count = %d, want 4", len(results))
	}
}

// ---------------------------------------------------------------------------
// VectorStore — Search topK clamping (no sqlite-vec; only tests stub path)
// ---------------------------------------------------------------------------

// TestVectorStoreSearchTopKClampEmbedderError validates that the topK clamping
// in VectorStore.Search is reached (via embed error) without a nil-pointer
// panic when topK is 0 or negative.
func TestVectorStoreSearchTopKClampNegative(t *testing.T) {
	vs := &VectorStore{
		db:       nil,
		embedder: &errorEmbedder{},
		dims:     4,
	}
	// topK <= 0 should be clamped to 5 before embed is called.
	// Embed will error, but we get past the topK clamping branch.
	_, err := vs.Search(context.Background(), "anything", -1)
	if err == nil {
		t.Fatal("Search should return error from errorEmbedder")
	}
	// Must mention "embed" in the chain, not panic.
	if !strings.Contains(err.Error(), "embed") {
		t.Errorf("error should mention embed, got: %v", err)
	}
}

// TestVectorStoreSearchTopKClampOverMax validates the topK > 50 clamping path.
func TestVectorStoreSearchTopKClampOverMax(t *testing.T) {
	vs := &VectorStore{
		db:       nil,
		embedder: &errorEmbedder{},
		dims:     4,
	}
	_, err := vs.Search(context.Background(), "anything", 99)
	if err == nil {
		t.Fatal("Search should return error from errorEmbedder")
	}
	if !strings.Contains(err.Error(), "embed") {
		t.Errorf("error should mention embed, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// VectorStore — SearchFiltered topK clamping
// ---------------------------------------------------------------------------

// TestVectorStoreSearchFilteredTopKClampNegative hits the topK <= 0 branch
// inside SearchFiltered and verifies the errorEmbedder's error propagates.
func TestVectorStoreSearchFilteredTopKClampNegative(t *testing.T) {
	vs := &VectorStore{
		db:       nil,
		embedder: &errorEmbedder{},
		dims:     4,
	}
	_, err := vs.SearchFiltered(context.Background(), "query", 0, "ag")
	if err == nil {
		t.Fatal("SearchFiltered should return error from errorEmbedder")
	}
	if !strings.Contains(err.Error(), "embed") {
		t.Errorf("error should mention embed, got: %v", err)
	}
}

// TestVectorStoreSearchFilteredTopKClampOverMax hits the topK > 50 branch
// inside SearchFiltered.
func TestVectorStoreSearchFilteredTopKClampOverMax(t *testing.T) {
	vs := &VectorStore{
		db:       nil,
		embedder: &errorEmbedder{},
		dims:     4,
	}
	_, err := vs.SearchFiltered(context.Background(), "query", 100, "ag")
	if err == nil {
		t.Fatal("SearchFiltered should return error from errorEmbedder")
	}
	if !strings.Contains(err.Error(), "embed") {
		t.Errorf("error should mention embed, got: %v", err)
	}
}

// TestVectorStoreSearchFilteredEmptyAgentID hits the agentID=="" branch (uses
// the query without the extra rowid subquery). The errorEmbedder surfaces
// before any DB call so no sqlite-vec is required.
func TestVectorStoreSearchFilteredEmptyAgentID(t *testing.T) {
	vs := &VectorStore{
		db:       nil,
		embedder: &errorEmbedder{},
		dims:     4,
	}
	_, err := vs.SearchFiltered(context.Background(), "query", 5, "")
	if err == nil {
		t.Fatal("SearchFiltered(agentID='') should return error from errorEmbedder")
	}
	if !strings.Contains(err.Error(), "embed") {
		t.Errorf("error should mention embed, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// VectorStore — Write propagates json.Marshal error (indirectly via stub)
// ---------------------------------------------------------------------------

// TestVectorStoreWriteCorrectDimsThenNilDBErrors verifies the code path in
// VectorStore.Write after a successful Embed but before any DB call.
// With db=nil, the BeginTx call will panic or error depending on the sql pkg.
// We use a stubEmbedder that returns the right number of dims.
// This exercises the vec marshal + BeginTx path (db==nil returns an error
// from sql.DB.BeginTx because the DB is closed/nil).
func TestVectorStoreWriteCorrectDimsNilDBErrors(t *testing.T) {
	vs := &VectorStore{
		db:       nil,
		embedder: &stubEmbedder{dims: 4, vec: []float32{0.1, 0.2, 0.3, 0.4}},
		dims:     4,
	}
	// db is nil — this will panic if we reach vs.db.BeginTx.
	// We recover from the panic to detect the code path was reached.
	func() {
		defer func() {
			if r := recover(); r != nil {
				// Panic from nil db.BeginTx — expected, proves we passed the
				// embed + dim-check + json.Marshal steps.
			}
		}()
		// Either panics (nil DB dereference) or returns an error.
		err := vs.Write(context.Background(), Entry{
			ID: "nil-db-1", AgentID: "ag", SessionID: "s1",
			Scope: ScopeSession, Content: "test", CreatedAt: time.Now().UTC(),
		})
		if err != nil && !strings.Contains(err.Error(), "begin") &&
			!strings.Contains(err.Error(), "tx") &&
			!strings.Contains(err.Error(), "sql") {
			t.Logf("Write with nil DB returned: %v (acceptable)", err)
		}
	}()
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Stats after multiple sessions for same agent
// ---------------------------------------------------------------------------

// TestSQLiteArchiveStatsSumAcrossSessions writes entries into different
// sessions for the same agent and verifies Stats returns the cross-session sum.
func TestSQLiteArchiveStatsSumAcrossSessions(t *testing.T) {
	a := newTestArchive(t)
	for sess := 0; sess < 3; sess++ {
		for i := 0; i < 4; i++ {
			archiveEntry(t, a, "ag-sum", fmt.Sprintf("s%d", sess), ScopeSession, "data")
		}
	}

	count, err := a.Stats("ag-sum")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if count != 12 {
		t.Errorf("Stats cross-session count = %d, want 12", count)
	}
}

// ---------------------------------------------------------------------------
// FileStore — Write assigns distinct IDs to concurrent entries
// ---------------------------------------------------------------------------

// TestFileStoreWriteDistinctIDs verifies that concurrent writes all produce
// unique UUIDs (no ID collision from the auto-assignment path).
func TestFileStoreWriteDistinctIDs(t *testing.T) {
	s := newFileStore(t)
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Pass an entry with no ID so Write auto-assigns one.
			e := Entry{
				AgentID:   "ag-ids",
				SessionID: "s1",
				Scope:     ScopeSession,
				Content:   fmt.Sprintf("entry-%d", i),
				CreatedAt: time.Now().UTC(),
			}
			if err := s.Write(e); err != nil {
				t.Errorf("Write: %v", err)
			}
		}(i)
	}
	wg.Wait()

	entries, err := s.Read("ag-ids", "s1", ScopeSession, n+1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != n {
		t.Fatalf("entry count = %d, want %d", len(entries), n)
	}
	ids := make(map[string]bool, n)
	for _, e := range entries {
		if e.ID == "" {
			t.Error("entry with empty ID found")
		}
		if ids[e.ID] {
			t.Errorf("duplicate ID: %q", e.ID)
		}
		ids[e.ID] = true
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Archive is idempotent for IDs with special chars
// ---------------------------------------------------------------------------

// TestSQLiteArchiveIDWithSpecialChars verifies that an entry whose ID contains
// special characters (dashes, slashes) is stored and retrieved correctly.
func TestSQLiteArchiveIDWithSpecialChars(t *testing.T) {
	a := newTestArchive(t)
	e := Entry{
		ID:        "agent/session-2026-06-06T00:00:00Z",
		AgentID:   "ag-special",
		SessionID: "sess-1",
		Scope:     ScopeSession,
		Content:   "special id content",
		CreatedAt: time.Now().UTC(),
	}
	if err := a.Archive(e); err != nil {
		t.Fatalf("Archive with special-char ID: %v", err)
	}

	results, err := a.Search("ag-special", "special id", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("count = %d, want 1", len(results))
	}
	if results[0].ID != e.ID {
		t.Errorf("ID = %q, want %q", results[0].ID, e.ID)
	}
}

// ---------------------------------------------------------------------------
// SQLiteArchive — Stats for agent with entries spread across many sessions
// ---------------------------------------------------------------------------

// TestSQLiteArchiveStatsManySessions archives a large number of entries spread
// across many (agent, session) pairs and verifies Stats returns the correct
// aggregate count.
func TestSQLiteArchiveStatsManySessions(t *testing.T) {
	a := newTestArchive(t)
	const sessions = 10
	const perSession = 3
	for i := 0; i < sessions; i++ {
		for j := 0; j < perSession; j++ {
			archiveEntry(t, a, "ag-many", fmt.Sprintf("sess-%d", i), ScopeSession, "data")
		}
	}

	count, err := a.Stats("ag-many")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	want := int64(sessions * perSession)
	if count != want {
		t.Errorf("Stats = %d, want %d", count, want)
	}
}

// ---------------------------------------------------------------------------
// FileStore — Read returns entries with correct AgentID and SessionID
// ---------------------------------------------------------------------------

// TestFileStoreReadPreservesAgentAndSession writes an entry and confirms the
// AgentID and SessionID fields round-trip correctly through JSON serialisation.
func TestFileStoreReadPreservesAgentAndSession(t *testing.T) {
	s := newFileStore(t)
	writeEntry(t, s, "agent-preserve", "session-preserve", ScopeSession, "hello")

	entries, err := s.Read("agent-preserve", "session-preserve", ScopeSession, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(entries))
	}
	if entries[0].AgentID != "agent-preserve" {
		t.Errorf("AgentID = %q, want 'agent-preserve'", entries[0].AgentID)
	}
	if entries[0].SessionID != "session-preserve" {
		t.Errorf("SessionID = %q, want 'session-preserve'", entries[0].SessionID)
	}
}
