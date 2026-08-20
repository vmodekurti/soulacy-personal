// history.go — SQLite-backed conversation history store for session package.
//
// ConversationEntry records one turn (user / assistant / system) in a
// multi-turn conversation.  Entries are keyed by session_id and agent_id,
// ordered by insertion id, and pruned automatically after 30 days.
package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"errors"
	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/internal/workspacepurge"
	"github.com/soulacy/soulacy/internal/wsroot"
	"sync"
)

// ConversationEntry is one turn in a conversation.
type ConversationEntry struct {
	ID int64 `json:"id"`
	// WorkspaceID and Subject are the tenant and the person the conversation
	// belongs to. Conversation history is the most revealing store in the
	// system — it is the message content itself — and its ownership class is
	// user-private, so both are required to read across sessions.
	WorkspaceID string    `json:"workspace_id,omitempty"`
	Subject     string    `json:"subject,omitempty"`
	SessionID   string    `json:"session_id"`
	AgentID     string    `json:"agent_id"`
	Role        string    `json:"role"` // "user" | "assistant" | "system"
	Content     string    `json:"content"`
	Tokens      int       `json:"tokens"` // 0 if unknown
	CreatedAt   time.Time `json:"created_at"`
}

// SearchHit is one matched conversation-history entry with a compact snippet
// for UI search results.
type SearchHit struct {
	ConversationEntry
	Snippet string `json:"snippet"`
}

// HistoryStore is the interface for persisting and retrieving conversation
// history entries.
type HistoryStore interface {
	// Append adds a conversation turn. The entry must carry a workspace.
	Append(ctx context.Context, e ConversationEntry) error

	// Load returns the last `limit` entries for one workspace's sessionID,
	// oldest first. If limit <= 0, returns all entries.
	//
	// It takes no subject: a session is addressed by an ID the caller already
	// had to be authorized for, and the gateway gates that with
	// session.Ownership (private by default, widened only inside a workspace).
	// A second, weaker check here would invite callers to rely on it instead.
	Load(ctx context.Context, workspaceID, sessionID string, limit int) ([]ConversationEntry, error)

	// LoadForAgent returns the last `limit` entries across all sessions for
	// agentID, newest first. It reads across sessions, so unlike Load it
	// carries the subject: this is where one workspace member would otherwise
	// read a colleague's conversations.
	LoadForAgent(ctx context.Context, workspaceID, subject, agentID string, limit int) ([]ConversationEntry, error)

	// Prune deletes entries older than the given duration. Returns count deleted.
	Prune(ctx context.Context, olderThan time.Duration) (int64, error)

	// Close releases resources.
	Close() error
}

// ---------------------------------------------------------------------------
// NoopHistoryStore — all no-ops for degraded / testing mode.
// ---------------------------------------------------------------------------

// NoopHistoryStore implements HistoryStore with no-op methods.
type NoopHistoryStore struct{}

// Append is a no-op.
func (NoopHistoryStore) Append(_ context.Context, _ ConversationEntry) error { return nil }

// Load returns nil, nil.
func (NoopHistoryStore) Load(_ context.Context, _, _ string, _ int) ([]ConversationEntry, error) {
	return nil, nil
}

// LoadForAgent returns nil, nil.
func (NoopHistoryStore) LoadForAgent(_ context.Context, _, _, _ string, _ int) ([]ConversationEntry, error) {
	return nil, nil
}

// Prune is a no-op.
func (NoopHistoryStore) Prune(_ context.Context, _ time.Duration) (int64, error) { return 0, nil }

// Close is a no-op.
func (NoopHistoryStore) Close() error { return nil }

// ---------------------------------------------------------------------------
// SQLiteHistoryStore — full SQLite-backed implementation.
// ---------------------------------------------------------------------------

const historySchema = `
CREATE TABLE IF NOT EXISTS conversation_history (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id TEXT NOT NULL DEFAULT 'ws_personal',
    subject     TEXT NOT NULL DEFAULT '',
    session_id  TEXT NOT NULL,
    agent_id    TEXT NOT NULL,
    role        TEXT NOT NULL,
    content     TEXT NOT NULL,
    tokens      INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ch_session ON conversation_history(workspace_id, session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_ch_agent   ON conversation_history(workspace_id, subject, agent_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ch_content ON conversation_history(content);
`

