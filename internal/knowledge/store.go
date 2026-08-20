// Package knowledge implements RAG storage: knowledge bases, documents,
// chunks, and vector search backed by SQLite + sqlite-vec.
//
// Layout
//
//	knowledge_bases — KB rows (one per named KB)
//	documents       — source files / pasted texts ingested into a KB
//	chunks          — text chunks produced by the chunker
//	vec_<kb_id>     — vec0 virtual table holding the embedding for each chunk
//	                  (one per KB so different KBs can use different dims)
//
// The vec0 extension is loaded automatically via sqlite_vec.Auto() into every
// subsequent connection opened by the mattn/go-sqlite3 driver. No external
// .dylib lookup is needed — the extension is compiled in via cgo.
package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/internal/workspacepurge"
)

// KB describes a single knowledge base.
type KB struct {
	ID string `json:"id"`
	// WorkspaceID is the tenant that owns this knowledge base. It is set by
	// the server from verified request context and is never read from a
	// request body: a client that could name its own namespace could read any
	// tenant's documents.
	WorkspaceID       string    `json:"workspace_id"`
	Name              string    `json:"name"`
	Description       string    `json:"description"`
	EmbeddingProvider string    `json:"embedding_provider"`
	EmbeddingModel    string    `json:"embedding_model"`
	Dim               int       `json:"dim"`
	ChunkSize         int       `json:"chunk_size"`
	ChunkOverlap      int       `json:"chunk_overlap"`
	DocumentCount     int       `json:"document_count"`
	ChunkCount        int       `json:"chunk_count"`
	CreatedAt         time.Time `json:"created_at"`
}

// Document is one ingested source.
type Document struct {
	ID         string    `json:"id"`
	KBID       string    `json:"kb_id"`
	Title      string    `json:"title"`
	Source     string    `json:"source"`
	MIMEType   string    `json:"mime_type"`
	ByteSize   int64     `json:"byte_size"`
	SHA256     string    `json:"sha256"`
	ChunkCount int       `json:"chunk_count"`
	CreatedAt  time.Time `json:"created_at"`
}

// Chunk is one piece of a document, ready to embed or already embedded.
type Chunk struct {
	ID            string    `json:"id"`
	DocID         string    `json:"doc_id"`
	KBID          string    `json:"kb_id"`
	Ordinal       int       `json:"ordinal"`
	Content       string    `json:"content"`
	ParentChunkID string    `json:"parent_chunk_id,omitempty"`
	Vector        []float32 `json:"-"`
}

// SearchHit is one result from a vector search.
type SearchHit struct {
	ChunkID   string  `json:"chunk_id"`
	DocID     string  `json:"doc_id"`
	DocTitle  string  `json:"doc_title"`
	DocSource string  `json:"doc_source"`
	Ordinal   int     `json:"ordinal"`
	Content   string  `json:"content"`
	Distance  float64 `json:"distance"`
}

// Store is the SQLite-backed knowledge store.
//
// Each KB gets its own vec0 virtual table (vec_<kb_id>) so KBs can use
// different embedding dimensions. The FTS5 full-text search table
// (chunks_fts) is shared across all KBs and filtered by kb_id at query time.
//
// hasFTS5 is detected at Open time by attempting to CREATE the FTS5 virtual
// table. When the SQLite build in use was compiled without the fts5 module
// (common in CGO-free test environments), hasFTS5 is false and all FTS5-
// guarded paths are skipped, degrading gracefully to vector-only search.
// ErrWorkspaceRequired is returned when a knowledge operation arrives without
// a workspace. Knowledge with no owner is knowledge every tenant can search.
var ErrWorkspaceRequired = errors.New("knowledge: workspace is required")

type Store struct {
	db      *sql.DB
	mu      sync.RWMutex // guards CREATE/DROP of per-KB vec0 tables
	hasFTS5 bool         // true when FTS5 extension is available in this SQLite build
}

var autoOnce sync.Once

