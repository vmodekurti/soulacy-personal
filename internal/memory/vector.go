// vector.go — semantic (vector) memory tier for Soulacy.
//
// Stores and retrieves memory entries by embedding similarity using sqlite-vec.
// Operates on the same *sql.DB as the SQLiteArchive so there is no separate
// process or API to manage: one database file, two complementary tables.
//
// Schema:
//
//	memory_vectors      — sqlite-vec virtual table (vec0), one row per entry.
//	memory_vector_meta  — companion table keyed by the same rowid, holding
//	                      agent_id, session_id, content, etc. for join-back
//	                      after a KNN search.
//
// The embedding dimension is fixed at construction time (must match what the
// configured embedder produces). Mixing dimensions across process restarts
// requires dropping and recreating the vec0 table — an operator concern.
//
// Embeddings are generated via the Embedder interface (defined in store.go),
// which is satisfied by *llm.OllamaEmbedder and *llm.OpenAIEmbedder.
package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/workspacepurge"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// VectorStore adds semantic search to the SQLiteArchive.
// Construction fails (non-nil error) if sqlite-vec isn't loaded in the DB
// process — in that case the caller should log a warning and continue without
// semantic memory rather than crashing.
type VectorStore struct {
	db       *sql.DB
	embedder Embedder
	dims     int // embedding dimensions; must match the embedder's output
}

// ExportWorkspaceMetadataJSONL exports rebuild metadata, not embedding
// floats. The source text remains in the memory export and can be re-embedded
// by the destination deployment's configured model.
func (vs *VectorStore) ExportWorkspaceMetadataJSONL(ctx context.Context, workspaceID string, w io.Writer) (int64, error) {
	if err := wsroot.Validate(strings.TrimSpace(workspaceID)); err != nil {
		return 0, err
	}
	rows, err := vs.db.QueryContext(ctx, `
		SELECT rowid, workspace_id, agent_id, session_id, scope, key, provenance, created_at
		FROM memory_vector_meta WHERE workspace_id = ? ORDER BY rowid ASC`, wsroot.Normalize(workspaceID))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	encoder := json.NewEncoder(w)
	var count int64
	for rows.Next() {
		var record struct {
			RowID       int64     `json:"row_id"`
			WorkspaceID string    `json:"workspace_id"`
			AgentID     string    `json:"agent_id"`
			SessionID   string    `json:"session_id"`
			Scope       string    `json:"scope"`
			Key         *string   `json:"key,omitempty"`
			Provenance  *string   `json:"provenance,omitempty"`
			CreatedAt   time.Time `json:"created_at"`
		}
		if err := rows.Scan(&record.RowID, &record.WorkspaceID, &record.AgentID, &record.SessionID, &record.Scope, &record.Key, &record.Provenance, &record.CreatedAt); err != nil {
			return count, err
		}
		if err := encoder.Encode(record); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}

func (vs *VectorStore) PurgeWorkspace(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
	if err := wsroot.Validate(strings.TrimSpace(workspaceID)); err != nil {
		return workspacepurge.Removed{}, err
	}
	tx, err := vs.db.BeginTx(ctx, nil)
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	workspaceID = wsroot.Normalize(workspaceID)
	result, err := tx.ExecContext(ctx, `DELETE FROM memory_vectors WHERE rowid IN (SELECT rowid FROM memory_vector_meta WHERE workspace_id = ?)`, workspaceID)
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	vectorRows, err := result.RowsAffected()
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	result, err = tx.ExecContext(ctx, `DELETE FROM memory_vector_meta WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	metadataRows, err := result.RowsAffected()
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	if err := tx.Commit(); err != nil {
		return workspacepurge.Removed{}, err
	}
	return workspacepurge.Removed{Rows: vectorRows + metadataRows, Note: "vector rows and rebuild metadata"}, nil
}

// NewVectorStore creates the schema (if missing) and returns a VectorStore.
// db must be the same *sql.DB used by the SQLiteArchive.
// dims is the dimensionality produced by the configured embedder
// (e.g. 768 for nomic-embed-text, 1536 for text-embedding-3-small).
func NewVectorStore(db *sql.DB, embedder Embedder, dims int) (*VectorStore, error) {
	vs := &VectorStore{db: db, embedder: embedder, dims: dims}
	if err := vs.ensureSchema(); err != nil {
		return nil, fmt.Errorf("vector memory: schema: %w", err)
	}
	return vs, nil
}

// ensureSchema creates the vec0 virtual table and companion metadata table.
func (vs *VectorStore) ensureSchema() error {
	stmts := []string{
		fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS memory_vectors USING vec0(embedding float[%d])`, vs.dims),
		`CREATE TABLE IF NOT EXISTS memory_vector_meta (
			rowid        INTEGER PRIMARY KEY,
			workspace_id TEXT    NOT NULL DEFAULT 'ws_personal',
			agent_id   TEXT    NOT NULL,
			session_id TEXT    NOT NULL,
			scope      TEXT    NOT NULL,
			content    TEXT    NOT NULL,
			key        TEXT,
			provenance TEXT,
			created_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_mvmeta_agent ON memory_vector_meta(workspace_id, agent_id)`,
	}
	// An index built before tenancy had agent_id as its leading column, which
	// no longer serves a workspace-first lookup. Dropping it lets the CREATE
	// above rebuild it; CREATE INDEX IF NOT EXISTS would otherwise keep the
	// old shape forever.
	if _, err := vs.db.Exec(`DROP INDEX IF EXISTS idx_mvmeta_agent`); err != nil {
		return fmt.Errorf("vector memory: drop legacy index: %w", err)
	}
	for _, s := range stmts {
		if _, err := vs.db.Exec(s); err != nil {
			// vec0 creation fails when sqlite-vec isn't loaded — surface a
			// clear error so callers can disable gracefully.
			if strings.Contains(err.Error(), "no such module") {
				return fmt.Errorf("sqlite-vec extension not loaded (set memory.vector_db to enable): %w", err)
			}
			preview := s
			if len(preview) > 60 {
				preview = preview[:60]
			}
			return fmt.Errorf("exec %q: %w", preview, err)
		}
	}
	// A database created before tenancy has no workspace column. Add it in
	// place and assign its rows to the personal workspace, which is what a
	// single-user installation's memories were. The backfill re-runs on every
	// open: a row with an empty workspace matches no scoped KNN pre-filter, so
	// it would not leak — it would silently stop being recallable.
	if _, err := vs.db.Exec(
		`ALTER TABLE memory_vector_meta ADD COLUMN workspace_id TEXT NOT NULL DEFAULT 'ws_personal'`,
	); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("vector memory: add workspace column: %w", err)
	}
	if _, err := vs.db.Exec(
		`UPDATE memory_vector_meta SET workspace_id = ? WHERE workspace_id IS NULL OR workspace_id = ''`,
		wsroot.PersonalWorkspaceID,
	); err != nil {
		return fmt.Errorf("vector memory: backfill workspace: %w", err)
	}
	return nil
}