// ErrWorkspaceRequired is returned by an operation with no tenant. A
// conversation turn with no owner is one every tenant can search.
var ErrWorkspaceRequired = errors.New("session/history: workspace_id is required")

// historyColumns is the canonical SELECT list matching scanEntries.
const historyColumns = `id, workspace_id, subject, session_id, agent_id, role, content, tokens, created_at`

// SQLiteHistoryStore is the SQLite-backed implementation of HistoryStore.
type SQLiteHistoryStore struct {
	db        *sql.DB
	stopCh    chan struct{}
	closeOnce sync.Once
	retention time.Duration
}

type HistoryOption func(*SQLiteHistoryStore)

// WithHistoryRetention configures automatic conversation deletion. Zero keeps
// history indefinitely; callers that omit the option retain the 30-day default.
func WithHistoryRetention(d time.Duration) HistoryOption {
	return func(s *SQLiteHistoryStore) { s.retention = d }
}

// NewSQLiteHistoryStore opens (or creates) the SQLite database at path,
// applies the conversation_history schema, and starts a background pruning
// goroutine that removes entries older than 30 days every 6 hours.
func NewSQLiteHistoryStore(path string, opts ...HistoryOption) (*SQLiteHistoryStore, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("session/history: open sqlite %s: %w", path, err)
	}

	for _, stmt := range splitStmts(historySchema) {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("session/history: schema migration (%q): %w",
				truncate(stmt, 60), err)
		}
	}

	if err := addHistoryWorkspaceColumns(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	s := &SQLiteHistoryStore{
		db:        db,
		stopCh:    make(chan struct{}),
		retention: 30 * 24 * time.Hour,
	}
	for _, opt := range opts {
		opt(s)
	}

	go s.pruneLoop()

	return s, nil
}

// pruneLoop runs every 6 hours and deletes entries older than 30 days.
// It exits when stopCh is closed.
func (s *SQLiteHistoryStore) pruneLoop() {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			if s.retention <= 0 {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, _ = s.Prune(ctx, s.retention)
			cancel()
		}
	}
}

// Append inserts a new conversation entry with created_at set to now (UTC).
func (s *SQLiteHistoryStore) Append(ctx context.Context, e ConversationEntry) error {
	workspaceID, err := requireWorkspace(e.WorkspaceID)
	if err != nil {
		return err
	}
	createdAt := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO conversation_history
		    (workspace_id, subject, session_id, agent_id, role, content, tokens, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		workspaceID, strings.TrimSpace(e.Subject),
		e.SessionID, e.AgentID, e.Role, e.Content, e.Tokens, createdAt,
	)
	if err != nil {
		return fmt.Errorf("session/history: append: %w", err)
	}
	return nil
}

// Load returns conversation entries for sessionID, oldest first.
// If limit <= 0, all entries are returned.
// If limit > 0, the last `limit` entries are returned in chronological order.
func (s *SQLiteHistoryStore) Load(ctx context.Context, workspaceID, sessionID string, limit int) ([]ConversationEntry, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	var rows *sql.Rows
	if limit <= 0 {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+historyColumns+`
			 FROM conversation_history
			 WHERE workspace_id = ? AND session_id = ?
			 ORDER BY id ASC`,
			workspaceID, sessionID,
		)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+historyColumns+`
			 FROM (
			     SELECT `+historyColumns+`
			     FROM conversation_history
			     WHERE workspace_id = ? AND session_id = ?
			     ORDER BY id DESC
			     LIMIT ?
			 )
			 ORDER BY id ASC`,
			workspaceID, sessionID, limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("session/history: load session %q: %w", sessionID, err)
	}
	defer rows.Close()

	return scanEntries(rows)
}

