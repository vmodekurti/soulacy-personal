// facts_sqlite.go — SQLite-backed store for adaptive memory facts.
//
// Embeddings are stored alongside each fact and compared in Go. A user's fact
// set is small (hundreds, not millions), so an exact scan scoped by
// (workspace, owner, agent) is faster than a KNN index round-trip and, more
// importantly, works on every SQLite build Soulacy ships on — no FTS5 or
// vec0 extension is required for adaptive memory to function.
package memory

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/soulacy/soulacy/internal/sqlitex"
)

// FactSQLite is the durable fact store used by the local adaptive engine.
type FactSQLite struct {
	db *sql.DB
}

// OpenFactSQLite opens (or creates) the fact database at path. The file is
// created 0600 because it holds personal facts.
func OpenFactSQLite(path string) (*FactSQLite, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return nil, fmt.Errorf("adaptive memory: path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	_ = f.Chmod(0600)
	if err := f.Close(); err != nil {
		return nil, err
	}
	opts := sqlitex.DefaultOptions()
	opts.MaxOpenConns, opts.MaxIdleConns = 1, 1
	dsn := sqlitex.DSN((&url.URL{Scheme: "file", Path: path}).String(), opts) + "&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := sqlitex.MigrateSchema(db, "adaptive_memory", []sqlitex.SchemaMigration{{Version: 1, SQL: `
CREATE TABLE adaptive_facts(
  id TEXT PRIMARY KEY,
  workspace TEXT NOT NULL,
  owner TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  category TEXT NOT NULL,
  content TEXT NOT NULL,
  confidence REAL NOT NULL,
  status TEXT NOT NULL,
  superseded_by TEXT,
  supersedes TEXT,
  source TEXT NOT NULL,
  source_session_id TEXT,
  source_run_id TEXT,
  embedding BLOB,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX adaptive_facts_scope ON adaptive_facts(workspace, owner, agent_id, status);
`}}); err != nil {
		db.Close()
		return nil, err
	}
	return &FactSQLite{db: db}, nil
}

// Close releases the database.
func (s *FactSQLite) Close() error { return s.db.Close() }

const factColumns = `id, workspace, owner, agent_id, category, content, confidence, status,
	COALESCE(superseded_by,''), COALESCE(supersedes,''), source,
	COALESCE(source_session_id,''), COALESCE(source_run_id,''), embedding, created_at, updated_at`

func scanFact(sc interface{ Scan(...any) error }) (Fact, error) {
	var f Fact
	var emb []byte
	var created, updated int64
	if err := sc.Scan(&f.ID, &f.Workspace, &f.Owner, &f.AgentID, &f.Category, &f.Content, &f.Confidence, &f.Status,
		&f.SupersededBy, &f.Supersedes, &f.Source, &f.SourceSessionID, &f.SourceRunID, &emb, &created, &updated); err != nil {
		return Fact{}, err
	}
	f.Embedding = decodeEmbedding(emb)
	f.CreatedAt = time.Unix(created, 0).UTC()
	f.UpdatedAt = time.Unix(updated, 0).UTC()
	return f, nil
}

func encodeEmbedding(v []float32) []byte {
	if len(v) == 0 {
		return nil
	}
	out := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(x))
	}
	return out
}

func decodeEmbedding(b []byte) []float32 {
	if len(b) < 4 {
		return nil
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

// Insert stores a new fact. Missing ID/timestamps/status are filled in.
func (s *FactSQLite) Insert(ctx context.Context, f Fact) (Fact, error) {
	f, err := prepareFact(f)
	if err != nil {
		return Fact{}, err
	}
	if err := s.insertTx(ctx, s.db, f); err != nil {
		return Fact{}, err
	}
	return f, nil
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *FactSQLite) insertTx(ctx context.Context, x execer, f Fact) error {
	_, err := x.ExecContext(ctx, `INSERT INTO adaptive_facts
		(id, workspace, owner, agent_id, category, content, confidence, status, superseded_by, supersedes, source, source_session_id, source_run_id, embedding, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.ID, f.Workspace, f.Owner, f.AgentID, string(f.Category), f.Content, f.Confidence, f.Status,
		nullIfEmpty(f.SupersededBy), nullIfEmpty(f.Supersedes), f.Source, nullIfEmpty(f.SourceSessionID), nullIfEmpty(f.SourceRunID),
		encodeEmbedding(f.Embedding), f.CreatedAt.Unix(), f.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("adaptive memory: insert: %w", err)
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func prepareFact(f Fact) (Fact, error) {
	scope := FactScope{Workspace: f.Workspace, Owner: f.Owner, AgentID: f.AgentID}.Normalize()
	if scope.Owner == "" {
		return Fact{}, ErrInvalidScope
	}
	f.Workspace, f.Owner, f.AgentID = scope.Workspace, scope.Owner, scope.AgentID
	content, err := NormalizeFactContent(f.Content)
	if err != nil {
		return Fact{}, err
	}
	f.Content = content
	if !ValidFactCategory(string(f.Category)) {
		return Fact{}, ErrInvalidFact
	}
	f.Category = FactCategory(strings.ToLower(string(f.Category)))
	if f.ID == "" {
		f.ID = uuid.NewString()
	}
	if f.Status == "" {
		f.Status = FactStatusActive
	}
	if f.Source == "" {
		f.Source = "extracted"
	}
	if f.Confidence <= 0 || f.Confidence > 1 {
		f.Confidence = 1
	}
	now := time.Now().UTC().Truncate(time.Second)
	if f.CreatedAt.IsZero() {
		f.CreatedAt = now
	}
	f.UpdatedAt = now
	return f, nil
}

// Supersede atomically marks old as superseded and inserts replacement as the
// active fact. The old row stays for audit with superseded_by set.
func (s *FactSQLite) Supersede(ctx context.Context, scope FactScope, oldID string, replacement Fact) (Fact, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return Fact{}, ErrInvalidScope
	}
	replacement.Workspace, replacement.Owner, replacement.AgentID = scope.Workspace, scope.Owner, scope.AgentID
	replacement.Supersedes = oldID
	replacement, err := prepareFact(replacement)
	if err != nil {
		return Fact{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Fact{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx, `UPDATE adaptive_facts SET status=?, superseded_by=?, updated_at=?
		WHERE id=? AND workspace=? AND owner=? AND agent_id=? AND status=?`,
		FactStatusSuperseded, replacement.ID, replacement.UpdatedAt.Unix(), oldID, scope.Workspace, scope.Owner, scope.AgentID, FactStatusActive)
	if err != nil {
		return Fact{}, fmt.Errorf("adaptive memory: supersede: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Fact{}, ErrFactNotFound
	}
	if err := s.insertTx(ctx, tx, replacement); err != nil {
		return Fact{}, err
	}
	if err := tx.Commit(); err != nil {
		return Fact{}, err
	}
	return replacement, nil
}

// Get returns one fact inside the scope.
func (s *FactSQLite) Get(ctx context.Context, scope FactScope, id string) (Fact, error) {
	scope = scope.Normalize()
	row := s.db.QueryRowContext(ctx, `SELECT `+factColumns+` FROM adaptive_facts WHERE id=? AND workspace=? AND owner=?`+agentClause(scope), append([]any{id, scope.Workspace, scope.Owner}, agentArgs(scope)...)...)
	f, err := scanFact(row)
	if err == sql.ErrNoRows {
		return Fact{}, ErrFactNotFound
	}
	return f, err
}

// agentClause narrows to one agent when the scope names one; an empty agent
// means "every agent this owner has facts for" (the GUI's cross-agent view).
func agentClause(scope FactScope) string {
	if scope.AgentID == "" {
		return ""
	}
	return " AND agent_id=?"
}

func agentArgs(scope FactScope) []any {
	if scope.AgentID == "" {
		return nil
	}
	return []any{scope.AgentID}
}

// List returns facts in the scope, newest first. status "" returns all.
func (s *FactSQLite) List(ctx context.Context, scope FactScope, status string, limit int) ([]Fact, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return nil, ErrInvalidScope
	}
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	q := `SELECT ` + factColumns + ` FROM adaptive_facts WHERE workspace=? AND owner=?` + agentClause(scope)
	args := append([]any{scope.Workspace, scope.Owner}, agentArgs(scope)...)
	if status != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	q += ` ORDER BY updated_at DESC, rowid DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("adaptive memory: list: %w", err)
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Active returns every active fact in the scope (bounded), used for
// similarity checks and hybrid retrieval.
func (s *FactSQLite) Active(ctx context.Context, scope FactScope) ([]Fact, error) {
	return s.List(ctx, scope, FactStatusActive, 2000)
}

// Update rewrites content and category. An updated fact gets a fresh
// embedding from the caller (the engine re-embeds before calling this).
func (s *FactSQLite) Update(ctx context.Context, scope FactScope, id, content string, category FactCategory, embedding []float32) (Fact, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return Fact{}, ErrInvalidScope
	}
	content, err := NormalizeFactContent(content)
	if err != nil {
		return Fact{}, err
	}
	if !ValidFactCategory(string(category)) {
		return Fact{}, ErrInvalidFact
	}
	now := time.Now().UTC().Truncate(time.Second)
	res, err := s.db.ExecContext(ctx, `UPDATE adaptive_facts SET content=?, category=?, embedding=?, updated_at=?
		WHERE id=? AND workspace=? AND owner=?`+agentClause(scope),
		append([]any{content, string(FactCategory(strings.ToLower(string(category)))), encodeEmbedding(embedding), now.Unix(), id, scope.Workspace, scope.Owner}, agentArgs(scope)...)...)
	if err != nil {
		return Fact{}, fmt.Errorf("adaptive memory: update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Fact{}, ErrFactNotFound
	}
	return s.Get(ctx, scope, id)
}

// Delete removes one fact permanently.
func (s *FactSQLite) Delete(ctx context.Context, scope FactScope, id string) error {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return ErrInvalidScope
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM adaptive_facts WHERE id=? AND workspace=? AND owner=?`+agentClause(scope),
		append([]any{id, scope.Workspace, scope.Owner}, agentArgs(scope)...)...)
	if err != nil {
		return fmt.Errorf("adaptive memory: delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrFactNotFound
	}
	return nil
}

// Purge removes every fact in the scope (all agents when AgentID is empty).
func (s *FactSQLite) Purge(ctx context.Context, scope FactScope) (int64, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return 0, ErrInvalidScope
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM adaptive_facts WHERE workspace=? AND owner=?`+agentClause(scope),
		append([]any{scope.Workspace, scope.Owner}, agentArgs(scope)...)...)
	if err != nil {
		return 0, fmt.Errorf("adaptive memory: purge: %w", err)
	}
	return res.RowsAffected()
}

// Count returns active and superseded counts for the scope.
func (s *FactSQLite) Count(ctx context.Context, scope FactScope) (active, superseded int64, err error) {
	scope = scope.Normalize()
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM adaptive_facts WHERE workspace=? AND owner=?`+agentClause(scope)+` GROUP BY status`,
		append([]any{scope.Workspace, scope.Owner}, agentArgs(scope)...)...)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return 0, 0, err
		}
		if st == FactStatusActive {
			active = n
		} else {
			superseded += n
		}
	}
	return active, superseded, rows.Err()
}
