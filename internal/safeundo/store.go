package safeundo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/soulacy/soulacy/internal/sqlitex"
)

type Store struct {
	db        *sql.DB
	lock      *os.File
	gate      chan struct{}
	closed    bool
	resources map[string]Resource
	clients   map[string]*http.Client
	token     func(string) string
	now       func() time.Time
}

func NewStore(path string, cfg Config) (*Store, error) {
	if err := Validate(cfg); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "?\x00") {
		return nil, ErrInvalid
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock, err := lockLedger(path)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			lock.Close()
		}
	}()
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("safe undo: unsafe ledger path")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	err = f.Chmod(0600)
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	opts := sqlitex.DefaultOptions()
	opts.MaxOpenConns, opts.MaxIdleConns = 1, 1
	u := (&url.URL{Scheme: "file", Path: path}).String()
	// NORMAL may lose a recently committed intent on power loss. Every ledger
	// connection uses FULL: persist the intent before making an external write.
	dsn := strings.Replace(sqlitex.DSN(u, opts), "_synchronous=NORMAL", "_synchronous=FULL", 1)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() {
		if failed {
			db.Close()
		}
	}()
	_, err = sqlitex.MigrateSchema(db, "safe_undo", []sqlitex.SchemaMigration{{Version: 1, SQL: `
CREATE TABLE safe_undo_jobs(id TEXT PRIMARY KEY, owner TEXT NOT NULL, agent_id TEXT NOT NULL, updated_at TEXT NOT NULL, payload BLOB NOT NULL);
CREATE INDEX safe_undo_owner ON safe_undo_jobs(owner, agent_id, updated_at DESC);
CREATE TABLE safe_undo_effects(id INTEGER PRIMARY KEY AUTOINCREMENT, job_id TEXT NOT NULL, action_index INTEGER NOT NULL, resource_key TEXT NOT NULL, state TEXT NOT NULL, fields_json BLOB NOT NULL, UNIQUE(job_id,action_index));
CREATE INDEX safe_undo_resource ON safe_undo_effects(resource_key);
`}})
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, lock: lock, gate: make(chan struct{}, 1), resources: map[string]Resource{}, clients: map[string]*http.Client{}, token: environmentToken, now: func() time.Time { return time.Now().UTC() }}
	for _, r := range cfg.Resources {
		r.Fields = append([]string(nil), r.Fields...)
		key := r.AgentID + "/" + r.ID
		s.resources[key], s.clients[key] = r, resourceClient(r)
	}
	// The process lock establishes that no old writer is still running.
	rows, err := db.Query(`SELECT payload FROM safe_undo_jobs`)
	if err != nil {
		return nil, err
	}
	interrupted := []Job{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			rows.Close()
			return nil, err
		}
		j, err := decodeJob(b)
		if err != nil {
			rows.Close()
			return nil, err
		}
		changed := false
		for i := range j.Actions {
			if j.Actions[i].Status == "running" {
				j.Actions[i].Status = "needs_review"
				changed = true
			}
		}
		if changed {
			j.Notice = ErrUncertain.Error()
			j.Plan = nil
			interrupted = append(interrupted, j)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range interrupted {
		if err := s.save(context.Background(), &interrupted[i]); err != nil {
			return nil, err
		}
	}
	failed = false
	return s, nil
}

func (s *Store) enter(ctx context.Context) error {
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if s.closed {
		<-s.gate
		return ErrUnavailable
	}
	return nil
}
func (s *Store) leave() { <-s.gate }
func (s *Store) Close() error {
	s.gate <- struct{}{}
	defer s.leave()
	if s.closed {
		return nil
	}
	s.closed = true
	return errors.Join(s.db.Close(), s.lock.Close())
}

func decodeJob(b []byte) (Job, error) {
	var j Job
	if len(b) > MaxJobBytes || json.Unmarshal(b, &j) != nil || len(j.Actions) == 0 || len(j.Actions) > MaxActions {
		return j, ErrUnavailable
	}
	return j, nil
}