// LoadForAgent returns the last `limit` entries for agentID across all
// sessions, newest first.  If limit <= 0, it is capped at 1000.
func (s *SQLiteHistoryStore) LoadForAgent(ctx context.Context, workspaceID, subject, agentID string, limit int) ([]ConversationEntry, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 1000
	}
	where := []string{"workspace_id = ?", "agent_id = ?"}
	args := []any{workspaceID, agentID}
	if clause, extra := subjectPredicate(workspaceID, subject); clause != "" {
		where = append(where, clause)
		args = append(args, extra...)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+historyColumns+`
		 FROM conversation_history
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY id DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("session/history: load agent %q: %w", agentID, err)
	}
	defer rows.Close()

	return scanEntries(rows)
}

// ExportWorkspaceJSONL streams every user's conversation turns for a complete
// owner-authorized workspace export. Ordinary history APIs remain user-private;
// this deliberately broader surface is only wired into the workspace lifecycle
// job and still predicates in SQL on the verified workspace.
func (s *SQLiteHistoryStore) ExportWorkspaceJSONL(ctx context.Context, workspaceID string, w io.Writer) (int64, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+historyColumns+`
		FROM conversation_history WHERE workspace_id = ? ORDER BY id ASC`, workspaceID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	encoder := json.NewEncoder(w)
	var count int64
	for rows.Next() {
		var entry ConversationEntry
		if err := rows.Scan(&entry.ID, &entry.WorkspaceID, &entry.Subject, &entry.SessionID,
			&entry.AgentID, &entry.Role, &entry.Content, &entry.Tokens, &entry.CreatedAt); err != nil {
			return count, err
		}
		if err := encoder.Encode(entry); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}

func (s *SQLiteHistoryStore) PurgeWorkspace(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
	return workspacepurge.PurgeCatalogTables(ctx, s.db, "messages", workspaceID)
}

// Search returns recent entries whose content contains all query terms. It is a
// bounded plain-SQL search intended for chat recall; semantic search can layer
// on top later without changing the API shape.
// Search is the read that mattered most here. agentID is optional — blank
// means "every agent" — so without the workspace and subject predicates a
// single query returned matching message content from every conversation in
// the deployment. Both are mandatory and applied before the optional filters.
func (s *SQLiteHistoryStore) Search(ctx context.Context, workspaceID, subject, agentID, query string, limit int) ([]SearchHit, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	terms := searchTerms(query, 5)
	if len(terms) == 0 {
		return []SearchHit{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	where := []string{"workspace_id = ?"}
	args := []any{workspaceID}
	if clause, extra := subjectPredicate(workspaceID, subject); clause != "" {
		where = append(where, clause)
		args = append(args, extra...)
	}
	if strings.TrimSpace(agentID) != "" {
		where = append(where, "agent_id = ?")
		args = append(args, strings.TrimSpace(agentID))
	}
	for _, term := range terms {
		where = append(where, "LOWER(content) LIKE ?")
		args = append(args, "%"+strings.ToLower(term)+"%")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+historyColumns+`
		 FROM conversation_history
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY id DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("session/history: search: %w", err)
	}
	defer rows.Close()

	entries, err := scanEntries(rows)
	if err != nil {
		return nil, err
	}
	out := make([]SearchHit, 0, len(entries))
	for _, e := range entries {
		out = append(out, SearchHit{ConversationEntry: e, Snippet: makeSnippet(e.Content, terms, 220)})
	}
	return out, nil
}

// subjectPredicate returns the SQL fragment and argument that restrict a
// cross-session read to one person — and deliberately returns nothing at all
// in the personal workspace.
//
// The personal workspace has exactly one user; that is what "personal" means.
// Partitioning it by subject buys no isolation, and it would actively lose
// data: every row written before tenancy carries an empty subject, while a
// reader today resolves to whatever local owner ID the deployment assigned. A
// Personal user would upgrade and find their own history had vanished from
// search. Product invariant 7 says they must not notice the storage layer
// became tenant-aware, so here they do not.
//
// In every other workspace the predicate applies in full: that is where one
// member reading a colleague's conversations is a real boundary.
func subjectPredicate(workspaceID, subject string) (string, []any) {
	if workspaceID == wsroot.PersonalWorkspaceID {
		return "", nil
	}
	return "subject = ?", []any{strings.TrimSpace(subject)}
}

