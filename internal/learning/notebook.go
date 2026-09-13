package learning

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/soulacy/soulacy/internal/redact"
	"github.com/soulacy/soulacy/internal/sqlitex"
)

// Notebook is the single source of truth for learned behavior. Unlike legacy
// proposal promotion it never copies private guidance into shared rulebooks,
// global skill directories or Studio. Activation/refinement is transactional.
type Notebook struct{ db *sql.DB }

func OpenNotebook(path string) (*Notebook, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return nil, ErrInvalidLesson
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, ErrInvalidLesson
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	err = f.Chmod(0600)
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	opts := sqlitex.DefaultOptions()
	opts.MaxOpenConns, opts.MaxIdleConns = 1, 1
	dsn := sqlitex.DSN((&url.URL{Scheme: "file", Path: path}).String(), opts)
	dsn = strings.Replace(dsn, "_synchronous=NORMAL", "_synchronous=FULL", 1) + "&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	_, err = sqlitex.MigrateSchema(db, "learning_notebook", []sqlitex.SchemaMigration{{Version: 1, SQL: `
CREATE TABLE learned_lessons(id TEXT PRIMARY KEY, owner TEXT NOT NULL, agent_id TEXT NOT NULL, lesson_key TEXT NOT NULL, status TEXT NOT NULL, payload BLOB NOT NULL);
CREATE INDEX learned_scope ON learned_lessons(owner, agent_id, status);
CREATE UNIQUE INDEX learned_active ON learned_lessons(owner, agent_id, lesson_key) WHERE status='active';
CREATE TABLE learned_uses(owner TEXT NOT NULL, agent_id TEXT NOT NULL, lesson_id TEXT NOT NULL, run_id TEXT NOT NULL, PRIMARY KEY(owner,agent_id,lesson_id,run_id));
CREATE TABLE learned_feedback(owner TEXT NOT NULL, agent_id TEXT NOT NULL, lesson_id TEXT NOT NULL, rating INTEGER NOT NULL, PRIMARY KEY(owner,agent_id,lesson_id));
CREATE TABLE learned_episodes(owner TEXT NOT NULL, agent_id TEXT NOT NULL, run_id TEXT NOT NULL, created_at INTEGER NOT NULL, payload BLOB NOT NULL, PRIMARY KEY(owner,agent_id,run_id));
`}})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Notebook{db: db}, nil
}

func (n *Notebook) Close() error { return n.db.Close() }

type notebookQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func notebookLessons(ctx context.Context, q notebookQuery, scope Scope) ([]Lesson, error) {
	if !validScope(scope) {
		return nil, ErrInvalidLesson
	}
	rows, err := q.QueryContext(ctx, `SELECT payload FROM learned_lessons WHERE owner=? AND agent_id=? ORDER BY rowid DESC LIMIT 2001`, scope.Owner, scope.AgentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Lesson{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var l Lesson
		if err := json.Unmarshal(raw, &l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (n *Notebook) List(ctx context.Context, scope Scope, status string) ([]Lesson, error) {
	all, err := notebookLessons(ctx, n.db, scope)
	if err != nil {
		return nil, err
	}
	out := []Lesson{}
	for _, l := range all {
		if status == "" || l.Status == status {
			out = append(out, l)
		}
	}
	return out, nil
}

func saveLesson(ctx context.Context, tx *sql.Tx, scope Scope, l Lesson) error {
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO learned_lessons(id,owner,agent_id,lesson_key,status,payload) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,payload=excluded.payload WHERE owner=excluded.owner AND agent_id=excluded.agent_id`, l.ID, scope.Owner, scope.AgentID, l.Key, l.Status, raw)
	return err
}

func activeLesson(all []Lesson, key string) *Lesson {
	for _, l := range all {
		if l.Key == key && l.Status == "active" {
			return &l
		}
	}
	return nil
}
func sameLesson(a, b Draft) bool {
	return a.Kind == b.Kind && normalized(a.Trigger) == normalized(b.Trigger) && normalized(a.Content) == normalized(b.Content) && normalized(a.Verification) == normalized(b.Verification) && normalized(a.Pitfalls) == normalized(b.Pitfalls)
}

