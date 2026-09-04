// service.go — high-level RAG facade used by the runtime and gateway.
//
// Service combines the SQLite-backed Store with an embedding registry so
// callers don't have to think about which embedder to pick for which KB.
// The runtime engine talks ONLY to Service (no direct Store access) so
// future backends (e.g. a remote vector DB) can be swapped in transparently.
package knowledge

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/soulacy/soulacy/internal/llm"
)

// KBSummary is the lightweight view of a KB used when listing what's
// available to an agent (no schema details, just name + size).
type KBSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	DocCount    int    `json:"document_count"`
	ChunkCount  int    `json:"chunk_count"`
}

// Service is the RAG facade.
type Service struct {
	Store     *Store
	Embedders *llm.EmbedderRegistry

	// embedCache memoises query embeddings to avoid re-embedding the same
	// query when the agent iterates (PRODUCTION_AUDIT → HIGH/Caching).
	// Bounded LRU keyed by provider|model|query. ~200ms saved per cache hit.
	embedCacheMu sync.Mutex
	embedCache   *embedLRU
}

const embedCacheMaxEntries = 256

// NewService wires a Store and an EmbedderRegistry together.
func NewService(store *Store, embedders *llm.EmbedderRegistry) *Service {
	return &Service{
		Store:      store,
		Embedders:  embedders,
		embedCache: newEmbedLRU(embedCacheMaxEntries),
	}
}

// ListAvailable returns KB summaries for the requested names. Names that
// don't resolve to an existing KB are silently dropped (they may have been
// declared in SOUL.yaml before the KB was created). Supports the "*" / "all"
// wildcards used by Skills for symmetry.
func (s *Service) ListAvailable(names []string) []KBSummary {
	if s == nil || s.Store == nil {
		return nil
	}
	wantAll := false
	for _, n := range names {
		if n == "*" || n == "all" {
			wantAll = true
			break
		}
	}
	if wantAll {
		kbs, err := s.Store.ListKBs()
		if err != nil {
			return nil
		}
		out := make([]KBSummary, 0, len(kbs))
		for _, kb := range kbs {
			out = append(out, KBSummary{Name: kb.Name, Description: kb.Description, DocCount: kb.DocumentCount, ChunkCount: kb.ChunkCount})
		}
		return out
	}
	out := make([]KBSummary, 0, len(names))
	for _, n := range names {
		kb, err := s.Store.GetKB(n)
		if err != nil || kb == nil {
			continue
		}
		out = append(out, KBSummary{Name: kb.Name, Description: kb.Description, DocCount: kb.DocumentCount, ChunkCount: kb.ChunkCount})
	}
	return out
}

// Search embeds the query, runs a hybrid (vector + FTS5) search against the
// named KB, and returns a pre-formatted text block ready for the LLM to
// consume as a tool result.
//
// Internally this calls Store.SearchHybrid, which fuses vector KNN and BM25
// results via Reciprocal Rank Fusion. If FTS5 is not compiled into the SQLite
// build (hasFTS5 == false in the Store), SearchHybrid degrades gracefully to
// vector-only results with no error — callers of Service.Search never need to
// handle the FTS5-absent case specially.
func (s *Service) Search(ctx context.Context, kbName, query string, topK int) (string, error) {
	if s == nil || s.Store == nil {
		return "", errors.New("knowledge: service not configured")
	}
	if strings.TrimSpace(query) == "" {
		return "", errors.New("kb_search: query is required")
	}
	if topK <= 0 {
		topK = 5
	}

	kb, err := s.Store.GetKB(kbName)
	if err != nil {
		return "", err
	}
	if kb == nil {
		return "", fmt.Errorf("kb_search: knowledge base %q not found", kbName)
	}

	if s.Embedders == nil {
		return "", errors.New("kb_search: no embedder registry configured")
	}
	embedder := s.Embedders.Get(kb.EmbeddingProvider)
	if embedder == nil {
		return "", fmt.Errorf("kb_search: no embedder registered for provider %q (KB %q)", kb.EmbeddingProvider, kbName)
	}

	// Cache hit avoids a round-trip to the embedder. Cache key includes the
	// provider+model so different KBs configured with different embedders
	// don't share a stale entry.
	cacheKey := kb.EmbeddingProvider + "|" + kb.EmbeddingModel + "|" + query
	var queryVec []float32
	s.embedCacheMu.Lock()
	if cached, ok := s.embedCache.get(cacheKey); ok {
		queryVec = cached
	}
	s.embedCacheMu.Unlock()
	if queryVec == nil {
		vecs, err := embedder.Embed(ctx, kb.EmbeddingModel, []string{query})
		if err != nil {
			return "", fmt.Errorf("kb_search: embed query: %w", err)
		}
		if len(vecs) == 0 || len(vecs[0]) == 0 {
			return "", fmt.Errorf("kb_search: empty embedding for query")
		}
		queryVec = vecs[0]
		s.embedCacheMu.Lock()
		s.embedCache.put(cacheKey, queryVec)
		s.embedCacheMu.Unlock()
	}

	hits, err := s.Store.SearchHybrid(kb, queryVec, query, topK)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return fmt.Sprintf("No matching passages found in knowledge base %q for query %q.", kbName, query), nil
	}

	return FormatHits(kbName, query, hits), nil
}

