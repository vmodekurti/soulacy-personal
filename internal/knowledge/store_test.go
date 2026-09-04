// store_test.go — round-trip tests for the knowledge store. Uses a temp
// SQLite file under t.TempDir() so each test gets a fresh schema. Tests
// real sqlite-vec because the autoOnce + cgo binding makes a stub awkward;
// build with CGO_ENABLED=1 (the project default).
package knowledge

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kb.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStore_CreateAndListKB(t *testing.T) {
	s := newTestStore(t)

	kb, err := s.CreateKB(KB{
		Name: "test-kb", Description: "for tests",
		EmbeddingProvider: "ollama", EmbeddingModel: "nomic-embed-text",
		Dim: 768, ChunkSize: 500, ChunkOverlap: 100,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	if kb.ID == "" {
		t.Error("CreateKB should assign an ID")
	}
	if kb.Dim != 768 {
		t.Errorf("dim: got %d, want 768", kb.Dim)
	}

	kbs, err := s.ListKBs()
	if err != nil {
		t.Fatalf("ListKBs: %v", err)
	}
	if len(kbs) != 1 {
		t.Fatalf("ListKBs: got %d KBs, want 1", len(kbs))
	}
	if kbs[0].Name != "test-kb" {
		t.Errorf("name: got %q, want test-kb", kbs[0].Name)
	}
}

func TestStore_DuplicateKBNameRejected(t *testing.T) {
	s := newTestStore(t)
	base := KB{Name: "dup", EmbeddingProvider: "ollama", EmbeddingModel: "nomic-embed-text", Dim: 4}
	if _, err := s.CreateKB(base); err != nil {
		t.Fatalf("first CreateKB: %v", err)
	}
	if _, err := s.CreateKB(base); err == nil {
		t.Error("expected duplicate-name CreateKB to fail, got nil")
	}
}

func TestStore_AddDocumentAndSearch(t *testing.T) {
	s := newTestStore(t)
	kb, err := s.CreateKB(KB{
		Name: "search-kb",
		EmbeddingProvider: "test", EmbeddingModel: "fake",
		Dim: 4, ChunkSize: 100, ChunkOverlap: 0,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}

	// Three chunks with simple distinct vectors so KNN ordering is predictable.
	chunks := []Chunk{
		{Content: "alpha document", Vector: []float32{1, 0, 0, 0}},
		{Content: "beta document",  Vector: []float32{0, 1, 0, 0}},
		{Content: "gamma document", Vector: []float32{0, 0, 1, 0}},
	}
	doc, err := s.AddDocument(kb, Document{Title: "vectors", Source: "test.md", MIMEType: "text/markdown"}, chunks)
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}
	if doc.ChunkCount != 3 {
		t.Errorf("chunk_count: got %d, want 3", doc.ChunkCount)
	}

	// Query close to chunk 0's vector — it should rank first.
	hits, err := s.Search(kb, []float32{0.9, 0.1, 0, 0}, 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits: got %d, want 2", len(hits))
	}
	if hits[0].Content != "alpha document" {
		t.Errorf("top hit: got %q, want %q", hits[0].Content, "alpha document")
	}
	if hits[0].Distance > hits[1].Distance {
		t.Errorf("hits not sorted by distance ascending: %v vs %v", hits[0].Distance, hits[1].Distance)
	}
}

func TestStore_AddDocumentRejectsWrongDim(t *testing.T) {
	s := newTestStore(t)
	kb, _ := s.CreateKB(KB{Name: "dim-kb", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 4})
	bad := []Chunk{{Content: "wrong size", Vector: []float32{1, 0, 0}}} // 3 dims, not 4
	if _, err := s.AddDocument(kb, Document{Title: "bad"}, bad); err == nil {
		t.Error("expected AddDocument to reject mismatched-dim chunk, got nil")
	}
}

func TestStore_DeleteDocumentCascadesToChunks(t *testing.T) {
	s := newTestStore(t)
	kb, _ := s.CreateKB(KB{Name: "cascade", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 2})
	chunks := []Chunk{
		{Content: "a", Vector: []float32{1, 0}},
		{Content: "b", Vector: []float32{0, 1}},
	}
	doc, err := s.AddDocument(kb, Document{Title: "tmp"}, chunks)
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}

	if err := s.DeleteDocument(kb.ID, doc.ID); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}

	// After delete, the KB's chunk count should be zero (vec0 rows + chunks
	// table both purged).
	got, err := s.GetKB("cascade")
	if err != nil || got == nil {
		t.Fatalf("GetKB after delete: %v / nil=%v", err, got == nil)
	}
	if got.ChunkCount != 0 {
		t.Errorf("post-delete chunk count: got %d, want 0", got.ChunkCount)
	}
}

