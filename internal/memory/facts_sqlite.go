// facts_sqlite.go — SQLite-backed store for adaptive memory facts, entity
// relations and per-fact history.
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
	if _, err := sqlitex.MigrateSchema(db, "adaptive_memory", []sqlitex.SchemaMigration{
		{Version: 1, SQL: `
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
`},
		{Version: 2, SQL: `
ALTER TABLE adaptive_facts ADD COLUMN expires_at INTEGER;
ALTER TABLE adaptive_facts ADD COLUMN session_scoped INTEGER NOT NULL DEFAULT 0;
CREATE TABLE adaptive_relations(
  id TEXT PRIMARY KEY,
  workspace TEXT NOT NULL,
  owner TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  subject TEXT NOT NULL,
  predicate TEXT NOT NULL,
  object TEXT NOT NULL,
  status TEXT NOT NULL,
  source TEXT NOT NULL,
  source_session_id TEXT,
  source_run_id TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX adaptive_relations_scope ON adaptive_relations(workspace, owner, agent_id, status);
CREATE TABLE adaptive_fact_history(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  fact_id TEXT NOT NULL,
  workspace TEXT NOT NULL,
  owner TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  event TEXT NOT NULL,
  content TEXT NOT NULL,
  category TEXT NOT NULL,
  status TEXT NOT NULL,
  actor TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX adaptive_fact_history_fact ON adaptive_fact_history(fact_id, id);
`},
	}); err != nil {
		db.Close()
		return nil, err
	}
	return &FactSQLite{db: db}, nil
}

// Close releases the database.
func (s *FactSQLite) Close() error { return s.db.Close() }

const factColumns = `id, workspace, owner, agent_id, category, content, confidence, status,
	COALESCE(superseded_by,''), COALESCE(supersedes,''), source,
	COALESCE(source_session_id,''), COALESCE(source_run_id,''), embedding, created_at, updated_at,
	expires_at, session_scoped`