// IngestText stores text content in a named knowledge base using the same
// chunking and embedding pipeline as the REST ingestion endpoint.
//
// It is now a thin wrapper over ingestExtracted so the synchronous path (the
// kb_write tool) and the async worker share ONE implementation — the gateway
// used to carry a third, duplicated copy of this pipeline.
func (s *Service) IngestText(ctx context.Context, kbName, title, source, mimeType, content string) (*Document, error) {
	if s == nil || s.Store == nil {
		return nil, errors.New("knowledge: service not configured")
	}
	if strings.TrimSpace(content) == "" {
		return nil, errors.New("kb_write: content is required")
	}
	text, err := ExtractText(mimeType, []byte(content))
	if err != nil {
		return nil, fmt.Errorf("kb_write: extract text: %w", err)
	}
	return s.ingestExtracted(ctx, kbName, title, source, mimeType, text, nil)
}

// ingestExtracted is the single ingestion pipeline: chunk → embed (batched) →
// store. `report`, when non-nil, is called with 0..100 after each embedding
// batch so a long document shows live progress instead of appearing hung.
func (s *Service) ingestExtracted(
	ctx context.Context,
	kbName, title, source, mimeType, text string,
	report func(pct int),
) (*Document, error) {
	if s == nil || s.Store == nil {
		return nil, errors.New("knowledge: service not configured")
	}
	kbName = strings.TrimSpace(kbName)
	if kbName == "" {
		return nil, errors.New("kb_write: kb is required")
	}
	kb, err := s.Store.GetKB(kbName)
	if err != nil {
		return nil, err
	}
	if kb == nil {
		return nil, fmt.Errorf("kb_write: knowledge base %q not found", kbName)
	}
	if strings.TrimSpace(title) == "" {
		title = "Untitled document"
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("kb_write: document produced no extractable text")
	}
	if s.Embedders == nil {
		return nil, errors.New("kb_write: no embedder registry configured")
	}
	embedder := s.Embedders.Get(kb.EmbeddingProvider)
	if embedder == nil {
		return nil, fmt.Errorf("kb_write: no embedder registered for provider %q (KB %q)", kb.EmbeddingProvider, kbName)
	}

	parentSize := kb.ChunkSize * 4
	if parentSize < 1 {
		parentSize = 4000
	}
	parentTexts := ChunkText(text, parentSize, kb.ChunkOverlap*2)
	childTexts := ChunkText(text, kb.ChunkSize, kb.ChunkOverlap)
	if len(childTexts) == 0 {
		return nil, errors.New("kb_write: no chunks produced")
	}

	const batchSize = 64
	vecs := make([][]float32, 0, len(childTexts))
	for i := 0; i < len(childTexts); i += batchSize {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("kb_write: cancelled before embed batch %d: %w", i, err)
		}
		end := i + batchSize
		if end > len(childTexts) {
			end = len(childTexts)
		}
		batch, eerr := embedder.Embed(ctx, kb.EmbeddingModel, childTexts[i:end])
		if eerr != nil {
			return nil, fmt.Errorf("kb_write: embed batch %d-%d: %w", i, end, eerr)
		}
		vecs = append(vecs, batch...)
		// Embedding dominates the wall-clock cost, so batch completion is the
		// honest progress signal. Reserve the last few percent for the write.
		if report != nil {
			report(int(float64(len(vecs)) / float64(len(childTexts)) * 95))
		}
	}
	if len(vecs) != len(childTexts) {
		return nil, fmt.Errorf("kb_write: embedder returned %d vectors for %d chunks", len(vecs), len(childTexts))
	}

	parentIDs := make([]string, len(parentTexts))
	for i := range parentTexts {
		parentIDs[i] = uuid.New().String()
	}
	parentEndRunes := make([]int, len(parentTexts))
	pos := 0
	for i, p := range parentTexts {
		pos += len([]rune(p))
		parentEndRunes[i] = pos
	}

	allChunks := make([]Chunk, 0, len(parentTexts)+len(childTexts))
	for i, pt := range parentTexts {
		allChunks = append(allChunks, Chunk{ID: parentIDs[i], Content: pt})
	}
	childRunePos := 0
	parentIdx := 0
	for i, ct := range childTexts {
		cr := len([]rune(ct))
		mid := childRunePos + cr/2
		for parentIdx < len(parentEndRunes)-1 && mid > parentEndRunes[parentIdx] {
			parentIdx++
		}
		parentID := ""
		if len(parentIDs) > 0 {
			parentID = parentIDs[parentIdx]
		}
		allChunks = append(allChunks, Chunk{
			Content:       ct,
			ParentChunkID: parentID,
			Vector:        vecs[i],
		})
		childRunePos += cr
	}

	doc, err := s.Store.AddDocument(kb, Document{
		Title:    title,
		Source:   source,
		MIMEType: mimeType,
		ByteSize: int64(len(text)),
	}, allChunks)
	if err != nil {
		return nil, err
	}
	if report != nil {
		report(100)
	}
	return doc, nil
}

