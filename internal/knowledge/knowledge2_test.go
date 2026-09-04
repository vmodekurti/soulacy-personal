// knowledge2_test.go — additional coverage for service.go paths not exercised
// by store_test.go or ingest_test.go. Focuses on:
//   - Service.ListAvailable (nil service, wildcard, specific names)
//   - Service.Search (nil service, empty query, missing KB, missing embedder,
//     embed-cache hit, no hits)
//   - FormatHits content-trimming and multi-chunk output
//   - embedLRU eviction and update-on-hit
//   - NewService
//   - Service.Search embedder cache hit path
package knowledge

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newTestStore2(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kb2.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// fakeEmbedder is an llm.Embedder that returns fixed vectors.
type fakeEmbedder struct {
	// vectors is a map from text → vector; if missing returns a default.
	vectors map[string][]float32
	// If errOnNext is set, Embed returns it once then clears it.
	errOnNext error
	// callCount tracks how many times Embed was called (for cache-hit checks).
	callCount int
}

func (f *fakeEmbedder) ID() string { return "test" }

func (f *fakeEmbedder) Embed(_ context.Context, _ string, texts []string) ([][]float32, error) {
	f.callCount++
	if f.errOnNext != nil {
		err := f.errOnNext
		f.errOnNext = nil
		return nil, err
	}
	out := make([][]float32, len(texts))
	for i, text := range texts {
		if v, ok := f.vectors[text]; ok {
			out[i] = v
		} else {
			// Default: a unit vector [1,0,0,0] so searches work.
			out[i] = []float32{1, 0, 0, 0}
		}
	}
	return out, nil
}

func (f *fakeEmbedder) Dim(_ context.Context, _ string) (int, error) {
	return 4, nil
}

// newTestService builds a Store + EmbedderRegistry + Service wired with a
// fakeEmbedder under "test" provider.
func newTestService(t *testing.T) (*Service, *Store, *fakeEmbedder) {
	t.Helper()
	store := newTestStore2(t)
	fe := &fakeEmbedder{vectors: map[string][]float32{}}
	reg := llm.NewEmbedderRegistry()
	reg.Register(fe)
	svc := NewService(store, reg)
	return svc, store, fe
}

// seedKB creates a KB and adds one document with a single chunk (dim=4).
func seedKB(t *testing.T, store *Store, name string) *KB {
	t.Helper()
	kb, err := store.CreateKB(KB{
		Name: name, EmbeddingProvider: "test", EmbeddingModel: "fake",
		Dim: 4, ChunkSize: 100, ChunkOverlap: 0,
	})
	if err != nil {
		t.Fatalf("CreateKB %q: %v", name, err)
	}
	_, err = store.AddDocument(kb, Document{Title: "doc", Source: "src.txt"},
		[]Chunk{{Content: "hello world", Vector: []float32{1, 0, 0, 0}}})
	if err != nil {
		t.Fatalf("AddDocument %q: %v", name, err)
	}
	return kb
}

// ---------------------------------------------------------------------------
// NewService
// ---------------------------------------------------------------------------

func TestNewService_NotNil(t *testing.T) {
	store := newTestStore2(t)
	reg := llm.NewEmbedderRegistry()
	svc := NewService(store, reg)
	if svc == nil {
		t.Fatal("NewService returned nil")
	}
	if svc.Store == nil {
		t.Error("NewService: Store is nil")
	}
	if svc.Embedders == nil {
		t.Error("NewService: Embedders is nil")
	}
}

// ---------------------------------------------------------------------------
// Service.ListAvailable
// ---------------------------------------------------------------------------

func TestListAvailable_NilService(t *testing.T) {
	var svc *Service
	got := svc.ListAvailable([]string{"kb1"})
	if got != nil {
		t.Errorf("nil service ListAvailable should return nil, got %v", got)
	}
}

func TestListAvailable_NilStore(t *testing.T) {
	svc := &Service{Store: nil}
	got := svc.ListAvailable([]string{"kb1"})
	if got != nil {
		t.Errorf("nil store ListAvailable should return nil, got %v", got)
	}
}

func TestListAvailable_WildcardStar(t *testing.T) {
	svc, store, _ := newTestService(t)
	seedKB(t, store, "kb-a")
	seedKB(t, store, "kb-b")

	got := svc.ListAvailable([]string{"*"})
	if len(got) < 2 {
		t.Errorf("wildcard '*' should return all KBs; got %d", len(got))
	}
}

func TestListAvailable_WildcardAll(t *testing.T) {
	svc, store, _ := newTestService(t)
	seedKB(t, store, "kb-all-1")

	got := svc.ListAvailable([]string{"all"})
	if len(got) < 1 {
		t.Errorf("wildcard 'all' should return all KBs; got %d", len(got))
	}
}

func TestListAvailable_SpecificNames(t *testing.T) {
	svc, store, _ := newTestService(t)
	seedKB(t, store, "exist-kb")

	got := svc.ListAvailable([]string{"exist-kb", "missing-kb"})
	// "missing-kb" silently dropped; only "exist-kb" returned.
	if len(got) != 1 {
		t.Errorf("expected 1 result; got %d: %v", len(got), got)
	}
	if got[0].Name != "exist-kb" {
		t.Errorf("expected 'exist-kb', got %q", got[0].Name)
	}
}

func TestListAvailable_AllMissing(t *testing.T) {
	svc, _, _ := newTestService(t)
	got := svc.ListAvailable([]string{"missing-a", "missing-b"})
	if len(got) != 0 {
		t.Errorf("all-missing names: expected 0, got %d", len(got))
	}
}

func TestListAvailable_KBSummaryFields(t *testing.T) {
	svc, store, _ := newTestService(t)
	_, err := store.CreateKB(KB{
		Name: "described-kb", Description: "my desc",
		EmbeddingProvider: "test", EmbeddingModel: "fake",
		Dim: 4, ChunkSize: 100, ChunkOverlap: 0,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	got := svc.ListAvailable([]string{"described-kb"})
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	if got[0].Description != "my desc" {
		t.Errorf("Description = %q, want 'my desc'", got[0].Description)
	}
}

// ---------------------------------------------------------------------------
// Service.Search — error paths
// ---------------------------------------------------------------------------

func TestServiceSearch_NilService(t *testing.T) {
	var svc *Service
	_, err := svc.Search(context.Background(), "kb", "query", 5)
	if err == nil {
		t.Error("nil service Search should return error")
	}
}

func TestServiceSearch_EmptyQuery(t *testing.T) {
	svc, _, _ := newTestService(t)
	_, err := svc.Search(context.Background(), "any-kb", "   ", 5)
	if err == nil {
		t.Error("empty/whitespace query should return error")
	}
	if !strings.Contains(err.Error(), "query is required") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestServiceSearch_MissingKB(t *testing.T) {
	svc, _, _ := newTestService(t)
	_, err := svc.Search(context.Background(), "no-such-kb", "test query", 5)
	if err == nil {
		t.Error("missing KB should return error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found': %v", err)
	}
}

func TestServiceSearch_MissingEmbedder(t *testing.T) {
	svc, store, _ := newTestService(t)
	// Create a KB with a provider not registered in the embedder registry.
	_, err := store.CreateKB(KB{
		Name: "unknown-provider-kb", EmbeddingProvider: "unknown-provider",
		EmbeddingModel: "none", Dim: 4,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}

	_, err = svc.Search(context.Background(), "unknown-provider-kb", "hello", 5)
	if err == nil {
		t.Error("missing embedder should return error")
	}
	if !strings.Contains(err.Error(), "no embedder") {
		t.Errorf("error should mention 'no embedder': %v", err)
	}
}

func TestServiceSearch_EmbedError(t *testing.T) {
	svc, store, fe := newTestService(t)
	seedKB(t, store, "embed-err-kb")
	fe.errOnNext = errors.New("embedding service down")

	_, err := svc.Search(context.Background(), "embed-err-kb", "query", 5)
	if err == nil {
		t.Error("embed error should propagate")
	}
}

func TestServiceSearch_DefaultTopK(t *testing.T) {
	// topK <= 0 should default to 5 internally with no error.
	svc, store, _ := newTestService(t)
	seedKB(t, store, "topk-svc-kb")

	result, err := svc.Search(context.Background(), "topk-svc-kb", "hello world", 0)
	if err != nil {
		t.Fatalf("Search topK=0: %v", err)
	}
	_ = result
}

func TestServiceSearch_NoHitsMessage(t *testing.T) {
	svc, store, _ := newTestService(t)
	// Create an empty KB (no documents).
	_, err := store.CreateKB(KB{
		Name: "empty-kb-svc", EmbeddingProvider: "test", EmbeddingModel: "fake", Dim: 4,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}

	result, err := svc.Search(context.Background(), "empty-kb-svc", "query", 5)
	if err != nil {
		t.Fatalf("Search empty KB: %v", err)
	}
	if !strings.Contains(result, "No matching passages") {
		t.Errorf("expected 'No matching passages' in result; got %q", result)
	}
}

func TestServiceSearch_HappyPath(t *testing.T) {
	svc, store, fe := newTestService(t)
	_ = seedKB(t, store, "happy-kb")
	fe.vectors["hello world"] = []float32{1, 0, 0, 0}

	result, err := svc.Search(context.Background(), "happy-kb", "hello world", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.Contains(result, "kb_results") {
		t.Errorf("expected XML-ish kb_results block, got: %s", result)
	}
}

// ---------------------------------------------------------------------------
// Service.Search — embed cache hit path
// ---------------------------------------------------------------------------

func TestServiceSearch_EmbedCacheHit(t *testing.T) {
	svc, store, fe := newTestService(t)
	seedKB(t, store, "cache-kb")

	query := "cached query"
	fe.vectors[query] = []float32{1, 0, 0, 0}

	// First call — embeds the query.
	if _, err := svc.Search(context.Background(), "cache-kb", query, 3); err != nil {
		t.Fatalf("first Search: %v", err)
	}
	callsAfterFirst := fe.callCount

	// Second call with the same query — should hit the cache, not call Embed again.
	if _, err := svc.Search(context.Background(), "cache-kb", query, 3); err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if fe.callCount != callsAfterFirst {
		t.Errorf("embed cache miss: Embed called %d times total; expected %d after second call",
			fe.callCount, callsAfterFirst)
	}
}

// ---------------------------------------------------------------------------
// embedLRU — unit tests
// ---------------------------------------------------------------------------

func TestEmbedLRU_GetMiss(t *testing.T) {
	lru := newEmbedLRU(4)
	_, ok := lru.get("missing")
	if ok {
		t.Error("get on empty LRU should return false")
	}
}

func TestEmbedLRU_PutAndGet(t *testing.T) {
	lru := newEmbedLRU(4)
	vec := []float32{1, 2, 3}
	lru.put("k", vec)
	got, ok := lru.get("k")
	if !ok {
		t.Fatal("get after put should return true")
	}
	if len(got) != len(vec) || got[0] != 1 {
		t.Errorf("got %v, want %v", got, vec)
	}
}

func TestEmbedLRU_UpdateExisting(t *testing.T) {
	lru := newEmbedLRU(4)
	lru.put("k", []float32{1, 0, 0})
	lru.put("k", []float32{9, 9, 9}) // update same key
	got, ok := lru.get("k")
	if !ok {
		t.Fatal("expected hit")
	}
	if got[0] != 9 {
		t.Errorf("update not applied; got %v", got)
	}
}

func TestEmbedLRU_Eviction(t *testing.T) {
	// max=2; insert 3 entries — the first should be evicted.
	lru := newEmbedLRU(2)
	lru.put("a", []float32{1})
	lru.put("b", []float32{2})
	lru.put("c", []float32{3}) // "a" should be evicted

	_, okA := lru.get("a")
	_, okB := lru.get("b")
	_, okC := lru.get("c")

	if okA {
		t.Error("LRU eviction: 'a' should have been evicted")
	}
	if !okB {
		t.Error("LRU eviction: 'b' should still be present")
	}
	if !okC {
		t.Error("LRU eviction: 'c' should be present")
	}
}

func TestEmbedLRU_AccessMovesToFront(t *testing.T) {
	// max=2; insert a, b; access a (moves to front); insert c → b evicted, a kept.
	lru := newEmbedLRU(2)
	lru.put("a", []float32{1})
	lru.put("b", []float32{2})
	lru.get("a") // touch a → moves to front; b becomes LRU
	lru.put("c", []float32{3}) // should evict b, not a

	_, okA := lru.get("a")
	_, okB := lru.get("b")
	_, okC := lru.get("c")

	if !okA {
		t.Error("'a' should survive (was accessed most recently)")
	}
	if okB {
		t.Error("'b' should have been evicted (was LRU)")
	}
	if !okC {
		t.Error("'c' should be present")
	}
}

func TestEmbedLRU_MaxOneEntry(t *testing.T) {
	// Degenerate cache: capacity 1. Only the most recent entry survives.
	lru := newEmbedLRU(1)
	lru.put("first", []float32{1})
	lru.put("second", []float32{2})

	_, ok1 := lru.get("first")
	_, ok2 := lru.get("second")
	if ok1 {
		t.Error("capacity-1 LRU: 'first' should be evicted")
	}
	if !ok2 {
		t.Error("capacity-1 LRU: 'second' should be present")
	}
}

// ---------------------------------------------------------------------------
// FormatHits — additional coverage
// ---------------------------------------------------------------------------

func TestFormatHits_MultipleChunks(t *testing.T) {
	hits := []SearchHit{
		{ChunkID: "c1", DocTitle: "Doc A", DocSource: "a.md", Content: "first hit", Distance: 0.1},
		{ChunkID: "c2", DocTitle: "Doc B", DocSource: "b.md", Content: "second hit", Distance: 0.2},
		{ChunkID: "c3", DocTitle: "Doc C", DocSource: "c.md", Content: "third hit", Distance: 0.3},
	}
	out := FormatHits("multi-kb", "my query", hits)
	if !strings.Contains(out, "hits=3") {
		t.Errorf("FormatHits: expected hits=3 in output; got %s", out)
	}
	for i := 1; i <= 3; i++ {
		marker := fmt.Sprintf("index=%d", i)
		if !strings.Contains(out, marker) {
			t.Errorf("FormatHits: expected %q in output; got %s", marker, out)
		}
	}
}

func TestFormatHits_ContentWithNewlines(t *testing.T) {
	hits := []SearchHit{
		{DocTitle: "T", DocSource: "s.md", Content: "line one\nline two\nline three", Distance: 0},
	}
	out := FormatHits("kb", "q", hits)
	// The content should be indented (newlines replaced) — just verify no crash.
	if !strings.Contains(out, "kb_results") {
		t.Errorf("unexpected output: %s", out)
	}
}

func TestFormatHits_ExactlyAtTruncationBoundary(t *testing.T) {
	// 1500 chars exactly — should NOT be truncated.
	content := strings.Repeat("x", 1500)
	hits := []SearchHit{{DocTitle: "T", DocSource: "s.md", Content: content, Distance: 0}}
	out := FormatHits("kb", "q", hits)
	if strings.Contains(out, "…") {
		t.Error("content exactly 1500 chars should not be truncated")
	}
}

func TestFormatHits_OneOverTruncationBoundary(t *testing.T) {
	// 1501 chars — must be truncated.
	content := strings.Repeat("y", 1501)
	hits := []SearchHit{{DocTitle: "T", DocSource: "s.md", Content: content, Distance: 0}}
	out := FormatHits("kb", "q", hits)
	if !strings.Contains(out, "…") {
		t.Error("content of 1501 chars should be truncated with ellipsis")
	}
}

// ---------------------------------------------------------------------------
// SearchFTS — hasFTS5=false graceful degradation (no FTS5 in test build)
// ---------------------------------------------------------------------------

func TestStore_SearchFTS_NoFTS5IsGraceful(t *testing.T) {
	// In the test environment FTS5 may or may not be available. When it's not,
	// SearchFTS must return (nil, nil) — not an error.
	s := newTestStore2(t)
	kb, err := s.CreateKB(KB{
		Name: "fts-graceful", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 2,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}

	if s.hasFTS5 {
		// FTS5 is available in this build — skip the graceful-degradation path.
		t.Skip("FTS5 is available in this SQLite build; skipping hasFTS5=false test")
	}

	hits, err := s.SearchFTS(kb, "hello", 5)
	if err != nil {
		t.Errorf("SearchFTS with hasFTS5=false: expected nil error, got %v", err)
	}
	if hits != nil {
		t.Errorf("SearchFTS with hasFTS5=false: expected nil hits, got %v", hits)
	}
}

// ---------------------------------------------------------------------------
// Store.SearchHybrid — vec error + fts fallback
// ---------------------------------------------------------------------------

func TestStore_SearchHybrid_VecErrorWithFTSResults(t *testing.T) {
	// When vec search fails AND fts also returns empty (hasFTS5=false or no matches),
	// SearchHybrid returns an error. We test the vec-error path without worrying
	// about what fts returns by using a wrong-dim vector.
	s := newTestStore2(t)
	kb, err := s.CreateKB(KB{
		Name: "hybrid-err", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 4,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}

	// Wrong dimension vector causes vec search to fail; with no fts results
	// the hybrid must return an error.
	_, err = s.SearchHybrid(kb, []float32{1, 0}, "query", 3) // dim 2 != 4
	if err == nil {
		t.Error("SearchHybrid with wrong-dim vector and no FTS results should return error")
	}
}

// ---------------------------------------------------------------------------
// DeleteKB — cascade to vec table (regression: vec DROP must succeed even
// after the KB's vec0 table was never populated by AddDocument).
// ---------------------------------------------------------------------------

func TestStore_DeleteKB_EmptyVecTable(t *testing.T) {
	s := newTestStore2(t)
	kb, err := s.CreateKB(KB{
		Name: "delete-empty-vec", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 4,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	// No documents added — vec0 table is empty but exists.
	if err := s.DeleteKB(kb.Name); err != nil {
		t.Fatalf("DeleteKB on empty-vec KB: %v", err)
	}
	got, err := s.GetKB(kb.Name)
	if err != nil || got != nil {
		t.Errorf("KB should be gone; err=%v got=%v", err, got)
	}
}

// ---------------------------------------------------------------------------
// Store.DeleteDocument — delete from empty KB is no-op (not an error)
// ---------------------------------------------------------------------------

func TestStore_DeleteDocument_MissingDocIsNoOp(t *testing.T) {
	s := newTestStore2(t)
	kb, _ := s.CreateKB(KB{Name: "del-doc-noop", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 2})
	// Deleting a non-existent doc ID should return nil (no rows deleted, no error).
	err := s.DeleteDocument(kb.ID, "does-not-exist")
	if err != nil {
		t.Errorf("DeleteDocument non-existent: expected nil, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Store.SearchHybrid — topK clamping (fetchK minimum of 10)
// ---------------------------------------------------------------------------

func TestStore_SearchHybrid_SmallTopK(t *testing.T) {
	// topK=1 → fetchK=max(2,10)=10; should not panic or error on small KB.
	s := newTestStore2(t)
	kb, _ := s.CreateKB(KB{Name: "hybrid-small-topk", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 2})
	_, _ = s.AddDocument(kb, Document{Title: "d"},
		[]Chunk{{Content: "chunk content", Vector: []float32{1, 0}}})
	hits, err := s.SearchHybrid(kb, []float32{1, 0}, "chunk", 1)
	if err != nil {
		t.Fatalf("SearchHybrid small topK: %v", err)
	}
	if len(hits) > 1 {
		t.Errorf("topK=1: expected at most 1 hit, got %d", len(hits))
	}
}

// ---------------------------------------------------------------------------
// Service.Search — empty embed result error path
// ---------------------------------------------------------------------------

// emptyVecEmbedder returns an empty vector slice (no rows) to trigger the
// "empty embedding for query" error path in Service.Search.
type emptyVecEmbedder struct{}

func (e *emptyVecEmbedder) ID() string { return "empty-vec" }
func (e *emptyVecEmbedder) Embed(_ context.Context, _ string, _ []string) ([][]float32, error) {
	return [][]float32{}, nil // returns rows but the first row is absent
}
func (e *emptyVecEmbedder) Dim(_ context.Context, _ string) (int, error) { return 4, nil }

func TestServiceSearch_EmptyEmbedResult(t *testing.T) {
	store := newTestStore2(t)
	reg := llm.NewEmbedderRegistry()
	reg.Register(&emptyVecEmbedder{})
	svc := NewService(store, reg)

	_, err := store.CreateKB(KB{
		Name: "empty-vec-kb", EmbeddingProvider: "empty-vec", EmbeddingModel: "none", Dim: 4,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}

	_, err = svc.Search(context.Background(), "empty-vec-kb", "query", 5)
	if err == nil {
		t.Error("empty embed result should return error")
	}
	if !strings.Contains(err.Error(), "empty embedding") {
		t.Errorf("error should mention 'empty embedding': %v", err)
	}
}