// Open opens (or creates) the knowledge DB at path and ensures the schema is
// present. sqlite-vec is auto-loaded into every new connection on the driver.
func Open(path string) (*Store, error) {
	autoOnce.Do(func() {
		// Registers a sqlite3_auto_extension hook so every subsequent
		// connection opened by the driver loads vec0. Safe to call multiple
		// times in theory but mattn warns about it, so we gate it.
		sqlite_vec.Auto()
	})

	// PRODUCTION_AUDIT → F3 (2026-05-27): WAL + NORMAL synchronous + 30s
	// busy_timeout + 256 MiB mmap (vec0 KNN reads scan rows linearly so
	// memory-mapped I/O helps notably) + foreign-key cascades + tuned pool.
	opts := sqlitex.DefaultOptions()
	opts.ForeignKeys = true
	opts.MMapSize = 256 << 20
	db, err := sqlitex.Open(path, opts)
	if err != nil {
		return nil, fmt.Errorf("knowledge: open sqlite %s: %w", path, err)
	}

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS knowledge_bases (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL DEFAULT 'ws_personal',
			name TEXT NOT NULL,
			description TEXT,
			embedding_provider TEXT NOT NULL,
			embedding_model TEXT NOT NULL,
			dim INTEGER NOT NULL,
			chunk_size INTEGER NOT NULL,
			chunk_overlap INTEGER NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		// A knowledge-base name is human-chosen, so two teams both calling one
		// "docs" is expected. Uniqueness is therefore composite; a global
		// UNIQUE(name) would have made one tenant's create fail because
		// another tenant already used the word.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_kb_workspace_name ON knowledge_bases(workspace_id, name)`,
		`CREATE TABLE IF NOT EXISTS documents (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL DEFAULT 'ws_personal',
			kb_id TEXT NOT NULL,
			title TEXT NOT NULL,
			source TEXT,
			mime_type TEXT,
			byte_size INTEGER,
			sha256 TEXT,
			created_at DATETIME NOT NULL,
			FOREIGN KEY(kb_id) REFERENCES knowledge_bases(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS chunks (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL DEFAULT 'ws_personal',
			doc_id TEXT NOT NULL,
			kb_id TEXT NOT NULL,
			ordinal INTEGER NOT NULL,
			content TEXT NOT NULL,
			parent_chunk_id TEXT DEFAULT NULL,
			FOREIGN KEY(doc_id) REFERENCES documents(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_documents_kb ON documents(workspace_id, kb_id)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_doc ON chunks(workspace_id, doc_id)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_kb ON chunks(workspace_id, kb_id)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return nil, fmt.Errorf("knowledge: schema (%q): %w", s[:minInt(len(s), 60)], err)
		}
	}

	// Durable ingestion job catalog. Idempotent (CREATE TABLE IF NOT EXISTS), so
	// an existing knowledge.db gains the table in place on first open.
	if err := initJobSchema(db); err != nil {
		return nil, err
	}

	// Schema versioning (E22 adoption): v1 = the bootstrap above INCLUDING
	// the legacy parent_chunk_id backfill below (pre-versioning, stays
	// best-effort); future changes go through sqlitex.MigrateSchema with v2+.
	// An existing knowledge.db has a global UNIQUE(name) and no tenant column.
	// SQLite cannot drop a constraint in place, so the table is rebuilt.
	if err := migrateKnowledgeWorkspaceSchema(db); err != nil {
		return nil, err
	}

	if err := sqlitex.RecordSchemaVersion(db, "knowledge", 2); err != nil {
		return nil, fmt.Errorf("knowledge: schema version: %w", err)
	}

	// Migration: add parent_chunk_id to existing DBs (ignore error if column
	// already exists or was included in CREATE TABLE above).
	db.Exec(`ALTER TABLE chunks ADD COLUMN parent_chunk_id TEXT DEFAULT NULL`) //nolint:errcheck

	// FTS5 hybrid search table — best-effort. FTS5 may not be compiled into
	// the SQLite driver in all environments (e.g. test builds without CGO tags).
	// If creation fails, hybrid search degrades gracefully to vector-only.
	_, ftsErr := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(chunk_id UNINDEXED, kb_id UNINDEXED, content, tokenize="porter unicode61")`)
	hasFTS5 := ftsErr == nil

	// Confirm vec0 is loaded so we fail fast instead of at first KB create.
	var vecVer string
	if err := db.QueryRow(`SELECT vec_version()`).Scan(&vecVer); err != nil {
		return nil, fmt.Errorf("knowledge: sqlite-vec not loaded (vec_version() failed): %w", err)
	}

	return &Store{db: db, hasFTS5: hasFTS5}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// vecTable returns the per-KB virtual table name (safe — KB IDs are uuids
// with dashes replaced by underscores).
func vecTable(kbID string) string {
	return `"vec_` + strings.ReplaceAll(kbID, "-", "_") + `"`
}

// CreateKB inserts a new KB row and creates its vec0 virtual table.
// Name uniqueness is enforced by the UNIQUE index on knowledge_bases.name.
func (s *Store) CreateKB(kb KB) (*KB, error) {
	if kb.Name == "" {
		return nil, errors.New("knowledge: name is required")
	}
	// The workspace comes from verified request context. Refusing an empty one
	// here means a knowledge base can never exist without an owner, which is
	// the only state in which every tenant could read it.
	kb.WorkspaceID = strings.TrimSpace(kb.WorkspaceID)
	if kb.WorkspaceID == "" {
		return nil, ErrWorkspaceRequired
	}
	if kb.Dim <= 0 {
		return nil, errors.New("knowledge: dim must be positive")
	}
	if kb.ID == "" {
		kb.ID = uuid.New().String()
	}
	if kb.ChunkSize <= 0 {
		kb.ChunkSize = 1000
	}
	if kb.ChunkOverlap < 0 {
		kb.ChunkOverlap = 200
	}
	if kb.CreatedAt.IsZero() {
		kb.CreatedAt = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.Exec(`INSERT INTO knowledge_bases
		(id, workspace_id, name, description, embedding_provider, embedding_model, dim, chunk_size, chunk_overlap, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		kb.ID, kb.WorkspaceID, kb.Name, kb.Description, kb.EmbeddingProvider, kb.EmbeddingModel,
		kb.Dim, kb.ChunkSize, kb.ChunkOverlap, kb.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("knowledge: insert kb: %w", err)
	}

	// vec0 virtual table — one per KB, dim fixed.
	createVec := fmt.Sprintf(
		`CREATE VIRTUAL TABLE IF NOT EXISTS %s USING vec0(chunk_id TEXT PRIMARY KEY, embedding FLOAT[%d])`,
		vecTable(kb.ID), kb.Dim,
	)
	if _, err := tx.Exec(createVec); err != nil {
		return nil, fmt.Errorf("knowledge: create vec0 table: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &kb, nil
}

// ListKBs returns all knowledge bases with up-to-date doc/chunk counts.
func (s *Store) ListKBs(workspaceID string) ([]KB, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, ErrWorkspaceRequired
	}
	rows, err := s.db.Query(`
		SELECT kb.id, kb.workspace_id, kb.name, COALESCE(kb.description,''), kb.embedding_provider, kb.embedding_model,
		       kb.dim, kb.chunk_size, kb.chunk_overlap, kb.created_at,
		       (SELECT COUNT(*) FROM documents d WHERE d.kb_id = kb.id) AS doc_count,
		       (SELECT COUNT(*) FROM chunks c WHERE c.kb_id = kb.id) AS chunk_count
		FROM knowledge_bases kb
		WHERE kb.workspace_id = ?
		ORDER BY kb.name`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []KB
	for rows.Next() {
		var kb KB
		if err := rows.Scan(
			&kb.ID, &kb.WorkspaceID, &kb.Name, &kb.Description, &kb.EmbeddingProvider, &kb.EmbeddingModel,
			&kb.Dim, &kb.ChunkSize, &kb.ChunkOverlap, &kb.CreatedAt,
			&kb.DocumentCount, &kb.ChunkCount,
		); err != nil {
			return nil, err
		}
		out = append(out, kb)
	}
	return out, rows.Err()
}

// GetKB looks up a KB by name (case-sensitive). Returns nil if not found.
func (s *Store) GetKB(workspaceID, name string) (*KB, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, ErrWorkspaceRequired
	}
	var kb KB
	err := s.db.QueryRow(`
		SELECT kb.id, kb.workspace_id, kb.name, COALESCE(kb.description,''), kb.embedding_provider, kb.embedding_model,
		       kb.dim, kb.chunk_size, kb.chunk_overlap, kb.created_at,
		       (SELECT COUNT(*) FROM documents d WHERE d.kb_id = kb.id),
		       (SELECT COUNT(*) FROM chunks c WHERE c.kb_id = kb.id)
		FROM knowledge_bases kb WHERE kb.workspace_id = ? AND kb.name = ?`, workspaceID, name).Scan(
		&kb.ID, &kb.WorkspaceID, &kb.Name, &kb.Description, &kb.EmbeddingProvider, &kb.EmbeddingModel,
		&kb.Dim, &kb.ChunkSize, &kb.ChunkOverlap, &kb.CreatedAt,
		&kb.DocumentCount, &kb.ChunkCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &kb, nil
}

// DeleteKB drops the KB row, all docs/chunks (via cascade), and the vec0 table.
func (s *Store) DeleteKB(workspaceID, name string) error {
	kb, err := s.GetKB(workspaceID, name)
	if err != nil {
		return err
	}
	if kb == nil {
		return nil // idempotent
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`DELETE FROM knowledge_bases WHERE id = ?`, kb.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE IF EXISTS ` + vecTable(kb.ID)); err != nil {
		return err
	}
	return tx.Commit()
}

// AddDocument writes a document row and its chunks (with embeddings) atomically.
// Chunks with a non-empty Vector must have len(Vector) == kb.Dim.
//
// Parent-child chunking:
//
// When the gateway ingests a document it produces two chunk tiers:
//   - Parent chunks (large context windows, ~4× chunk_size): stored in the
//     chunks table only — they carry no embedding and are never inserted into
//     the vec0 or FTS5 tables. They exist purely to provide a wider excerpt to
//     the LLM after a child chunk retrieval hit.
//   - Child chunks (small retrieval windows, chunk_size): embedded and inserted
//     into both the vec0 KNN table and the FTS5 full-text table. Each child
//     carries a parent_chunk_id FK that the Search query resolves to return the
//     parent's broader context instead of the narrow child text.
//
// Chunks with len(Vector) == 0 are skipped for vec/FTS inserts — this is the
// sentinel that marks parent-tier chunks.
func (s *Store) AddDocument(kb *KB, doc Document, chunks []Chunk) (*Document, error) {
	if kb == nil {
		return nil, errors.New("knowledge: kb is nil")
	}
	// Ownership is inherited from the knowledge base, which was resolved
	// through a workspace-scoped lookup. It is never taken from the document,
	// so an ingestion payload cannot file itself under another tenant.
	if strings.TrimSpace(kb.WorkspaceID) == "" {
		return nil, ErrWorkspaceRequired
	}
	if doc.ID == "" {
		doc.ID = uuid.New().String()
	}
	doc.KBID = kb.ID
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = time.Now().UTC()
	}
	if doc.SHA256 == "" && len(chunks) > 0 {
		h := sha256.New()
		for _, c := range chunks {
			h.Write([]byte(c.Content))
		}
		doc.SHA256 = hex.EncodeToString(h.Sum(nil))
	}

	// Validate dimensions only for chunks that carry a vector.
	for i, c := range chunks {
		if len(c.Vector) > 0 && len(c.Vector) != kb.Dim {
			return nil, fmt.Errorf("knowledge: chunk %d vector dim %d != kb dim %d", i, len(c.Vector), kb.Dim)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`INSERT INTO documents
		(id, workspace_id, kb_id, title, source, mime_type, byte_size, sha256, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		doc.ID, kb.WorkspaceID, doc.KBID, doc.Title, doc.Source, doc.MIMEType, doc.ByteSize, doc.SHA256, doc.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("knowledge: insert document: %w", err)
	}

	chunkStmt, err := tx.Prepare(`INSERT INTO chunks (id, workspace_id, doc_id, kb_id, ordinal, content, parent_chunk_id) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	defer chunkStmt.Close()

	vecStmt, err := tx.Prepare(fmt.Sprintf(`INSERT INTO %s (chunk_id, embedding) VALUES (?, ?)`, vecTable(kb.ID)))
	if err != nil {
		return nil, fmt.Errorf("knowledge: prepare vec insert: %w", err)
	}
	defer vecStmt.Close()

	// FTS5 insert stmt — only prepared when FTS5 is available.
	var ftsStmt *sql.Stmt
	if s.hasFTS5 {
		ftsStmt, err = tx.Prepare(`INSERT INTO chunks_fts (chunk_id, kb_id, content) VALUES (?, ?, ?)`)
		if err != nil {
			return nil, fmt.Errorf("knowledge: prepare fts insert: %w", err)
		}
		defer ftsStmt.Close()
	}

	for i, c := range chunks {
		if c.ID == "" {
			c.ID = uuid.New().String()
		}
		c.DocID = doc.ID
		c.KBID = kb.ID
		c.Ordinal = i
		if _, err := chunkStmt.Exec(c.ID, kb.WorkspaceID, c.DocID, c.KBID, c.Ordinal, c.Content, nullableString(c.ParentChunkID)); err != nil {
			return nil, fmt.Errorf("knowledge: insert chunk: %w", err)
		}
		if len(c.Vector) > 0 {
			blob, err := sqlite_vec.SerializeFloat32(c.Vector)
			if err != nil {
				return nil, fmt.Errorf("knowledge: serialize embedding: %w", err)
			}
			if _, err := vecStmt.Exec(c.ID, blob); err != nil {
				return nil, fmt.Errorf("knowledge: insert vec: %w", err)
			}
			if ftsStmt != nil {
				if _, err := ftsStmt.Exec(c.ID, c.KBID, c.Content); err != nil {
					return nil, fmt.Errorf("knowledge: insert fts: %w", err)
				}
			}
		}
	}

	doc.ChunkCount = len(chunks)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &doc, nil
}

// nullableString converts an empty string to nil so it is stored as SQL NULL
// rather than an empty string. Used for the parent_chunk_id FK column, which
// must be NULL for top-level and parent chunks so joins via COALESCE work
// correctly in the Search query.
func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// ListDocuments returns documents in a KB, newest first.
func (s *Store) ListDocuments(workspaceID, kbID string) ([]Document, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, ErrWorkspaceRequired
	}
	rows, err := s.db.Query(`
		SELECT d.id, d.kb_id, d.title, COALESCE(d.source,''), COALESCE(d.mime_type,''),
		       COALESCE(d.byte_size,0), COALESCE(d.sha256,''), d.created_at,
		       (SELECT COUNT(*) FROM chunks c WHERE c.doc_id = d.id) AS chunk_count
		FROM documents d WHERE d.workspace_id = ? AND d.kb_id = ? ORDER BY d.created_at DESC`, workspaceID, kbID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.KBID, &d.Title, &d.Source, &d.MIMEType,
			&d.ByteSize, &d.SHA256, &d.CreatedAt, &d.ChunkCount); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ExportWorkspaceJSONL streams every reconstructable knowledge record for one
// workspace. Embedding vectors are deliberately excluded: the ownership
// catalog classifies them as regenerable metadata, while source chunks are the
// durable content needed to rebuild them.
func (s *Store) ExportWorkspaceJSONL(ctx context.Context, workspaceID string, w io.Writer) (int64, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return 0, ErrWorkspaceRequired
	}
	encoder := json.NewEncoder(w)
	var count int64
	emit := func(kind string, value any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		return encoder.Encode(struct {
			Kind  string `json:"kind"`
			Value any    `json:"value"`
		}{Kind: kind, Value: value})
	}

	kbs, err := s.ListKBs(workspaceID)
	if err != nil {
		return count, err
	}
	for _, kb := range kbs {
		if err := emit("knowledge_base", kb); err != nil {
			return count, err
		}
		documents, err := s.ListDocuments(workspaceID, kb.ID)
		if err != nil {
			return count, err
		}
		for _, document := range documents {
			if err := emit("document", document); err != nil {
				return count, err
			}
		}
		rows, err := s.db.QueryContext(ctx, `SELECT id, doc_id, kb_id, ordinal, content,
			COALESCE(parent_chunk_id, '') FROM chunks WHERE workspace_id = ? AND kb_id = ? ORDER BY doc_id, ordinal, id`,
			workspaceID, kb.ID)
		if err != nil {
			return count, err
		}
		for rows.Next() {
			var chunk Chunk
			if err := rows.Scan(&chunk.ID, &chunk.DocID, &chunk.KBID, &chunk.Ordinal, &chunk.Content, &chunk.ParentChunkID); err != nil {
				rows.Close()
				return count, err
			}
			if err := emit("chunk", chunk); err != nil {
				rows.Close()
				return count, err
			}
		}
		if err := rows.Close(); err != nil {
			return count, err
		}
	}

	jobs, err := s.listWorkspaceIngests(workspaceID)
	if err != nil {
		return count, err
	}
	for _, job := range jobs {
		if err := emit("ingest_job", job); err != nil {
			return count, err
		}
	}
	return count, nil
}

// ExportVectorMetadataJSONL records enough lineage to verify and rebuild the
// workspace's embeddings without exporting the large regenerable float arrays.
func (s *Store) ExportVectorMetadataJSONL(ctx context.Context, workspaceID string, w io.Writer) (int64, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return 0, ErrWorkspaceRequired
	}
	kbs, err := s.ListKBs(workspaceID)
	if err != nil {
		return 0, err
	}
	encoder := json.NewEncoder(w)
	var count int64
	for _, kb := range kbs {
		rows, err := s.db.QueryContext(ctx, `SELECT chunk_id FROM `+vecTable(kb.ID)+` ORDER BY chunk_id`)
		if err != nil {
			return count, err
		}
		for rows.Next() {
			var chunkID string
			if err := rows.Scan(&chunkID); err != nil {
				rows.Close()
				return count, err
			}
			if err := encoder.Encode(struct {
				KBID       string `json:"kb_id"`
				KBName     string `json:"kb_name"`
				Provider   string `json:"embedding_provider"`
				Model      string `json:"embedding_model"`
				Dimensions int    `json:"dimensions"`
				ChunkID    string `json:"chunk_id"`
			}{kb.ID, kb.Name, kb.EmbeddingProvider, kb.EmbeddingModel, kb.Dim, chunkID}); err != nil {
				rows.Close()
				return count, err
			}
			count++
		}
		if err := rows.Close(); err != nil {
			return count, err
		}
	}
	return count, nil
}

func (s *Store) listWorkspaceIngests(workspaceID string) ([]IngestJob, error) {
	rows, err := s.db.Query(`SELECT id, workspace_id, kb_name, title, source, mime_type, spool_path, byte_size,
		status, attempt, progress, COALESCE(error,''), COALESCE(doc_id,''), created_at, started_at, ended_at
		FROM ingest_jobs WHERE workspace_id = ? ORDER BY created_at DESC, id DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []IngestJob
	for rows.Next() {
		job, err := scanIngest(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// PurgeWorkspace removes a workspace's dynamic vector/FTS state, relational
// records, ingest jobs, and any still-spooled uploads. Every spool path is
// proven inside spoolRoot before the first mutation; a corrupted database row
// can therefore stop deletion, but cannot turn deletion into an arbitrary
// filesystem remove.
func (s *Store) PurgeWorkspace(ctx context.Context, workspaceID, spoolRoot string) (workspacepurge.Removed, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return workspacepurge.Removed{}, ErrWorkspaceRequired
	}
	jobs, err := s.listWorkspaceIngests(workspaceID)
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	root, err := filepath.EvalSymlinks(spoolRoot)
	if err != nil {
		return workspacepurge.Removed{}, fmt.Errorf("knowledge: resolve ingest spool root: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	paths := make([]string, 0, len(jobs))
	for _, job := range jobs {
		path, err := confinedSpoolPath(root, job.SpoolPath)
		if err != nil {
			return workspacepurge.Removed{}, err
		}
		paths = append(paths, path)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM knowledge_bases WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	var kbIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return workspacepurge.Removed{}, err
		}
		kbIDs = append(kbIDs, id)
	}
	if err := rows.Close(); err != nil {
		return workspacepurge.Removed{}, err
	}

	var removedBytes int64
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil {
			removedBytes += info.Size()
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return workspacepurge.Removed{}, fmt.Errorf("knowledge: remove spooled upload: %w", err)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if s.hasFTS5 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks_fts WHERE chunk_id IN
			(SELECT id FROM chunks WHERE workspace_id = ?)`, workspaceID); err != nil {
			return workspacepurge.Removed{}, err
		}
	}
	for _, id := range kbIDs {
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+vecTable(id)); err != nil {
			return workspacepurge.Removed{}, err
		}
	}
	var removedRows int64
	for _, statement := range []string{
		`DELETE FROM knowledge_bases WHERE workspace_id = ?`,
		`DELETE FROM ingest_jobs WHERE workspace_id = ?`,
	} {
		result, err := tx.ExecContext(ctx, statement, workspaceID)
		if err != nil {
			return workspacepurge.Removed{}, err
		}
		n, _ := result.RowsAffected()
		removedRows += n
	}
	if err := tx.Commit(); err != nil {
		return workspacepurge.Removed{}, err
	}
	return workspacepurge.Removed{Rows: removedRows, Bytes: removedBytes, Note: "knowledge bases, documents, chunks, vectors, ingest jobs, and spool files"}, nil
}