// Write embeds the entry's content and inserts a new vector memory row.
func (vs *VectorStore) Write(ctx context.Context, e Entry) error {
	if strings.TrimSpace(e.WorkspaceID) == "" {
		return ErrWorkspaceRequired
	}
	vec, err := vs.embedder.Embed(ctx, e.Content)
	if err != nil {
		return fmt.Errorf("vector memory: embed: %w", err)
	}
	if len(vec) != vs.dims {
		return fmt.Errorf("vector memory: embedder returned %d dims, expected %d", len(vec), vs.dims)
	}

	vecJSON, err := json.Marshal(vec)
	if err != nil {
		return fmt.Errorf("vector memory: marshal vector: %w", err)
	}

	tx, err := vs.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("vector memory: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	res, err := tx.ExecContext(ctx, `
		INSERT INTO memory_vector_meta (workspace_id, agent_id, session_id, scope, content, key, provenance, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		wsroot.Normalize(e.WorkspaceID), e.AgentID, e.SessionID, string(e.Scope), e.Content, e.Key, "", e.CreatedAt, // provenance col kept for schema compat
	)
	if err != nil {
		return fmt.Errorf("vector memory: insert meta: %w", err)
	}
	rowID, _ := res.LastInsertId()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memory_vectors (rowid, embedding) VALUES (?, ?)`,
		rowID, string(vecJSON),
	); err != nil {
		return fmt.Errorf("vector memory: insert vec: %w", err)
	}
	return tx.Commit()
}

// SearchResult is one hit from a semantic memory search.
type SearchResult struct {
	Entry    Entry
	Distance float64 // cosine distance (lower = more similar; 0 = identical)
}

// Search embeds the query and returns the top-K most similar memory entries in
// one workspace, across every agent in it.
func (vs *VectorStore) Search(ctx context.Context, workspaceID, query string, topK int) ([]SearchResult, error) {
	return vs.SearchFiltered(ctx, workspaceID, query, topK, "")
}

// SearchFiltered embeds the query and returns the top-K most similar memory
// entries scoped to the given agent.
//
// Pre-filter vs. post-filter:
//
// The original Search method fetches the top-K globally and then discards rows
// belonging to other agents in Go. That works when agents share the vector
// space roughly equally, but breaks badly when one agent has thousands of
// memories and another has dozens — the KNN scan uses up all K slots on the
// dominant agent, returning zero results for everyone else.
//
// The fix is a SQL-level pre-filter:
//
//	WHERE mv.rowid IN (SELECT rowid FROM memory_vector_meta WHERE agent_id = ?)
//
// sqlite-vec evaluates this constraint inside the KNN loop and only counts
// matching rows against the K budget, so agents with sparser memories still
// get their fair share. There is a marginal overhead for the subquery but it
// is dominated by the embedding I/O, making the trade-off strongly positive.
//
// The workspace goes in that same pre-filter, and for a sharper version of the
// same reason. A post-filter would not merely return another tenant's rows to
// be discarded in Go — it would spend the K budget on them, so a tenant
// sharing a database with a busier one would get few results or none, while
// the busier tenant's memories were read out of the store to decide that.
// Pre-filtering means the neighbour's vectors are never candidates at all.
func (vs *VectorStore) SearchFiltered(ctx context.Context, workspaceID, query string, topK int, agentID string) ([]SearchResult, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, ErrWorkspaceRequired
	}
	workspaceID = wsroot.Normalize(workspaceID)
	if topK <= 0 {
		topK = 5
	}
	if topK > 50 {
		topK = 50
	}
	vec, err := vs.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("vector memory: embed query: %w", err)
	}
	vecJSON, _ := json.Marshal(vec)

	filter := `SELECT rowid FROM memory_vector_meta WHERE workspace_id = ?`
	args := []any{string(vecJSON), topK, workspaceID}
	if agentID != "" {
		filter += ` AND agent_id = ?`
		args = append(args, agentID)
	}
	rows, err := vs.db.QueryContext(ctx, `
			SELECT mv.rowid, mv.distance,
			       m.agent_id, m.session_id, m.scope, m.content, m.key, m.provenance, m.created_at
			FROM memory_vectors mv
			JOIN memory_vector_meta m ON mv.rowid = m.rowid
			WHERE mv.embedding MATCH ?
			  AND k = ?
			  AND mv.rowid IN (`+filter+`)
			ORDER BY mv.distance
		`, args...)
	if err != nil {
		return nil, fmt.Errorf("vector memory: knn search filtered: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var rowID int64
		var dist float64
		var sr SearchResult
		var scope, ignoredProvenance string // provenance col retained in schema, discarded on read
		if err := rows.Scan(
			&rowID, &dist,
			&sr.Entry.AgentID, &sr.Entry.SessionID,
			&scope, &sr.Entry.Content,
			&sr.Entry.Key, &ignoredProvenance, &sr.Entry.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("vector memory: scan: %w", err)
		}
		sr.Entry.Scope = Scope(scope)
		sr.Distance = dist
		results = append(results, sr)
	}
	return results, rows.Err()
}

// Prune removes one workspace's vector memories older than before for an
// agent. It is scoped so a retention sweep in one tenant cannot delete
// another's recall.
func (vs *VectorStore) Prune(ctx context.Context, workspaceID, agentID string, before time.Time) error {
	if strings.TrimSpace(workspaceID) == "" {
		return ErrWorkspaceRequired
	}
	rows, err := vs.db.QueryContext(ctx,
		`SELECT rowid FROM memory_vector_meta WHERE workspace_id = ? AND agent_id = ? AND created_at < ?`,
		wsroot.Normalize(workspaceID), agentID, before,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, id := range ids {
		vs.db.ExecContext(ctx, `DELETE FROM memory_vectors WHERE rowid = ?`, id)     //nolint:errcheck
		vs.db.ExecContext(ctx, `DELETE FROM memory_vector_meta WHERE rowid = ?`, id) //nolint:errcheck
	}
	return nil
}
