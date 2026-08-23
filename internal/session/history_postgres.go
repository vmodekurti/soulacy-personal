package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soulacy/soulacy/internal/workspacepurge"
	"github.com/soulacy/soulacy/internal/wsroot"
)

const postgresHistorySchema = `
CREATE TABLE IF NOT EXISTS conversation_history (
 id BIGSERIAL PRIMARY KEY,
 workspace_id TEXT NOT NULL,
 subject TEXT NOT NULL DEFAULT '',
 session_id TEXT NOT NULL,
 agent_id TEXT NOT NULL,
 role TEXT NOT NULL,
 content TEXT NOT NULL,
 tokens INTEGER NOT NULL DEFAULT 0,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_ch_session ON conversation_history(workspace_id,session_id,id);
CREATE INDEX IF NOT EXISTS idx_ch_agent ON conversation_history(workspace_id,subject,agent_id,id DESC);
CREATE INDEX IF NOT EXISTS idx_ch_created ON conversation_history(created_at);`

// PostgresHistoryStore is the shared conversation store used by Team/Scale.
// Every query carries workspace_id; subject is additionally applied to
// cross-session reads outside the single-user personal workspace.
type PostgresHistoryStore struct{ pool *pgxpool.Pool }

var _ HistoryStore = (*PostgresHistoryStore)(nil)
var _ ForkingHistoryStore = (*PostgresHistoryStore)(nil)
var _ ConversationLocker = (*PostgresHistoryStore)(nil)

func NewPostgresHistoryStore(ctx context.Context, dsn string) (*PostgresHistoryStore, error) {
	pool, err := pgxpool.New(ctx, strings.TrimSpace(dsn))
	if err != nil {
		return nil, fmt.Errorf("session/history postgres: connect: %w", err)
	}
	if _, err = pool.Exec(ctx, postgresHistorySchema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("session/history postgres: schema: %w", err)
	}
	return &PostgresHistoryStore{pool: pool}, nil
}

func (s *PostgresHistoryStore) LockConversation(ctx context.Context, workspaceID, sessionID string) (func(), error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("session/history postgres: acquire conversation lock connection: %w", err)
	}
	key := workspaceID + "\x00" + sessionID
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, key); err != nil {
		conn.Release()
		return nil, fmt.Errorf("session/history postgres: lock conversation: %w", err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, unlockErr := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key); unlockErr != nil {
				// A pooled connection carrying a session advisory lock must never be
				// returned to the pool. Closing the hijacked connection releases it.
				raw := conn.Hijack()
				_ = raw.Close(unlockCtx)
				return
			}
			conn.Release()
		})
	}, nil
}

func (s *PostgresHistoryStore) Append(ctx context.Context, e ConversationEntry) error {
	workspaceID, err := requireWorkspace(e.WorkspaceID)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO conversation_history
 (workspace_id,subject,session_id,agent_id,role,content,tokens,created_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, workspaceID, strings.TrimSpace(e.Subject), e.SessionID,
		e.AgentID, e.Role, e.Content, e.Tokens, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("session/history postgres: append: %w", err)
	}
	return nil
}

const postgresHistoryColumns = `id,workspace_id,subject,session_id,agent_id,role,content,tokens,created_at`

func (s *PostgresHistoryStore) Load(ctx context.Context, workspaceID, sessionID string, limit int) ([]ConversationEntry, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	var rows pgx.Rows
	if limit <= 0 {
		rows, err = s.pool.Query(ctx, `SELECT `+postgresHistoryColumns+` FROM conversation_history
 WHERE workspace_id=$1 AND session_id=$2 ORDER BY id`, workspaceID, sessionID)
	} else {
		rows, err = s.pool.Query(ctx, `SELECT `+postgresHistoryColumns+` FROM
 (SELECT `+postgresHistoryColumns+` FROM conversation_history WHERE workspace_id=$1 AND session_id=$2 ORDER BY id DESC LIMIT $3) recent
 ORDER BY id`, workspaceID, sessionID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("session/history postgres: load: %w", err)
	}
	defer rows.Close()
	return scanPostgresHistory(rows)
}

func (s *PostgresHistoryStore) LoadForAgent(ctx context.Context, workspaceID, subject, agentID string, limit int) ([]ConversationEntry, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 1000
	}
	args := []any{workspaceID, agentID}
	where := `workspace_id=$1 AND agent_id=$2`
	if workspaceID != wsroot.PersonalWorkspaceID {
		args = append(args, strings.TrimSpace(subject))
		where += fmt.Sprintf(" AND subject=$%d", len(args))
	}
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, `SELECT `+postgresHistoryColumns+` FROM conversation_history WHERE `+where+
		fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("session/history postgres: load agent: %w", err)
	}
	defer rows.Close()
	return scanPostgresHistory(rows)
}