// FormatHits renders search results as a compact XML-ish block. Designed to
// minimise tokens while staying easy for the LLM to parse: each chunk is its
// own tag with source/score attributes.
func FormatHits(kbName, query string, hits []SearchHit) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<kb_results kb=%q query=%q hits=%d>\n", kbName, query, len(hits)))
	for i, h := range hits {
		// Trim absurdly long chunks so a single hit can't blow the context.
		content := h.Content
		if len(content) > 1500 {
			content = content[:1500] + "…"
		}
		sb.WriteString(fmt.Sprintf("  <chunk index=%d source=%q title=%q distance=%.4f>\n",
			i+1, h.DocSource, h.DocTitle, h.Distance))
		sb.WriteString("    ")
		sb.WriteString(strings.ReplaceAll(content, "\n", "\n    "))
		sb.WriteString("\n  </chunk>\n")
	}
	sb.WriteString("</kb_results>")
	return sb.String()
}

// ─── embedLRU: bounded LRU for query embeddings ────────────────────────────
//
// Tiny hand-rolled LRU. `list.List` for recency order; map for O(1) lookup.
// Not safe for concurrent access — Service guards it with embedCacheMu.

type embedLRU struct {
	max     int
	order   *list.List
	entries map[string]*list.Element
}

type embedEntry struct {
	key string
	vec []float32
}

func newEmbedLRU(max int) *embedLRU {
	return &embedLRU{
		max:     max,
		order:   list.New(),
		entries: make(map[string]*list.Element, max),
	}
}

func (c *embedLRU) get(k string) ([]float32, bool) {
	el, ok := c.entries[k]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*embedEntry).vec, true
}

func (c *embedLRU) put(k string, v []float32) {
	if el, ok := c.entries[k]; ok {
		el.Value.(*embedEntry).vec = v
		c.order.MoveToFront(el)
		return
	}
	el := c.order.PushFront(&embedEntry{key: k, vec: v})
	c.entries[k] = el
	if c.order.Len() > c.max {
		evict := c.order.Back()
		if evict != nil {
			c.order.Remove(evict)
			delete(c.entries, evict.Value.(*embedEntry).key)
		}
	}
}