// requireWorkspace normalizes a tenant and refuses an absent one.
func requireWorkspace(workspaceID string) (string, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return "", ErrWorkspaceRequired
	}
	return wsroot.Normalize(workspaceID), nil
}

// addHistoryWorkspaceColumns brings a pre-tenant history database up to the
// current schema and assigns its turns to the personal workspace, which is
// what a single-user installation's conversations were.
//
// The backfill runs on every open, not only when the columns are added. A row
// with an empty workspace matches no scoped query, so it is not a leak — it is
// a conversation that silently vanished, which is worse and harder to notice.
func addHistoryWorkspaceColumns(db *sql.DB) error {
	for _, stmt := range []string{
		`ALTER TABLE conversation_history ADD COLUMN workspace_id TEXT NOT NULL DEFAULT 'ws_personal'`,
		`ALTER TABLE conversation_history ADD COLUMN subject TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("session/history: migrate: %w", err)
		}
	}
	if _, err := db.Exec(
		`UPDATE conversation_history SET workspace_id = ? WHERE workspace_id IS NULL OR workspace_id = ''`,
		wsroot.PersonalWorkspaceID); err != nil {
		return fmt.Errorf("session/history: backfill workspace: %w", err)
	}
	return nil
}

// Prune deletes entries older than olderThan across every tenant. Retention is
// a deployment-wide guarantee: a policy that silently applied to one workspace
// only would be worse than none, so this is deliberately unscoped.

func (s *SQLiteHistoryStore) Prune(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan).Format("2006-01-02 15:04:05")
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM conversation_history WHERE created_at < ?`, cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("session/history: prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("session/history: prune rows affected: %w", err)
	}
	return n, nil
}

// Close stops the background pruner and closes the database connection.
//
// Safe to call more than once: closing an already-closed channel panics, and a
// store reached through two shutdown paths — an explicit Close and a deferred
// one — would take the process down rather than return an error.
func (s *SQLiteHistoryStore) Close() error {
	s.closeOnce.Do(func() { close(s.stopCh) })
	return s.db.Close()
}

// ---------------------------------------------------------------------------
// internal helpers
// ---------------------------------------------------------------------------

// scanEntries scans all rows into a []ConversationEntry slice.
func scanEntries(rows *sql.Rows) ([]ConversationEntry, error) {
	var out []ConversationEntry
	for rows.Next() {
		var e ConversationEntry
		// Scan created_at directly into time.Time so the mattn/go-sqlite3 driver
		// handles the format conversion — the same fix as internal/auth/apikeys.
		// Scanning into string and parsing "2006-01-02 15:04:05" breaks because
		// the driver reformats DATETIME columns as RFC3339 when the scan target
		// is a string.
		if err := rows.Scan(
			&e.ID, &e.WorkspaceID, &e.Subject, &e.SessionID, &e.AgentID, &e.Role, &e.Content, &e.Tokens, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("session/history: scan row: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func searchTerms(query string, max int) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, term := range strings.Fields(query) {
		term = strings.Trim(term, `"'.,;:!?()[]{}<>`)
		if len(term) < 2 || seen[term] {
			continue
		}
		seen[term] = true
		out = append(out, term)
		if len(out) >= max {
			break
		}
	}
	return out
}

func makeSnippet(content string, terms []string, max int) string {
	content = strings.Join(strings.Fields(content), " ")
	if len(content) <= max {
		return content
	}
	lower := strings.ToLower(content)
	pos := -1
	for _, term := range terms {
		if idx := strings.Index(lower, strings.ToLower(term)); idx >= 0 && (pos == -1 || idx < pos) {
			pos = idx
		}
	}
	if pos < 0 {
		pos = 0
	}
	start := pos - max/3
	if start < 0 {
		start = 0
	}
	if start+max > len(content) {
		start = len(content) - max
	}
	if start < 0 {
		start = 0
	}
	snippet := strings.TrimSpace(content[start : start+max])
	if start > 0 {
		snippet = "..." + snippet
	}
	if start+max < len(content) {
		snippet += "..."
	}
	return snippet
}