func scanPostgresHistory(rows pgx.Rows) ([]ConversationEntry, error) {
	out := []ConversationEntry{}
	for rows.Next() {
		var entry ConversationEntry
		if err := rows.Scan(&entry.ID, &entry.WorkspaceID, &entry.Subject, &entry.SessionID, &entry.AgentID,
			&entry.Role, &entry.Content, &entry.Tokens, &entry.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

func (s *PostgresHistoryStore) Search(ctx context.Context, workspaceID, subject, agentID, query string, limit int) ([]SearchHit, error) {
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
	args := []any{workspaceID}
	where := []string{"workspace_id=$1"}
	if workspaceID != wsroot.PersonalWorkspaceID {
		args = append(args, strings.TrimSpace(subject))
		where = append(where, fmt.Sprintf("subject=$%d", len(args)))
	}
	if agentID = strings.TrimSpace(agentID); agentID != "" {
		args = append(args, agentID)
		where = append(where, fmt.Sprintf("agent_id=$%d", len(args)))
	}
	for _, term := range terms {
		args = append(args, "%"+strings.ToLower(term)+"%")
		where = append(where, fmt.Sprintf("LOWER(content) LIKE $%d", len(args)))
	}
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, `SELECT `+postgresHistoryColumns+` FROM conversation_history WHERE `+
		strings.Join(where, " AND ")+fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("session/history postgres: search: %w", err)
	}
	defer rows.Close()
	entries, err := scanPostgresHistory(rows)
	if err != nil {
		return nil, err
	}
	out := make([]SearchHit, 0, len(entries))
	for _, entry := range entries {
		out = append(out, SearchHit{ConversationEntry: entry, Snippet: makeSnippet(entry.Content, terms, 220)})
	}
	return out, nil
}

func (s *PostgresHistoryStore) Fork(ctx context.Context, workspaceID, source, target string, upto int64) (int, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return 0, err
	}
	if source == target || strings.TrimSpace(target) == "" {
		return 0, fmt.Errorf("session/history: invalid fork target %q", target)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var exists int
	if err = tx.QueryRow(ctx, `SELECT COUNT(*) FROM conversation_history WHERE workspace_id=$1 AND session_id=$2`, workspaceID, target).Scan(&exists); err != nil {
		return 0, err
	}
	if exists > 0 {
		return 0, fmt.Errorf("session/history: fork target %q already has %d entries", target, exists)
	}
	rows, err := tx.Query(ctx, `SELECT subject,agent_id,role,content,tokens FROM conversation_history
 WHERE workspace_id=$1 AND session_id=$2 AND id<=$3 ORDER BY id`, workspaceID, source, upto)
	if err != nil {
		return 0, err
	}
	type turn struct {
		subject, agent, role, content string
		tokens                        int
	}
	var turns []turn
	for rows.Next() {
		var item turn
		if err = rows.Scan(&item.subject, &item.agent, &item.role, &item.content, &item.tokens); err != nil {
			rows.Close()
			return 0, err
		}
		turns = append(turns, item)
	}
	rows.Close()
	if len(turns) == 0 {
		return 0, nil
	}
	for i, item := range turns {
		if _, err = tx.Exec(ctx, `INSERT INTO conversation_history
 (workspace_id,subject,session_id,agent_id,role,content,tokens,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
			workspaceID, item.subject, target, item.agent, item.role, item.content, item.tokens, time.Now().UTC().Add(time.Duration(i)*time.Microsecond)); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(turns), nil
}

func (s *PostgresHistoryStore) ExportWorkspaceJSONL(ctx context.Context, workspaceID string, w io.Writer) (int64, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+postgresHistoryColumns+` FROM conversation_history WHERE workspace_id=$1 ORDER BY id`, workspaceID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	encoder := json.NewEncoder(w)
	var count int64
	for rows.Next() {
		var entry ConversationEntry
		if err = rows.Scan(&entry.ID, &entry.WorkspaceID, &entry.Subject, &entry.SessionID, &entry.AgentID,
			&entry.Role, &entry.Content, &entry.Tokens, &entry.CreatedAt); err != nil {
			return count, err
		}
		if err = encoder.Encode(entry); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}

func (s *PostgresHistoryStore) PurgeWorkspace(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
	workspaceID, err := requireWorkspace(workspaceID)
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	result, err := s.pool.Exec(ctx, `DELETE FROM conversation_history WHERE workspace_id=$1`, workspaceID)
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	return workspacepurge.Removed{Rows: result.RowsAffected(), Note: "shared conversation history"}, nil
}

func (s *PostgresHistoryStore) Prune(ctx context.Context, olderThan time.Duration) (int64, error) {
	if olderThan <= 0 {
		return 0, nil
	}
	result, err := s.pool.Exec(ctx, `DELETE FROM conversation_history WHERE created_at<$1`, time.Now().UTC().Add(-olderThan))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func (s *PostgresHistoryStore) Close() error {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
	return nil
}
