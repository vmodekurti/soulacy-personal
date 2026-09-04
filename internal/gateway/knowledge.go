// knowledge.go — REST endpoints for the RAG layer.
//
// Endpoints (all under /api/v1):
//
//	GET    /knowledge                       — list KBs (with counts)
//	POST   /knowledge                       — create KB (body: name, description, embedding_provider, embedding_model)
//	DELETE /knowledge/:kb                   — drop KB + all docs/chunks
//	GET    /knowledge/:kb/documents         — list documents in a KB
//	POST   /knowledge/:kb/documents         — ingest a document (JSON body or multipart upload)
//	DELETE /knowledge/:kb/documents/:doc    — delete a single document
//	POST   /knowledge/:kb/search            — quick search (used by the GUI's test box)
//
// All handlers require the API key middleware applied by the parent router.
package gateway

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/knowledge"
)

// handleListKnowledge returns every KB in the store.
func (s *Server) handleListKnowledge(c *fiber.Ctx) error {
	svc := s.engine.Knowledge()
	if svc == nil {
		return c.JSON(fiber.Map{"knowledge_bases": []any{}, "enabled": false})
	}
	kbs, err := svc.Store.ListKBs()
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{
		"knowledge_bases":            kbs,
		"enabled":                    true,
		"default_embedding_provider": s.cfg.Knowledge.EmbeddingProvider,
		"default_embedding_model":    s.cfg.Knowledge.EmbeddingModel,
		"embedding_providers":        s.embeddingProviderCatalog(),
	})
}

func (s *Server) embeddingProviderCatalog() []fiber.Map {
	svc := s.engine.Knowledge()
	if svc == nil || svc.Embedders == nil {
		return []fiber.Map{}
	}
	ids := svc.Embedders.IDs()
	sort.Strings(ids)
	out := make([]fiber.Map, 0, len(ids))
	for _, id := range ids {
		models := embeddingModelOptions(id)
		if id == s.cfg.Knowledge.EmbeddingProvider && s.cfg.Knowledge.EmbeddingModel != "" {
			models = prependUnique(models, s.cfg.Knowledge.EmbeddingModel)
		}
		out = append(out, fiber.Map{
			"id":            id,
			"default_model": firstNonEmpty(providerDefaultEmbeddingModel(id), s.cfg.Knowledge.EmbeddingModel),
			"models":        models,
		})
	}
	return out
}

func embeddingModelOptions(provider string) []string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "ollama":
		return []string{"nomic-embed-text", "mxbai-embed-large", "all-minilm"}
	case "openai":
		return []string{"text-embedding-3-small", "text-embedding-3-large", "text-embedding-ada-002"}
	case "google", "gemini":
		return []string{"gemini-embedding-001", "text-embedding-004"}
	case "ollama_cloud":
		return []string{"nomic-embed-text", "mxbai-embed-large", "all-minilm"}
	case "nvidia":
		return []string{"nvidia/nv-embedqa-e5-v5", "nvidia/llama-3.2-nv-embedqa-1b-v2"}
	case "openroute", "openrouter", "together", "groq", "mistral", "deepseek":
		return []string{"text-embedding-3-small", "text-embedding-3-large"}
	default:
		return nil
	}
}

func providerDefaultEmbeddingModel(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "ollama":
		return "nomic-embed-text"
	case "openai":
		return "text-embedding-3-small"
	case "google", "gemini":
		return "gemini-embedding-001"
	case "ollama_cloud":
		return "nomic-embed-text"
	case "nvidia":
		return "nvidia/nv-embedqa-e5-v5"
	case "openroute", "openrouter", "together", "groq", "mistral", "deepseek":
		return "text-embedding-3-small"
	default:
		return ""
	}
}

func prependUnique(xs []string, first string) []string {
	first = strings.TrimSpace(first)
	if first == "" {
		return xs
	}
	out := []string{first}
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x != "" && x != first {
			out = append(out, x)
		}
	}
	return out
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

func knowledgeKBParam(c *fiber.Ctx) string {
	raw := c.Params("kb")
	if raw == "" {
		return raw
	}
	name, err := url.PathUnescape(raw)
	if err != nil {
		return raw
	}
	return name
}

