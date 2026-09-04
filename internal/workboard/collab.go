package workboard

// Collaboration primitives (Story 14): owner, priority, tags, due date on
// tasks; comments and reviewer notes per task. Local-first and deliberately
// small — no users table, authors are free-text labels.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Priorities (board order: most urgent last so sorting asc reads naturally).
const (
	PriorityLow    = "low"
	PriorityNormal = "normal"
	PriorityHigh   = "high"
	PriorityUrgent = "urgent"
)

// Priorities lists valid priorities.
var Priorities = []string{PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent}

// ValidPriority reports whether p is a recognised priority.
func ValidPriority(p string) bool {
	switch p {
	case PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent:
		return true
	}
	return false
}

// Comment kinds.
const (
	CommentKindComment = "comment"
	CommentKindReview  = "review" // reviewer note
)

// Comment is one note on a task.
type Comment struct {
	ID        int64     `json:"id"`
	TaskID    int64     `json:"task_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

const commentsSchema = `
CREATE TABLE IF NOT EXISTS workboard_comments (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id    INTEGER NOT NULL,
    author     TEXT NOT NULL DEFAULT 'user',
    body       TEXT NOT NULL,
    kind       TEXT NOT NULL DEFAULT 'comment',
    created_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_wbc_task ON workboard_comments(task_id);
`

// taskMigrations are idempotent column additions for pre-Story-14 databases.
// SQLite has no ADD COLUMN IF NOT EXISTS; duplicate-column errors are
// expected and ignored.
var taskMigrations = []string{
	`ALTER TABLE workboard_tasks ADD COLUMN owner TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE workboard_tasks ADD COLUMN priority TEXT NOT NULL DEFAULT 'normal'`,
	`ALTER TABLE workboard_tasks ADD COLUMN tags TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE workboard_tasks ADD COLUMN due_at DATETIME`,
}

func migrateTaskColumns(db *sql.DB) error {
	for _, m := range taskMigrations {
		if _, err := db.Exec(m); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("workboard: migration %q: %w", m, err)
		}
	}
	return nil
}

// normalizeTags trims, lowercases, drops empties, and dedupes preserving
// first-seen order.
func normalizeTags(tags []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

func tagsToCSV(tags []string) string { return strings.Join(normalizeTags(tags), ",") }
func csvToTags(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return []string{}
	}
	return normalizeTags(strings.Split(csv, ","))
}

// AddComment appends a comment (or reviewer note) to a task.
func (s *Store) AddComment(ctx context.Context, taskID int64, c Comment) (Comment, error) {
	c.Body = strings.TrimSpace(c.Body)
	if c.Body == "" {
		return Comment{}, fmt.Errorf("%w: comment body is required", ErrInvalid)
	}
	if c.Kind == "" {
		c.Kind = CommentKindComment
	}
	if c.Kind != CommentKindComment && c.Kind != CommentKindReview {
		return Comment{}, fmt.Errorf("%w: unknown comment kind %q", ErrInvalid, c.Kind)
	}
	if strings.TrimSpace(c.Author) == "" {
		c.Author = "user"
	}
	if _, err := s.Get(ctx, taskID); err != nil {
		return Comment{}, err // ErrNotFound for missing task
	}
	c.TaskID = taskID
	c.CreatedAt = time.Now().UTC().Truncate(time.Second)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO workboard_comments (task_id, author, body, kind, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		c.TaskID, c.Author, c.Body, c.Kind, c.CreatedAt.Format(timeLayout))
	if err != nil {
		return Comment{}, err
	}
	c.ID, err = res.LastInsertId()
	if err != nil {
		return Comment{}, err
	}
	return c, nil
}

// ListComments returns a task's comments oldest-first (conversation order).
func (s *Store) ListComments(ctx context.Context, taskID int64) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, author, body, kind, created_at
		 FROM workboard_comments WHERE task_id = ? ORDER BY created_at ASC, id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.TaskID, &c.Author, &c.Body, &c.Kind, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.CreatedAt = c.CreatedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteComment removes one comment, or returns ErrNotFound.
func (s *Store) DeleteComment(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workboard_comments WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