func confinedSpoolPath(root, candidate string) (string, error) {
	candidate, err := filepath.Abs(strings.TrimSpace(candidate))
	if err != nil {
		return "", err
	}
	// Resolve the parent even when the file no longer exists. This catches a
	// symlink planted between the spool directory and a named upload.
	parent, err := filepath.EvalSymlinks(filepath.Dir(candidate))
	if err != nil {
		return "", fmt.Errorf("knowledge: resolve spool parent: %w", err)
	}
	candidate = filepath.Join(parent, filepath.Base(candidate))
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("knowledge: refusing spool path outside ingest root: %q", candidate)
	}
	return candidate, nil
}

// DeleteDocument removes a document, cascading chunks, and clears the matching
// rows from the KB's vec0 table.
func (s *Store) DeleteDocument(workspaceID, kbID, docID string) error {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return ErrWorkspaceRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	// FTS and vec0 tables don't honour FK cascade, so wipe by id list first.
	if s.hasFTS5 {
		if _, err := tx.Exec(`DELETE FROM chunks_fts WHERE chunk_id IN (SELECT id FROM chunks WHERE workspace_id = ? AND doc_id = ?)`, workspaceID, docID); err != nil {
			return fmt.Errorf("knowledge: delete fts rows: %w", err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(
		`DELETE FROM %s WHERE chunk_id IN (SELECT id FROM chunks WHERE workspace_id = ? AND doc_id = ?)`,
		vecTable(kbID),
	), workspaceID, docID); err != nil {
		return fmt.Errorf("knowledge: delete vec rows: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM documents WHERE workspace_id = ? AND id = ? AND kb_id = ?`, workspaceID, docID, kbID); err != nil {
		return err
	}
	return tx.Commit()
}

// Search runs a vec0 KNN query against the KB's chunk table. `vector` must
// match the KB's dim. Returns up to topK hits ordered by ascending distance.
func (s *Store) Search(kb *KB, vector []float32, topK int) ([]SearchHit, error) {
	if kb == nil {
		return nil, errors.New("knowledge: kb is nil")
	}
	if len(vector) != kb.Dim {
		return nil, fmt.Errorf("knowledge: query vector dim %d != kb dim %d", len(vector), kb.Dim)
	}
	if topK <= 0 {
		topK = 5
	}

	blob, err := sqlite_vec.SerializeFloat32(vector)
	if err != nil {
		return nil, err
	}

	query := fmt.Sprintf(`
		SELECT v.chunk_id, v.distance,
		       COALESCE(parent.doc_id, child.doc_id),
		       COALESCE(parent.ordinal, child.ordinal),
		       COALESCE(parent.content, child.content),
		       d.title, COALESCE(d.source,'')
		FROM %s v
		JOIN chunks child ON child.id = v.chunk_id
		LEFT JOIN chunks parent ON parent.id = child.parent_chunk_id
		JOIN documents d ON d.id = COALESCE(parent.doc_id, child.doc_id)
		WHERE v.embedding MATCH ?
		  AND v.k = ?
		ORDER BY v.distance`, vecTable(kb.ID))

	rows, err := s.db.Query(query, blob, topK)
	if err != nil {
		return nil, fmt.Errorf("knowledge: vec search: %w", err)
	}
	defer rows.Close()

	var hits []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.ChunkID, &h.Distance, &h.DocID, &h.Ordinal, &h.Content, &h.DocTitle, &h.DocSource); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// SearchFTS runs a full-text search using the FTS5 virtual table.
//
// Results are ranked by BM25 (SQLite's built-in bm25() function). BM25 values
// are negative floats — more negative means a stronger match — so the query
// uses ORDER BY rank (ascending) to put the best hits first.
//
// When hasFTS5 is false (FTS5 not compiled into this SQLite build), the method
// returns (nil, nil) so callers can treat it as an empty result set and fall
// back to vector-only search without any error handling.
//
// The caller is expected to sanitize the query string before passing it here;
// SearchHybrid does this by removing FTS5 special characters.
func (s *Store) SearchFTS(kb *KB, query string, topK int) ([]SearchHit, error) {
	if !s.hasFTS5 {
		return nil, nil // FTS5 not available — caller falls back to vector-only
	}
	if topK <= 0 {
		topK = 5
	}
	rows, err := s.db.Query(`
		SELECT f.chunk_id, bm25(chunks_fts) as rank,
		       c.doc_id, c.ordinal, c.content, d.title, COALESCE(d.source,'')
		FROM chunks_fts f
		JOIN chunks c ON c.id = f.chunk_id
		JOIN documents d ON d.id = c.doc_id
		WHERE chunks_fts MATCH ?
		  AND f.kb_id = ?
		ORDER BY rank
		LIMIT ?`,
		query, kb.ID, topK)
	if err != nil {
		return nil, fmt.Errorf("knowledge: fts search: %w", err)
	}
	defer rows.Close()
	var hits []SearchHit
	for rows.Next() {
		var h SearchHit
		var rank float64
		if err := rows.Scan(&h.ChunkID, &rank, &h.DocID, &h.Ordinal, &h.Content, &h.DocTitle, &h.DocSource); err != nil {
			return nil, err
		}
		h.Distance = rank
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// SearchHybrid combines vector KNN search and FTS5 full-text search via
// Reciprocal Rank Fusion (RRF), returning up to topK results.
//
// Algorithm:
//
//  1. Run vec KNN with fetchK = max(topK*2, 10) candidates.
//
//  2. Run FTS5 BM25 with the same fetchK limit (FTS query sanitized of special
//     chars to avoid parse errors).
//
//  3. For each unique chunk across both result sets, compute the RRF score:
//
//     score(d) = Σ  1 / (k + rank_i(d))
//     i ∈ {vec, fts}
//
//     where k = 60 (the standard RRF constant from Cormack et al., 2009).
//     k = 60 was chosen because it reduces sensitivity to outlier ranks in
//     the tail; lower values make the top-1 rank dominate excessively.
//
//  4. Sort all candidates by descending RRF score and return the top topK.
//
// FTS failure is non-fatal: if FTS5 is unavailable or returns an error, the
// method returns the vector-only results rather than an error.
func (s *Store) SearchHybrid(kb *KB, vector []float32, query string, topK int) ([]SearchHit, error) {
	fetchK := topK * 2
	if fetchK < 10 {
		fetchK = 10
	}

	vecHits, vecErr := s.Search(kb, vector, fetchK)
	// FTS query: sanitize by removing special FTS5 chars that would cause parse errors.
	safeQuery := strings.NewReplacer(`"`, `""`, `*`, ``, `:`, ``, `(`, ``, `)`, ``).Replace(query)
	ftsHits, _ := s.SearchFTS(kb, safeQuery, fetchK) // FTS failure is non-fatal

	if vecErr != nil && len(ftsHits) == 0 {
		return nil, vecErr
	}

	type rrfEntry struct {
		hit   SearchHit
		score float64
	}
	scores := make(map[string]*rrfEntry, len(vecHits)+len(ftsHits))
	for rank, h := range vecHits {
		scores[h.ChunkID] = &rrfEntry{hit: h, score: 1.0 / float64(60+rank+1)}
	}
	for rank, h := range ftsHits {
		if e, ok := scores[h.ChunkID]; ok {
			e.score += 1.0 / float64(60+rank+1)
		} else {
			scores[h.ChunkID] = &rrfEntry{hit: h, score: 1.0 / float64(60+rank+1)}
		}
	}

	out := make([]SearchHit, 0, len(scores))
	for _, e := range scores {
		out = append(out, e.hit)
	}
	sort.Slice(out, func(i, j int) bool {
		return scores[out[i].ChunkID].score > scores[out[j].ChunkID].score
	})
	if len(out) > topK {
		out = out[:topK]
	}
	return out, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// migrateKnowledgeWorkspaceSchema brings a knowledge.db created before tenants
// existed up to the current shape.
//
// The hard part is the old `name TEXT NOT NULL UNIQUE` on knowledge_bases:
// SQLite cannot drop a column constraint in place, so the table is rebuilt and
// its rows copied. Everything runs in one transaction — a half-migrated
// knowledge base would be worse than an unmigrated one, because its documents
// would still be reachable while its access rule was not.
//
// Existing rows are assigned to the personal workspace, which is exactly what
// they were: a single-user installation's knowledge.
func migrateKnowledgeWorkspaceSchema(db *sql.DB) error {
	hasWorkspace, err := columnExists(db, "knowledge_bases", "workspace_id")
	if err != nil {
		return err
	}
	if hasWorkspace {
		// Later opens still backfill: a row with an empty workspace matches no
		// scoped query, so it would be knowledge that silently vanished.
		return backfillKnowledgeWorkspaces(db)
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	stmts := []string{
		`ALTER TABLE knowledge_bases RENAME TO knowledge_bases_legacy`,
		`CREATE TABLE knowledge_bases (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL DEFAULT 'ws_personal',
			name TEXT NOT NULL,
			description TEXT,
			embedding_provider TEXT NOT NULL,
			embedding_model TEXT NOT NULL,
			dim INTEGER NOT NULL,
			chunk_size INTEGER NOT NULL,
			chunk_overlap INTEGER NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`INSERT INTO knowledge_bases
			(id, workspace_id, name, description, embedding_provider, embedding_model, dim, chunk_size, chunk_overlap, created_at)
		 SELECT id, 'ws_personal', name, description, embedding_provider, embedding_model, dim, chunk_size, chunk_overlap, created_at
		   FROM knowledge_bases_legacy`,
		`DROP TABLE knowledge_bases_legacy`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_kb_workspace_name ON knowledge_bases(workspace_id, name)`,
		`ALTER TABLE documents ADD COLUMN workspace_id TEXT NOT NULL DEFAULT 'ws_personal'`,
		`ALTER TABLE chunks ADD COLUMN workspace_id TEXT NOT NULL DEFAULT 'ws_personal'`,
	}
	for _, statement := range stmts {
		if _, err := tx.Exec(statement); err != nil {
			lowered := strings.ToLower(err.Error())
			// A concurrent opener may have won the race; duplicate column and
			// duplicate table are both "already done".
			if strings.Contains(lowered, "duplicate column") || strings.Contains(lowered, "already exists") {
				continue
			}
			return fmt.Errorf("knowledge: workspace migration (%.60s): %w", statement, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return backfillKnowledgeWorkspaces(db)
}

func backfillKnowledgeWorkspaces(db *sql.DB) error {
	for _, table := range []string{"knowledge_bases", "documents", "chunks"} {
		if _, err := db.Exec(`UPDATE ` + table + ` SET workspace_id='ws_personal' WHERE workspace_id IS NULL OR workspace_id=''`); err != nil {
			return fmt.Errorf("knowledge: backfill %s: %w", table, err)
		}
	}
	return nil
}

func columnExists(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, fmt.Errorf("knowledge: inspect %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid              int
			name, columnType string
			notNull, pk      int
			defaultValue     sql.NullString
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