func scanFact(sc interface{ Scan(...any) error }) (Fact, error) {
	var f Fact
	var emb []byte
	var created, updated int64
	var expires sql.NullInt64
	var sessionScoped int
	if err := sc.Scan(&f.ID, &f.Workspace, &f.Owner, &f.AgentID, &f.Category, &f.Content, &f.Confidence, &f.Status,
		&f.SupersededBy, &f.Supersedes, &f.Source, &f.SourceSessionID, &f.SourceRunID, &emb, &created, &updated,
		&expires, &sessionScoped); err != nil {
		return Fact{}, err
	}
	f.Embedding = decodeEmbedding(emb)
	f.CreatedAt = time.Unix(created, 0).UTC()
	f.UpdatedAt = time.Unix(updated, 0).UTC()
	if expires.Valid && expires.Int64 > 0 {
		t := time.Unix(expires.Int64, 0).UTC()
		f.ExpiresAt = &t
	}
	f.SessionScoped = sessionScoped != 0
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

func expiresArg(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.Unix()
}

func boolArg(b bool) int {
	if b {
		return 1
	}
	return 0
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// Insert stores a new fact and records a "created" history event. Missing
// ID/timestamps/status are filled in.
func (s *FactSQLite) Insert(ctx context.Context, f Fact) (Fact, error) {
	f, err := prepareFact(f)
	if err != nil {
		return Fact{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Fact{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := s.insertTx(ctx, tx, f); err != nil {
		return Fact{}, err
	}
	if err := s.historyTx(ctx, tx, f, "created", actorFor(f.Source)); err != nil {
		return Fact{}, err
	}
	if err := tx.Commit(); err != nil {
		return Fact{}, err
	}
	return f, nil
}

func actorFor(source string) string {
	switch source {
	case "manual":
		return "user"
	case "provider":
		return "provider"
	}
	return "extractor"
}

func (s *FactSQLite) insertTx(ctx context.Context, x execer, f Fact) error {
	_, err := x.ExecContext(ctx, `INSERT INTO adaptive_facts
		(id, workspace, owner, agent_id, category, content, confidence, status, superseded_by, supersedes, source, source_session_id, source_run_id, embedding, created_at, updated_at, expires_at, session_scoped)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.ID, f.Workspace, f.Owner, f.AgentID, string(f.Category), f.Content, f.Confidence, f.Status,
		nullIfEmpty(f.SupersededBy), nullIfEmpty(f.Supersedes), f.Source, nullIfEmpty(f.SourceSessionID), nullIfEmpty(f.SourceRunID),
		encodeEmbedding(f.Embedding), f.CreatedAt.Unix(), f.UpdatedAt.Unix(), expiresArg(f.ExpiresAt), boolArg(f.SessionScoped))
	if err != nil {
		return fmt.Errorf("adaptive memory: insert: %w", err)
	}
	return nil
}

func (s *FactSQLite) historyTx(ctx context.Context, x execer, f Fact, event, actor string) error {
	_, err := x.ExecContext(ctx, `INSERT INTO adaptive_fact_history (fact_id, workspace, owner, agent_id, event, content, category, status, actor, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		f.ID, f.Workspace, f.Owner, f.AgentID, event, f.Content, string(f.Category), f.Status, actor, time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("adaptive memory: history: %w", err)
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// prepareFact validates and fills defaults. Category validity against custom
// categories is the engine's job; here only the slug shape is enforced.
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
	cat := strings.ToLower(strings.TrimSpace(string(f.Category)))
	if !categorySlug.MatchString(cat) {
		return Fact{}, ErrInvalidFact
	}
	f.Category = FactCategory(cat)
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
	if f.ExpiresAt != nil && f.ExpiresAt.IsZero() {
		f.ExpiresAt = nil
	}
	if f.SessionScoped && f.SourceSessionID == "" {
		f.SessionScoped = false
	}
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
	old, err := s.getTx(ctx, tx, scope, oldID)
	if err != nil {
		return Fact{}, err
	}
	if old.Status != FactStatusActive {
		return Fact{}, ErrFactNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE adaptive_facts SET status=?, superseded_by=?, updated_at=? WHERE id=?`,
		FactStatusSuperseded, replacement.ID, replacement.UpdatedAt.Unix(), oldID); err != nil {
		return Fact{}, fmt.Errorf("adaptive memory: supersede: %w", err)
	}
	old.Status = FactStatusSuperseded
	if err := s.historyTx(ctx, tx, old, "superseded", "extractor"); err != nil {
		return Fact{}, err
	}
	if err := s.insertTx(ctx, tx, replacement); err != nil {
		return Fact{}, err
	}
	if err := s.historyTx(ctx, tx, replacement, "created", "extractor"); err != nil {
		return Fact{}, err
	}
	if err := tx.Commit(); err != nil {
		return Fact{}, err
	}
	return replacement, nil
}

// Retract marks an active fact as no longer true. The row stays for audit.
func (s *FactSQLite) Retract(ctx context.Context, scope FactScope, id, actor string) (Fact, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return Fact{}, ErrInvalidScope
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Fact{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	f, err := s.getTx(ctx, tx, scope, id)
	if err != nil {
		return Fact{}, err
	}
	if f.Status != FactStatusActive {
		return Fact{}, ErrFactNotFound
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := tx.ExecContext(ctx, `UPDATE adaptive_facts SET status=?, updated_at=? WHERE id=?`, FactStatusRetracted, now.Unix(), id); err != nil {
		return Fact{}, fmt.Errorf("adaptive memory: retract: %w", err)
	}
	f.Status, f.UpdatedAt = FactStatusRetracted, now
	if err := s.historyTx(ctx, tx, f, "retracted", actor); err != nil {
		return Fact{}, err
	}
	if err := tx.Commit(); err != nil {
		return Fact{}, err
	}
	return f, nil
}

type querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *FactSQLite) getTx(ctx context.Context, q querier, scope FactScope, id string) (Fact, error) {
	row := q.QueryRowContext(ctx, `SELECT `+factColumns+` FROM adaptive_facts WHERE id=? AND workspace=? AND owner=?`+agentClause(scope), append([]any{id, scope.Workspace, scope.Owner}, agentArgs(scope)...)...)
	f, err := scanFact(row)
	if err == sql.ErrNoRows {
		return Fact{}, ErrFactNotFound
	}
	return f, err
}

// Get returns one fact inside the scope.
func (s *FactSQLite) Get(ctx context.Context, scope FactScope, id string) (Fact, error) {
	return s.getTx(ctx, s.db, scope.Normalize(), id)
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

// Active returns the facts eligible for recall in the scope: active, not
// expired, and either global or scoped to the scope's session.
func (s *FactSQLite) Active(ctx context.Context, scope FactScope) ([]Fact, error) {
	all, err := s.List(ctx, scope, FactStatusActive, 2000)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := all[:0]
	for _, f := range all {
		if f.Expired(now) {
			continue
		}
		if f.SessionScoped && f.SourceSessionID != scope.SessionID {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// Update rewrites content, category, embedding and expiry, recording the
// previous version in history.
func (s *FactSQLite) Update(ctx context.Context, scope FactScope, id string, in FactInput, embedding []float32, actor string) (Fact, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return Fact{}, ErrInvalidScope
	}
	content, err := NormalizeFactContent(in.Content)
	if err != nil {
		return Fact{}, err
	}
	cat := strings.ToLower(strings.TrimSpace(string(in.Category)))
	if !categorySlug.MatchString(cat) {
		return Fact{}, ErrInvalidFact
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Fact{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	prev, err := s.getTx(ctx, tx, scope, id)
	if err != nil {
		return Fact{}, err
	}
	if err := s.historyTx(ctx, tx, prev, "updated", actor); err != nil {
		return Fact{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := tx.ExecContext(ctx, `UPDATE adaptive_facts SET content=?, category=?, embedding=?, expires_at=?, updated_at=? WHERE id=?`,
		content, cat, encodeEmbedding(embedding), expiresArg(in.ExpiresAt), now.Unix(), id); err != nil {
		return Fact{}, fmt.Errorf("adaptive memory: update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Fact{}, err
	}
	return s.Get(ctx, scope, id)
}

// Delete removes one fact permanently, leaving a "deleted" history event.
func (s *FactSQLite) Delete(ctx context.Context, scope FactScope, id, actor string) error {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return ErrInvalidScope
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	f, err := s.getTx(ctx, tx, scope, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM adaptive_facts WHERE id=?`, id); err != nil {
		return fmt.Errorf("adaptive memory: delete: %w", err)
	}
	if err := s.historyTx(ctx, tx, f, "deleted", actor); err != nil {
		return err
	}
	return tx.Commit()
}

// History returns the change log of one fact, newest first. The fact may
// already be deleted; the log survives it.
func (s *FactSQLite) History(ctx context.Context, scope FactScope, id string) ([]FactEvent, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return nil, ErrInvalidScope
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, fact_id, event, content, category, status, actor, created_at
		FROM adaptive_fact_history WHERE fact_id=? AND workspace=? AND owner=?`+agentClause(scope)+` ORDER BY id DESC LIMIT 200`,
		append([]any{id, scope.Workspace, scope.Owner}, agentArgs(scope)...)...)
	if err != nil {
		return nil, fmt.Errorf("adaptive memory: history: %w", err)
	}
	defer rows.Close()
	var out []FactEvent
	for rows.Next() {
		var e FactEvent
		var created int64
		if err := rows.Scan(&e.ID, &e.FactID, &e.Event, &e.Content, &e.Category, &e.Status, &e.Actor, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, ErrFactNotFound
	}
	return out, rows.Err()
}

// Purge removes every fact, relation and history row in the scope (all
// agents when AgentID is empty).
func (s *FactSQLite) Purge(ctx context.Context, scope FactScope) (int64, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return 0, ErrInvalidScope
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	args := append([]any{scope.Workspace, scope.Owner}, agentArgs(scope)...)
	res, err := tx.ExecContext(ctx, `DELETE FROM adaptive_facts WHERE workspace=? AND owner=?`+agentClause(scope), args...)
	if err != nil {
		return 0, fmt.Errorf("adaptive memory: purge: %w", err)
	}
	for _, table := range []string{"adaptive_relations", "adaptive_fact_history"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE workspace=? AND owner=?`+agentClause(scope), args...); err != nil {
			return 0, fmt.Errorf("adaptive memory: purge %s: %w", table, err)
		}
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

// Count returns active, superseded and retracted counts for the scope.
func (s *FactSQLite) Count(ctx context.Context, scope FactScope) (active, superseded, retracted int64, err error) {
	scope = scope.Normalize()
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM adaptive_facts WHERE workspace=? AND owner=?`+agentClause(scope)+` GROUP BY status`,
		append([]any{scope.Workspace, scope.Owner}, agentArgs(scope)...)...)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return 0, 0, 0, err
		}
		switch st {
		case FactStatusActive:
			active = n
		case FactStatusRetracted:
			retracted = n
		default:
			superseded += n
		}
	}
	return active, superseded, retracted, rows.Err()
}

// ── relations ───────────────────────────────────────────────────────────────

const relationColumns = `id, workspace, owner, agent_id, subject, predicate, object, status, source,
	COALESCE(source_session_id,''), COALESCE(source_run_id,''), created_at, updated_at`

func scanRelation(sc interface{ Scan(...any) error }) (Relation, error) {
	var r Relation
	var created, updated int64
	if err := sc.Scan(&r.ID, &r.Workspace, &r.Owner, &r.AgentID, &r.Subject, &r.Predicate, &r.Object, &r.Status, &r.Source,
		&r.SourceSessionID, &r.SourceRunID, &created, &updated); err != nil {
		return Relation{}, err
	}
	r.CreatedAt = time.Unix(created, 0).UTC()
	r.UpdatedAt = time.Unix(updated, 0).UTC()
	return r, nil
}

func normalizeRelation(r Relation) (Relation, error) {
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	r.Subject, r.Predicate, r.Object = norm(r.Subject), strings.ToLower(norm(r.Predicate)), norm(r.Object)
	if r.Subject == "" || r.Predicate == "" || r.Object == "" || len(r.Subject) > 120 || len(r.Predicate) > 60 || len(r.Object) > 120 {
		return Relation{}, ErrInvalidFact
	}
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if r.Status == "" {
		r.Status = FactStatusActive
	}
	if r.Source == "" {
		r.Source = "extracted"
	}
	now := time.Now().UTC().Truncate(time.Second)
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	return r, nil
}

// UpsertRelation stores a relation. An identical active triple is a no-op
// (returns false). An active triple with the same subject and predicate but
// a different object is superseded, so "User lives in SF" gives way to
// "User lives in NYC".
func (s *FactSQLite) UpsertRelation(ctx context.Context, scope FactScope, r Relation) (Relation, bool, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return Relation{}, false, ErrInvalidScope
	}
	r.Workspace, r.Owner, r.AgentID = scope.Workspace, scope.Owner, scope.AgentID
	r, err := normalizeRelation(r)
	if err != nil {
		return Relation{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Relation{}, false, err
	}
	defer tx.Rollback() //nolint:errcheck
	rows, err := tx.QueryContext(ctx, `SELECT `+relationColumns+` FROM adaptive_relations
		WHERE workspace=? AND owner=? AND agent_id=? AND status=? AND lower(subject)=lower(?) AND predicate=?`,
		scope.Workspace, scope.Owner, scope.AgentID, FactStatusActive, r.Subject, r.Predicate)
	if err != nil {
		return Relation{}, false, err
	}
	var existing []Relation
	for rows.Next() {
		x, err := scanRelation(rows)
		if err != nil {
			rows.Close()
			return Relation{}, false, err
		}
		existing = append(existing, x)
	}
	rows.Close()
	for _, x := range existing {
		if strings.EqualFold(x.Object, r.Object) {
			return x, false, nil
		}
	}
	for _, x := range existing {
		if _, err := tx.ExecContext(ctx, `UPDATE adaptive_relations SET status=?, updated_at=? WHERE id=?`, FactStatusSuperseded, r.UpdatedAt.Unix(), x.ID); err != nil {
			return Relation{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO adaptive_relations
		(id, workspace, owner, agent_id, subject, predicate, object, status, source, source_session_id, source_run_id, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Workspace, r.Owner, r.AgentID, r.Subject, r.Predicate, r.Object, r.Status, r.Source,
		nullIfEmpty(r.SourceSessionID), nullIfEmpty(r.SourceRunID), r.CreatedAt.Unix(), r.UpdatedAt.Unix()); err != nil {
		return Relation{}, false, fmt.Errorf("adaptive memory: insert relation: %w", err)
	}
	return r, true, tx.Commit()
}

// Relations returns relations in the scope, newest first. status "" = all.
func (s *FactSQLite) Relations(ctx context.Context, scope FactScope, status string, limit int) ([]Relation, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return nil, ErrInvalidScope
	}
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	q := `SELECT ` + relationColumns + ` FROM adaptive_relations WHERE workspace=? AND owner=?` + agentClause(scope)
	args := append([]any{scope.Workspace, scope.Owner}, agentArgs(scope)...)
	if status != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	q += ` ORDER BY updated_at DESC, rowid DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("adaptive memory: relations: %w", err)
	}
	defer rows.Close()
	var out []Relation
	for rows.Next() {
		r, err := scanRelation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRelation removes one relation permanently.
func (s *FactSQLite) DeleteRelation(ctx context.Context, scope FactScope, id string) error {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return ErrInvalidScope
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM adaptive_relations WHERE id=? AND workspace=? AND owner=?`+agentClause(scope),
		append([]any{id, scope.Workspace, scope.Owner}, agentArgs(scope)...)...)
	if err != nil {
		return fmt.Errorf("adaptive memory: delete relation: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrFactNotFound
	}
	return nil
}