func TestStore_DeleteKBIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	if err := s.DeleteKB("never-existed"); err != nil {
		t.Errorf("deleting nonexistent KB should be no-op, got %v", err)
	}
}

// ─── GetKB ───────────────────────────────────────────────────────────────────

func TestStore_GetKB_NotFound(t *testing.T) {
	s := newTestStore(t)
	kb, err := s.GetKB("does-not-exist")
	if err != nil {
		t.Fatalf("GetKB nonexistent: unexpected error %v", err)
	}
	if kb != nil {
		t.Errorf("expected nil KB for nonexistent name, got %+v", kb)
	}
}

func TestStore_GetKB_CaseSensitive(t *testing.T) {
	s := newTestStore(t)
	_, err := s.CreateKB(KB{
		Name: "MyKB", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 2,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	// Exact name matches.
	kb, err := s.GetKB("MyKB")
	if err != nil || kb == nil {
		t.Fatalf("GetKB exact: err=%v kb=%v", err, kb)
	}
	// Different case must not match.
	kb2, err := s.GetKB("mykb")
	if err != nil {
		t.Fatalf("GetKB wrong case unexpected err: %v", err)
	}
	if kb2 != nil {
		t.Errorf("GetKB should be case-sensitive; got non-nil for 'mykb'")
	}
}

// ─── CreateKB validation ─────────────────────────────────────────────────────

func TestStore_CreateKB_MissingName(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateKB(KB{EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 4}); err == nil {
		t.Error("expected error for missing name, got nil")
	}
}

func TestStore_CreateKB_ZeroDim(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateKB(KB{Name: "nodim", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 0}); err == nil {
		t.Error("expected error for zero dim, got nil")
	}
}

func TestStore_CreateKB_DefaultsApplied(t *testing.T) {
	s := newTestStore(t)
	kb, err := s.CreateKB(KB{
		Name: "defaults-kb", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 4,
		// ChunkSize and ChunkOverlap intentionally zero — should default.
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	if kb.ChunkSize != 1000 {
		t.Errorf("ChunkSize default: got %d, want 1000", kb.ChunkSize)
	}
	// ChunkOverlap only defaults to 200 when negative; zero is valid (no overlap between chunks).
	if kb.ChunkOverlap < 0 {
		t.Errorf("ChunkOverlap should not be negative, got %d", kb.ChunkOverlap)
	}
	if kb.ID == "" {
		t.Error("ID should be auto-assigned")
	}
	if kb.CreatedAt.IsZero() {
		t.Error("CreatedAt should be auto-set")
	}
}

// ─── DeleteKB ────────────────────────────────────────────────────────────────

func TestStore_DeleteKB_RemovesKBAndDocs(t *testing.T) {
	s := newTestStore(t)
	kb, err := s.CreateKB(KB{
		Name: "to-delete", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 2,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	_, _ = s.AddDocument(kb, Document{Title: "doc"},
		[]Chunk{{Content: "hello", Vector: []float32{1, 0}}})

	if err := s.DeleteKB("to-delete"); err != nil {
		t.Fatalf("DeleteKB: %v", err)
	}

	got, err := s.GetKB("to-delete")
	if err != nil {
		t.Fatalf("GetKB after delete: %v", err)
	}
	if got != nil {
		t.Errorf("KB should be gone after delete, got %+v", got)
	}
}

// ─── ListDocuments ───────────────────────────────────────────────────────────

func TestStore_ListDocuments_Empty(t *testing.T) {
	s := newTestStore(t)
	kb, _ := s.CreateKB(KB{Name: "empty-kb", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 2})
	docs, err := s.ListDocuments(kb.ID)
	if err != nil {
		t.Fatalf("ListDocuments empty: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("expected 0 docs, got %d", len(docs))
	}
}

func TestStore_ListDocuments_MultipleDocsNewestFirst(t *testing.T) {
	s := newTestStore(t)
	kb, _ := s.CreateKB(KB{Name: "list-kb", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 2})

	for _, title := range []string{"first", "second", "third"} {
		_, err := s.AddDocument(kb, Document{Title: title},
			[]Chunk{{Content: title + " chunk", Vector: []float32{1, 0}}})
		if err != nil {
			t.Fatalf("AddDocument %q: %v", title, err)
		}
	}

	docs, err := s.ListDocuments(kb.ID)
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("expected 3 docs, got %d", len(docs))
	}
	// Newest first: third was added last.
	if docs[0].Title != "third" {
		t.Errorf("first listed doc should be newest ('third'), got %q", docs[0].Title)
	}
	// Each should have chunk counts.
	for _, d := range docs {
		if d.ChunkCount != 1 {
			t.Errorf("doc %q: chunk_count=%d, want 1", d.Title, d.ChunkCount)
		}
	}
}

func TestStore_ListDocuments_IsolatedByKB(t *testing.T) {
	s := newTestStore(t)
	kb1, _ := s.CreateKB(KB{Name: "kb1", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 2})
	kb2, _ := s.CreateKB(KB{Name: "kb2", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 2})

	_, _ = s.AddDocument(kb1, Document{Title: "in-kb1"},
		[]Chunk{{Content: "data", Vector: []float32{1, 0}}})

	docs, err := s.ListDocuments(kb2.ID)
	if err != nil {
		t.Fatalf("ListDocuments kb2: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("kb2 should have no docs, got %d", len(docs))
	}
}

// ─── AddDocument edge cases ───────────────────────────────────────────────────

func TestStore_AddDocument_NilKB(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.AddDocument(nil, Document{Title: "x"}, nil); err == nil {
		t.Error("expected error for nil KB, got nil")
	}
}

func TestStore_AddDocument_AutoSHA256(t *testing.T) {
	s := newTestStore(t)
	kb, _ := s.CreateKB(KB{Name: "sha-kb", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 2})
	doc, err := s.AddDocument(kb, Document{Title: "auto-sha"},
		[]Chunk{{Content: "chunk content", Vector: []float32{1, 0}}})
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}
	if doc.SHA256 == "" {
		t.Error("SHA256 should be auto-computed when not provided")
	}
}

func TestStore_AddDocument_ParentChildChunks(t *testing.T) {
	// A parent chunk with no vector and a child chunk pointing to it.
	s := newTestStore(t)
	kb, _ := s.CreateKB(KB{Name: "parent-child-kb", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 2})

	parentID := "parent-id-1"
	chunks := []Chunk{
		{ID: parentID, Content: "parent wide context", Vector: nil}, // no vector = parent
		{Content: "child narrow", Vector: []float32{1, 0}, ParentChunkID: parentID},
	}
	doc, err := s.AddDocument(kb, Document{Title: "hierarchical"}, chunks)
	if err != nil {
		t.Fatalf("AddDocument parent-child: %v", err)
	}
	if doc.ChunkCount != 2 {
		t.Errorf("chunk_count: got %d, want 2", doc.ChunkCount)
	}
}

// ─── Search edge cases ────────────────────────────────────────────────────────

func TestStore_Search_NilKB(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Search(nil, []float32{1, 0}, 3); err == nil {
		t.Error("expected error for nil KB, got nil")
	}
}

func TestStore_Search_WrongDim(t *testing.T) {
	s := newTestStore(t)
	kb, _ := s.CreateKB(KB{Name: "dim4", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 4})
	if _, err := s.Search(kb, []float32{1, 0}, 3); err == nil {
		t.Error("expected error for mismatched query dim, got nil")
	}
}

func TestStore_Search_EmptyKB(t *testing.T) {
	s := newTestStore(t)
	kb, _ := s.CreateKB(KB{Name: "empty-search", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 2})
	hits, err := s.Search(kb, []float32{1, 0}, 5)
	if err != nil {
		t.Fatalf("Search empty KB: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected 0 hits, got %d", len(hits))
	}
}

func TestStore_Search_DefaultTopK(t *testing.T) {
	// topK <= 0 should default to 5 internally (no error, just returns whatever's there).
	s := newTestStore(t)
	kb, _ := s.CreateKB(KB{Name: "topk-kb", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 2})
	_, _ = s.AddDocument(kb, Document{Title: "d"},
		[]Chunk{{Content: "c", Vector: []float32{1, 0}}})
	hits, err := s.Search(kb, []float32{1, 0}, 0)
	if err != nil {
		t.Fatalf("Search topK=0: %v", err)
	}
	// Should return 1 result (only 1 chunk inserted).
	if len(hits) != 1 {
		t.Errorf("expected 1 hit, got %d", len(hits))
	}
}

// ─── SearchHybrid ─────────────────────────────────────────────────────────────

func TestStore_SearchHybrid_ReturnsResults(t *testing.T) {
	s := newTestStore(t)
	kb, err := s.CreateKB(KB{
		Name: "hybrid-kb", EmbeddingProvider: "x", EmbeddingModel: "x",
		Dim: 4, ChunkSize: 100, ChunkOverlap: 0,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}

	chunks := []Chunk{
		{Content: "alpha retrieval", Vector: []float32{1, 0, 0, 0}},
		{Content: "beta retrieval",  Vector: []float32{0, 1, 0, 0}},
		{Content: "gamma topic",     Vector: []float32{0, 0, 1, 0}},
	}
	if _, err := s.AddDocument(kb, Document{Title: "hybrid-doc", Source: "src.md"}, chunks); err != nil {
		t.Fatalf("AddDocument: %v", err)
	}

	hits, err := s.SearchHybrid(kb, []float32{0.9, 0.1, 0, 0}, "alpha retrieval", 2)
	if err != nil {
		t.Fatalf("SearchHybrid: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("SearchHybrid: expected at least one hit")
	}
	// The alpha chunk should appear in top results given the vector is nearest.
	found := false
	for _, h := range hits {
		if h.Content == "alpha retrieval" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'alpha retrieval' in hybrid results, got: %+v", hits)
	}
}

func TestStore_SearchHybrid_EmptyKB(t *testing.T) {
	s := newTestStore(t)
	kb, _ := s.CreateKB(KB{Name: "hybrid-empty", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 2})
	hits, err := s.SearchHybrid(kb, []float32{1, 0}, "anything", 5)
	if err != nil {
		t.Fatalf("SearchHybrid empty: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected 0 hits on empty KB, got %d", len(hits))
	}
}

// ─── nullableString ──────────────────────────────────────────────────────────

func TestNullableString(t *testing.T) {
	if v := nullableString(""); v != nil {
		t.Errorf("nullableString(''): expected nil, got %v", v)
	}
	if v := nullableString("hello"); v != "hello" {
		t.Errorf("nullableString('hello'): expected 'hello', got %v", v)
	}
}

// ─── minInt ───────────────────────────────────────────────────────────────────

func TestMinInt(t *testing.T) {
	cases := [][3]int{{1, 2, 1}, {5, 3, 3}, {4, 4, 4}}
	for _, c := range cases {
		if got := minInt(c[0], c[1]); got != c[2] {
			t.Errorf("minInt(%d, %d) = %d, want %d", c[0], c[1], got, c[2])
		}
	}
}

// ─── ListKBs ordering ────────────────────────────────────────────────────────

func TestStore_ListKBs_AlphaOrder(t *testing.T) {
	s := newTestStore(t)
	for _, name := range []string{"zebra", "apple", "mango"} {
		if _, err := s.CreateKB(KB{Name: name, EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 2}); err != nil {
			t.Fatalf("CreateKB %q: %v", name, err)
		}
	}
	kbs, err := s.ListKBs()
	if err != nil {
		t.Fatalf("ListKBs: %v", err)
	}
	if len(kbs) != 3 {
		t.Fatalf("ListKBs count = %d, want 3", len(kbs))
	}
	if kbs[0].Name != "apple" || kbs[1].Name != "mango" || kbs[2].Name != "zebra" {
		t.Errorf("ListKBs not in alpha order: %v", []string{kbs[0].Name, kbs[1].Name, kbs[2].Name})
	}
}

// ─── vecTable helper ─────────────────────────────────────────────────────────

func TestVecTable(t *testing.T) {
	id := "abc-123-def"
	got := vecTable(id)
	want := `"vec_abc_123_def"`
	if got != want {
		t.Errorf("vecTable(%q) = %q, want %q", id, got, want)
	}
}

// ─── FormatHits (service.go) ─────────────────────────────────────────────────

func TestFormatHits_Basic(t *testing.T) {
	hits := []SearchHit{
		{ChunkID: "c1", DocID: "d1", DocTitle: "My Doc", DocSource: "src.md", Ordinal: 0, Content: "some content here", Distance: 0.12},
	}
	out := FormatHits("my-kb", "test query", hits)
	for _, substr := range []string{"my-kb", "test query", "My Doc", "some content here", "0.1200"} {
		if !containsStr(out, substr) {
			t.Errorf("FormatHits output missing %q:\n%s", substr, out)
		}
	}
}

func TestFormatHits_TruncatesLongContent(t *testing.T) {
	longContent := make([]byte, 2000)
	for i := range longContent {
		longContent[i] = 'a'
	}
	hits := []SearchHit{{Content: string(longContent), DocTitle: "big", DocSource: "big.md"}}
	out := FormatHits("kb", "q", hits)
	// Should contain the ellipsis truncation marker.
	if !containsStr(out, "…") {
		t.Error("FormatHits should truncate long content with ellipsis")
	}
}

func TestFormatHits_Empty(t *testing.T) {
	out := FormatHits("kb", "q", nil)
	if !containsStr(out, "hits=0") {
		t.Errorf("FormatHits with no hits should include hits=0: %s", out)
	}
}

// containsStr is a small helper to avoid importing strings in test file.
func containsStr(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