// handleCreateKnowledge creates a new KB. The embedding dim is probed against
// the chosen embedder, so the user only needs to pick a provider+model.
func (s *Server) handleCreateKnowledge(c *fiber.Ctx) error {
	svc := s.engine.Knowledge()
	if svc == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "knowledge store disabled (set knowledge.db_path in config.yaml)")
	}
	var body struct {
		Name              string `json:"name"`
		Description       string `json:"description"`
		EmbeddingProvider string `json:"embedding_provider"`
		EmbeddingModel    string `json:"embedding_model"`
		ChunkSize         int    `json:"chunk_size"`
		ChunkOverlap      int    `json:"chunk_overlap"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "name is required")
	}
	if body.EmbeddingProvider == "" {
		body.EmbeddingProvider = s.cfg.Knowledge.EmbeddingProvider
	}
	if body.EmbeddingModel == "" {
		body.EmbeddingModel = s.cfg.Knowledge.EmbeddingModel
	}
	if body.ChunkSize == 0 {
		body.ChunkSize = s.cfg.Knowledge.ChunkSize
		if body.ChunkSize == 0 {
			body.ChunkSize = 1000
		}
	}
	if body.ChunkOverlap == 0 {
		body.ChunkOverlap = s.cfg.Knowledge.ChunkOverlap
	}

	embedder := svc.Embedders.Get(body.EmbeddingProvider)
	if embedder == nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": fmt.Sprintf("embedding provider %q not registered (available: %v)", body.EmbeddingProvider, svc.Embedders.IDs()),
		})
	}

	dim, err := embedder.Dim(c.Context(), body.EmbeddingModel)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": fmt.Sprintf("probe embedding model %q on %q: %v", body.EmbeddingModel, body.EmbeddingProvider, err),
		})
	}

	kb, err := svc.Store.CreateKB(knowledge.KB{
		Name:              body.Name,
		Description:       body.Description,
		EmbeddingProvider: body.EmbeddingProvider,
		EmbeddingModel:    body.EmbeddingModel,
		Dim:               dim,
		ChunkSize:         body.ChunkSize,
		ChunkOverlap:      body.ChunkOverlap,
	})
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	s.log.Info("knowledge: kb created", zap.String("name", kb.Name), zap.Int("dim", kb.Dim))
	return c.Status(fiber.StatusCreated).JSON(kb)
}

// handleDeleteKnowledge drops a KB.
func (s *Server) handleDeleteKnowledge(c *fiber.Ctx) error {
	svc := s.engine.Knowledge()
	if svc == nil {
		return c.SendStatus(fiber.StatusNoContent)
	}
	name := knowledgeKBParam(c)
	if err := svc.Store.DeleteKB(name); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.log.Info("knowledge: kb deleted", zap.String("name", name))
	return c.SendStatus(fiber.StatusNoContent)
}

// handleListKnowledgeDocuments returns documents in a KB.
func (s *Server) handleListKnowledgeDocuments(c *fiber.Ctx) error {
	svc := s.engine.Knowledge()
	if svc == nil {
		return c.JSON(fiber.Map{"documents": []any{}})
	}
	kb, err := svc.Store.GetKB(knowledgeKBParam(c))
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if kb == nil {
		return s.errMsg(c, fiber.StatusNotFound, "kb not found")
	}
	docs, err := svc.Store.ListDocuments(kb.ID)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{"kb": kb, "documents": docs})
}

// handleIngestDocument adds a document to a KB. Accepts either:
//   - JSON  { "title": "...", "source": "...", "content": "..." }
//   - multipart form: file field "file" (filename used as title/source).
//
// Parent-child chunking:
//
// To maximise retrieval recall without inflating context windows, documents are
// chunked at two granularities:
//
//   - Parent chunks (context windows): ~4× chunk_size characters each. These
//     are stored in the chunks table but carry NO embedding and are never
//     inserted into the vec0 or FTS5 tables. They exist purely to provide a
//     wider excerpt to the LLM after a child retrieval hit. Parents have no
//     parent_chunk_id (NULL).
//
//   - Child chunks (retrieval windows): chunk_size characters each, with
//     chunk_overlap overlap. These are embedded and inserted into both vec0
//     (for KNN) and chunks_fts (for BM25). Each child references the parent
//     that covers it via parent_chunk_id.
//
// Parent assignment uses character midpoint: for each child chunk the handler
// computes the rune offset of its midpoint within the full document text and
// advances a cursor through the cumulative rune lengths of the parent chunks
// until it finds the one whose range covers that midpoint. This is O(n) in the
// number of chunks and requires only the pre-computed parent end-rune positions.
//
// At query time, Store.Search resolves the parent via a LEFT JOIN and returns
// parent.content (the wide window) rather than child.content (the narrow one),
// giving the LLM sufficient surrounding context without embedding the full
// parent text.
func (s *Server) handleIngestDocument(c *fiber.Ctx) error {
	svc := s.engine.Knowledge()
	if svc == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "knowledge store disabled")
	}
	kb, err := svc.Store.GetKB(knowledgeKBParam(c))
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if kb == nil {
		return s.errMsg(c, fiber.StatusNotFound, "kb not found")
	}

	var (
		title    string
		source   string
		mimeType string
		reader   io.Reader
		size     int64
	)

	ctype := strings.ToLower(c.Get("Content-Type"))
	switch {
	case strings.HasPrefix(ctype, "multipart/form-data"):
		fh, ferr := c.FormFile("file")
		if ferr != nil {
			return s.errMsg(c, fiber.StatusBadRequest, fmt.Sprintf("multipart upload missing 'file': %v", ferr))
		}
		f, oerr := fh.Open()
		if oerr != nil {
			return s.errMsg(c, fiber.StatusBadRequest, oerr.Error())
		}
		defer f.Close()
		size = fh.Size
		if err := s.rejectOversizedKnowledgePayload(c, size); err != nil {
			return err
		}
		reader = f
		title = c.FormValue("title")
		if title == "" {
			title = fh.Filename
		}
		source = fh.Filename
		mimeType = knowledge.MimeFromFilename(fh.Filename)
		if mimeType == "" {
			mimeType = fh.Header.Get("Content-Type")
		}
	default:
		var body struct {
			Title    string `json:"title"`
			Source   string `json:"source"`
			MIMEType string `json:"mime_type"`
			Content  string `json:"content"`
		}
		if perr := c.BodyParser(&body); perr != nil {
			return s.errMsg(c, fiber.StatusBadRequest, perr.Error())
		}
		if body.Content == "" {
			return s.errMsg(c, fiber.StatusBadRequest, "content is required")
		}
		title = body.Title
		source = body.Source
		mimeType = body.MIMEType
		size = int64(len(body.Content))
		if err := s.rejectOversizedKnowledgePayload(c, size); err != nil {
			return err
		}
		reader = bytes.NewReader([]byte(body.Content))
	}

	if title == "" {
		title = "Untitled document"
	}

	// Spool the raw bytes to disk and ENQUEUE. Ingestion (extract → chunk →
	// embed → store) used to run INLINE here, blocking the HTTP request for the
	// entire embed — minutes on a large PDF, the whole file pinned in memory, no
	// progress, and the work lost on a restart or a transient embedder error.
	// Now the request only records the job durably and returns 202 Accepted; the
	// background worker performs the ingestion and reports live progress.
	job, err := s.enqueueIngest(kb.Name, title, source, mimeType, reader, size)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.log.Info("knowledge: ingest queued",
		zap.String("kb", kb.Name),
		zap.String("title", title),
		zap.String("job", job.ID),
		zap.Int64("bytes", size),
	)
	return c.Status(fiber.StatusAccepted).JSON(job)
}

func (s *Server) knowledgeMaxDocumentBytes() int64 {
	if s != nil && s.cfg != nil && s.cfg.Knowledge.MaxDocumentBytes > 0 {
		return s.cfg.Knowledge.MaxDocumentBytes
	}
	return knowledge.DefaultMaxDocumentBytes
}

func (s *Server) rejectOversizedKnowledgePayload(c *fiber.Ctx, size int64) error {
	limit := s.knowledgeMaxDocumentBytes()
	if limit > 0 && size > limit {
		return c.Status(fiber.StatusRequestEntityTooLarge).JSON(fiber.Map{
			"error": fmt.Sprintf("document is %s, over the configured knowledge.max_document_bytes limit of %s", formatBytes(size), formatBytes(limit)),
		})
	}
	return nil
}

// handleDeleteKnowledgeDocument removes a single document and its chunks.
func (s *Server) handleDeleteKnowledgeDocument(c *fiber.Ctx) error {
	svc := s.engine.Knowledge()
	if svc == nil {
		return c.SendStatus(fiber.StatusNoContent)
	}
	kb, err := svc.Store.GetKB(knowledgeKBParam(c))
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if kb == nil {
		return s.errMsg(c, fiber.StatusNotFound, "kb not found")
	}
	if err := svc.Store.DeleteDocument(kb.ID, c.Params("doc")); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// handleSearchKnowledge runs a one-shot search for the GUI's debug box.
func (s *Server) handleSearchKnowledge(c *fiber.Ctx) error {
	svc := s.engine.Knowledge()
	if svc == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "knowledge store disabled")
	}
	var body struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if body.TopK <= 0 {
		body.TopK = 5
	}
	kbName := knowledgeKBParam(c)
	kb, err := svc.Store.GetKB(kbName)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if kb == nil {
		return s.errMsg(c, fiber.StatusNotFound, "kb not found")
	}

	embedder := svc.Embedders.Get(kb.EmbeddingProvider)
	if embedder == nil {
		return s.errMsg(c, fiber.StatusFailedDependency, "embedder not registered")
	}
	vecs, err := embedder.Embed(c.Context(), kb.EmbeddingModel, []string{body.Query})
	if err != nil || len(vecs) == 0 {
		return s.errMsg(c, fiber.StatusBadGateway, fmt.Sprintf("embed query: %v", err))
	}
	hits, err := svc.Store.Search(kb, vecs[0], body.TopK)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{"hits": hits})
}