func (s *Store) get(ctx context.Context, owner, agentID, id string) (Job, error) {
	var b []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM safe_undo_jobs WHERE id=? AND owner=? AND agent_id=?`, id, owner, agentID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	j, err := decodeJob(b)
	if err != nil {
		return j, err
	}
	// All callers hold the gate. A persisted running action at this point
	// cannot still be executing: its final receipt write failed or we restarted.
	for i := range j.Actions {
		if j.Actions[i].Status == "running" {
			j.Actions[i].Status = "needs_review"
			j.Notice, j.Plan = ErrUncertain.Error(), nil
		}
	}
	if j.Status == "running" {
		err = s.persist(&j)
	}
	return j, err
}

func jobStatus(j Job) string {
	pending, applied, undone := 0, 0, 0
	for _, a := range j.Actions {
		switch a.Status {
		case "needs_review":
			return "needs_review"
		case "running":
			return "running"
		case "pending":
			pending++
		case "applied":
			applied++
		case "undone":
			undone++
		}
	}
	if undone > 0 && applied == 0 {
		return "undone"
	}
	if applied == len(j.Actions) {
		return "applied"
	}
	if pending == len(j.Actions) {
		return "draft"
	}
	return "partial"
}

func actionFields(a Action) []string {
	out := []string{}
	for _, f := range a.Fields {
		out = append(out, f.Name)
	}
	return out
}
func overlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

// save updates the receipt and the conflict index in the same durable commit.
func (s *Store) save(ctx context.Context, j *Job) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // No-op after commit; preserve the operation's error.
	for i := range j.Actions {
		a := &j.Actions[i]
		if a.Status == "pending" || a.Status == "undone" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM safe_undo_effects WHERE job_id=? AND action_index=?`, j.ID, i); err != nil {
				return err
			}
			continue
		}
		fields, _ := json.Marshal(actionFields(*a))
		_, err := tx.ExecContext(ctx, `INSERT INTO safe_undo_effects(job_id,action_index,resource_key,state,fields_json) VALUES(?,?,?,?,?) ON CONFLICT(job_id,action_index) DO UPDATE SET state=excluded.state`, j.ID, i, j.AgentID+"/"+a.ResourceID, a.Status, fields)
		if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT id FROM safe_undo_effects WHERE job_id=? AND action_index=?`, j.ID, i).Scan(&a.Order); err != nil {
			return err
		}
	}
	j.Status = jobStatus(*j)
	j.UpdatedAt = s.now()
	j.Revision++
	b, err := json.Marshal(j)
	if err != nil || len(b) > MaxJobBytes {
		return ErrLimit
	}
	var bytesUsed, count int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(payload)),0),COUNT(*) FROM safe_undo_jobs WHERE id!=?`, j.ID).Scan(&bytesUsed, &count); err != nil {
		return err
	}
	if bytesUsed+int64(len(b)) > MaxStoreBytes || count >= 2000 {
		return ErrLimit
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO safe_undo_jobs(id,owner,agent_id,updated_at,payload) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET updated_at=excluded.updated_at,payload=excluded.payload`, j.ID, j.Owner, j.AgentID, j.UpdatedAt.Format(time.RFC3339Nano), b)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) persist(j *Job) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.save(ctx, j)
}

func (s *Store) competing(ctx context.Context, j Job, a Action, direction string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,state,fields_json FROM safe_undo_effects WHERE resource_key=? AND job_id!=?`, j.AgentID+"/"+a.ResourceID, j.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var order int64
		var state string
		var b []byte
		if err := rows.Scan(&order, &state, &b); err != nil {
			return err
		}
		var fields []string
		if json.Unmarshal(b, &fields) != nil {
			return ErrUnavailable
		}
		if overlap(fields, actionFields(a)) && (state == "running" || state == "needs_review" || (direction == "undo" && order > a.Order)) {
			return ErrConflict
		}
	}
	return rows.Err()
}

func (s *Store) resource(j Job, a Action) (Resource, error) {
	r, ok := s.resources[j.AgentID+"/"+a.ResourceID]
	if !ok || binding(r) != a.Binding {
		return r, ErrPermission
	}
	return r, nil
}

func (s *Store) Resources(agentID string) []Resource {
	out := []Resource{}
	for _, r := range s.resources {
		if r.AgentID == agentID {
			r.Fields = append([]string{}, r.Fields...)
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b Resource) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func (s *Store) Get(ctx context.Context, owner, agentID, id string) (Job, error) {
	if err := s.enter(ctx); err != nil {
		return Job{}, err
	}
	defer s.leave()
	j, err := s.get(ctx, owner, agentID, id)
	return j.Public(), err
}

func (s *Store) List(ctx context.Context, owner, agentID string) ([]JobSummary, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	defer s.leave()
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM safe_undo_jobs WHERE owner=? AND agent_id=? ORDER BY updated_at DESC,id DESC LIMIT 100`, owner, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JobSummary{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		j, err := decodeJob(b)
		if err != nil {
			return nil, err
		}
		status := j.Status
		if status == "running" {
			status = "needs_review"
		}
		out = append(out, JobSummary{ID: j.ID, AgentID: j.AgentID, Title: j.Title, Status: status, UpdatedAt: j.UpdatedAt, ActionCount: len(j.Actions)})
	}
	return out, rows.Err()
}