func (n *Notebook) Propose(ctx context.Context, scope Scope, runID, sessionID string, d Draft, sources []Source) (Lesson, bool, error) {
	if !validScope(scope) || !bounded(runID, 128) || len(sessionID) > 128 {
		return Lesson{}, false, ErrInvalidLesson
	}
	refs, err := validateDraft(d, sources)
	if err != nil {
		return Lesson{}, false, err
	}
	tx, err := n.db.BeginTx(ctx, nil)
	if err != nil {
		return Lesson{}, false, err
	}
	defer func() { _ = tx.Rollback() }() // No-op after commit; preserve the operation's error.
	all, err := notebookLessons(ctx, tx, scope)
	if err != nil {
		return Lesson{}, false, err
	}
	for _, l := range all {
		if sameLesson(l.Draft, d) && (l.Status != "pending" || l.BaseID == d.BaseID) {
			return l, false, nil
		}
	}
	head := activeLesson(all, d.Key)
	base := ""
	version := 1
	for _, l := range all {
		if l.Key == d.Key {
			version = max(version, l.Version+1)
		}
	}
	if head != nil {
		base = head.ID
		if head.Kind != d.Kind {
			return Lesson{}, false, ErrLessonConflict
		}
	}
	if base != d.BaseID {
		if base == "" {
			return Lesson{}, false, fmt.Errorf("%w: no active lesson uses this key; this is a NEW lesson, so remove base_id entirely (do not send None or null) and retry the proposal", ErrLessonConflict)
		}
		return Lesson{}, false, fmt.Errorf("%w: inspect the active lesson with learning.read, then refine using base_id %q", ErrLessonConflict, base)
	}
	pending := 0
	for _, l := range all {
		if l.Status == "pending" {
			pending++
		}
	}
	if len(all) >= 2000 || pending >= 200 {
		return Lesson{}, false, ErrNotebookFull
	}
	now := time.Now().UTC()
	l := Lesson{Draft: d, ID: uuid.NewString(), AgentID: scope.AgentID, Version: version, Status: "pending", RunID: runID, SessionID: sessionID, Sources: refs, CreatedAt: now, UpdatedAt: now}
	if err := saveLesson(ctx, tx, scope, l); err != nil {
		return Lesson{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Lesson{}, false, err
	}
	return l, true, nil
}

// Review only mutates the requested immutable revision. Repeated acceptance
// is idempotent; stale parallel refinements cannot overwrite newer guidance.
func (n *Notebook) Review(ctx context.Context, scope Scope, id, action string) (Lesson, error) {
	if !validScope(scope) || !bounded(id, 128) {
		return Lesson{}, ErrInvalidLesson
	}
	tx, err := n.db.BeginTx(ctx, nil)
	if err != nil {
		return Lesson{}, err
	}
	defer func() { _ = tx.Rollback() }() // No-op after commit; preserve the operation's error.
	all, err := notebookLessons(ctx, tx, scope)
	if err != nil {
		return Lesson{}, err
	}
	var l Lesson
	for _, v := range all {
		if v.ID == id {
			l = v
			break
		}
	}
	if l.ID == "" {
		return Lesson{}, ErrLessonNotFound
	}
	switch action {
	case "approve":
		if l.Status == "active" {
			return l, nil
		}
		if l.Status != "pending" {
			return Lesson{}, ErrLessonConflict
		}
		head := activeLesson(all, l.Key)
		base := ""
		if head != nil {
			base = head.ID
		}
		if base != l.BaseID {
			return Lesson{}, ErrLessonConflict
		}
		active := 0
		for _, v := range all {
			if v.Status == "active" {
				active++
			}
		}
		if head == nil && active >= 100 {
			return Lesson{}, ErrNotebookFull
		}
		if head != nil {
			head.Status = "superseded"
			head.UpdatedAt = time.Now().UTC()
			if err := saveLesson(ctx, tx, scope, *head); err != nil {
				return Lesson{}, err
			}
		}
		l.Status = "active"
	case "reject":
		if l.Status == "rejected" {
			return l, nil
		}
		if l.Status != "pending" {
			return Lesson{}, ErrLessonConflict
		}
		l.Status = "rejected"
	case "archive":
		if l.Status == "archived" {
			return l, nil
		}
		if l.Status != "active" {
			return Lesson{}, ErrLessonConflict
		}
		l.Status = "archived"
	case "restore":
		if l.Status != "archived" && l.Status != "superseded" {
			return Lesson{}, ErrLessonConflict
		}
		base := ""
		if head := activeLesson(all, l.Key); head != nil {
			base = head.ID
		}
		pending := 0
		for _, v := range all {
			if v.Status == "pending" && v.BaseID == base && sameLesson(v.Draft, l.Draft) {
				return v, nil
			}
			if v.Status == "pending" {
				pending++
			}
		}
		if len(all) >= 2000 || pending >= 200 {
			return Lesson{}, ErrNotebookFull
		}
		l.ID = uuid.NewString()
		l.BaseID = base
		l.Version++
		for _, v := range all {
			if v.Key == l.Key {
				l.Version = max(l.Version, v.Version+1)
			}
		}
		l.Status = "pending"
		l.Uses = 0
		l.Helpful = 0
		l.Unhelpful = 0
		l.CreatedAt = time.Now().UTC()
	default:
		return Lesson{}, ErrInvalidLesson
	}
	l.UpdatedAt = time.Now().UTC()
	if err := saveLesson(ctx, tx, scope, l); err != nil {
		return Lesson{}, err
	}
	if err := tx.Commit(); err != nil {
		return Lesson{}, err
	}
	return l, nil
}

func (n *Notebook) Get(ctx context.Context, scope Scope, id string) (Lesson, error) {
	if !validScope(scope) {
		return Lesson{}, ErrInvalidLesson
	}
	var raw []byte
	err := n.db.QueryRowContext(ctx, `SELECT payload FROM learned_lessons WHERE id=? AND owner=? AND agent_id=?`, id, scope.Owner, scope.AgentID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Lesson{}, ErrLessonNotFound
	}
	if err != nil {
		return Lesson{}, err
	}
	var l Lesson
	err = json.Unmarshal(raw, &l)
	return l, err
}

func (n *Notebook) Search(ctx context.Context, scope Scope, query string, limit int) ([]Lesson, error) {
	all, err := n.List(ctx, scope, "active")
	if err != nil {
		return nil, err
	}
	terms := lessonTerms(query)
	type hit struct {
		l Lesson
		s int
	}
	hits := []hit{}
	for _, l := range all {
		score := 3*termScore(terms, l.Title+" "+l.Trigger) + termScore(terms, l.Content)
		if score > 0 || (query == "" && l.Kind == "preference") {
			hits = append(hits, hit{l, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].s != hits[j].s {
			return hits[i].s > hits[j].s
		}
		return hits[i].l.UpdatedAt.After(hits[j].l.UpdatedAt)
	})
	limit = max(1, min(limit, 10))
	out := []Lesson{}
	for _, h := range hits {
		out = append(out, h.l)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// Feedback is one current human vote per revision, never model self-grading.
func (n *Notebook) Feedback(ctx context.Context, scope Scope, id string, rating int) (Lesson, error) {
	if !validScope(scope) || (rating != 1 && rating != -1) {
		return Lesson{}, ErrInvalidLesson
	}
	tx, err := n.db.BeginTx(ctx, nil)
	if err != nil {
		return Lesson{}, err
	}
	defer func() { _ = tx.Rollback() }() // No-op after commit; preserve the operation's error.
	all, err := notebookLessons(ctx, tx, scope)
	if err != nil {
		return Lesson{}, err
	}
	var l Lesson
	for _, v := range all {
		if v.ID == id {
			l = v
			break
		}
	}
	if l.ID == "" {
		return Lesson{}, ErrLessonNotFound
	}
	if l.Status != "active" {
		return Lesson{}, ErrLessonConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO learned_feedback VALUES(?,?,?,?) ON CONFLICT(owner,agent_id,lesson_id) DO UPDATE SET rating=excluded.rating`, scope.Owner, scope.AgentID, id, rating)
	if err != nil {
		return Lesson{}, err
	}
	l.Helpful, l.Unhelpful = 0, 0
	if rating == 1 {
		l.Helpful = 1
	} else {
		l.Unhelpful = 1
	}
	if err := saveLesson(ctx, tx, scope, l); err != nil {
		return Lesson{}, err
	}
	if err := tx.Commit(); err != nil {
		return Lesson{}, err
	}
	return l, nil
}

func (n *Notebook) RecordUse(ctx context.Context, scope Scope, id, runID string) error {
	if !validScope(scope) || !bounded(runID, 128) {
		return ErrInvalidLesson
	}
	tx, err := n.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // No-op after commit; preserve the operation's error.
	all, err := notebookLessons(ctx, tx, scope)
	if err != nil {
		return err
	}
	var l Lesson
	for _, v := range all {
		if v.ID == id && v.Status == "active" {
			l = v
			break
		}
	}
	if l.ID == "" {
		return ErrLessonNotFound
	}
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO learned_uses VALUES(?,?,?,?)`, scope.Owner, scope.AgentID, id, runID)
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count > 0 {
		l.Uses++
		if err := saveLesson(ctx, tx, scope, l); err != nil {
			return err
		}
	}
	// Usage receipts only need to cover recent runs for replay deduplication.
	_, err = tx.ExecContext(ctx, `DELETE FROM learned_uses WHERE owner=? AND agent_id=? AND rowid NOT IN (SELECT rowid FROM learned_uses WHERE owner=? AND agent_id=? ORDER BY rowid DESC LIMIT 5000)`, scope.Owner, scope.AgentID, scope.Owner, scope.AgentID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (n *Notebook) RecordEpisode(ctx context.Context, scope Scope, ep Episode) error {
	if !validScope(scope) || !bounded(ep.RunID, 128) || !bounded(ep.SessionID, 128) {
		return ErrInvalidLesson
	}
	ep.Request = Clip(redact.Text(ep.Request), 2000)
	ep.Reply = Clip(redact.Text(ep.Reply), 4000)
	ep.CreatedAt = time.Now().UTC()
	raw, err := json.Marshal(ep)
	if err != nil {
		return err
	}
	tx, err := n.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // No-op after commit; preserve the operation's error.
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO learned_episodes VALUES(?,?,?,?,?)`, scope.Owner, scope.AgentID, ep.RunID, ep.CreatedAt.UnixNano(), raw)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM learned_episodes WHERE owner=? AND agent_id=? AND run_id NOT IN (SELECT run_id FROM learned_episodes WHERE owner=? AND agent_id=? ORDER BY created_at DESC LIMIT 500)`, scope.Owner, scope.AgentID, scope.Owner, scope.AgentID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (n *Notebook) Recall(ctx context.Context, scope Scope, query, excludeSession string) ([]Episode, error) {
	if !validScope(scope) {
		return nil, ErrInvalidLesson
	}
	rows, err := n.db.QueryContext(ctx, `SELECT payload FROM learned_episodes WHERE owner=? AND agent_id=? ORDER BY created_at DESC LIMIT 500`, scope.Owner, scope.AgentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type hit struct {
		e Episode
		s int
	}
	hits := []hit{}
	terms := lessonTerms(query)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var ep Episode
		if err := json.Unmarshal(raw, &ep); err != nil {
			return nil, err
		}
		if ep.SessionID == excludeSession {
			continue
		}
		score := 2*termScore(terms, ep.Request) + termScore(terms, ep.Reply)
		if score > 0 {
			ep.Reply = Clip(ep.Reply, 1000)
			hits = append(hits, hit{ep, score})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].s > hits[j].s })
	out := []Episode{}
	for _, h := range hits {
		out = append(out, h.e)
		if len(out) == 5 {
			break
		}
	}
	return out, nil
}

// ExportSkill produces a portable procedure document, never a filesystem write
// or an installed executable. Owner-scoped runtime reads remain authoritative.
func ExportSkill(l Lesson) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %q\n---\n\n# %s\n\n## When to use\n%s\n\n## Procedure\n%s\n\n## Pitfalls\n%s\n\n## Verification\n%s\n", l.Key, l.Trigger, l.Title, l.Trigger, l.Content, l.Pitfalls, l.Verification)
}
