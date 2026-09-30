package missions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/soulacy/soulacy/internal/sqlitex"
)

const (
	StatusActive    = "active"
	StatusPaused    = "paused"
	StatusBlocked   = "blocked"
	StatusCompleted = "completed"
	StatusCancelled = "cancelled"
)

var ErrNotFound = errors.New("mission not found")

type Mission struct {
	ID             string         `json:"id"`
	WorkspaceID    string         `json:"workspace_id"`
	OwnerSubject   string         `json:"owner_subject,omitempty"`
	Title          string         `json:"title"`
	Objective      string         `json:"objective"`
	FinishLine     string         `json:"finish_line"`
	Cron           string         `json:"cron,omitempty"`
	At             string         `json:"at,omitempty"`
	Channel        string         `json:"channel,omitempty"`
	To             string         `json:"to,omitempty"`
	MonitorID      string         `json:"monitor_id,omitempty"`
	Status         string         `json:"status"`
	Progress       string         `json:"progress,omitempty"`
	NextAction     string         `json:"next_action,omitempty"`
	Blocker        string         `json:"blocker,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	LastProgressAt *time.Time     `json:"last_progress_at,omitempty"`
	ExecutionPlan  *ExecutionPlan `json:"execution_plan,omitempty"`
}

type Plan struct {
	Title             string   `json:"title"`
	Objective         string   `json:"objective"`
	FinishLine        string   `json:"finish_line"`
	SuggestedCadences []string `json:"suggested_cadences"`
	NeedsSchedule     bool     `json:"needs_schedule"`
	NeedsDelivery     bool     `json:"needs_delivery"`
	Guardrails        []string `json:"guardrails"`
}

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS genie_missions (
  id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  owner_subject TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL,
  objective TEXT NOT NULL,
  finish_line TEXT NOT NULL,
  cron TEXT NOT NULL DEFAULT '',
  run_at TEXT NOT NULL DEFAULT '',
  channel TEXT NOT NULL DEFAULT '',
  destination TEXT NOT NULL DEFAULT '',
  monitor_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  progress TEXT NOT NULL DEFAULT '',
  next_action TEXT NOT NULL DEFAULT '',
  blocker TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL,
  updated_at TIMESTAMP NOT NULL,
  last_progress_at TIMESTAMP,
  PRIMARY KEY (workspace_id, owner_subject, id)
);
CREATE INDEX IF NOT EXISTS genie_missions_updated
  ON genie_missions(workspace_id, owner_subject, updated_at DESC);
CREATE TABLE IF NOT EXISTS genie_mission_execution_plans (
  workspace_id TEXT NOT NULL,
  owner_subject TEXT NOT NULL DEFAULT '',
  mission_id TEXT NOT NULL,
  plan_json TEXT NOT NULL,
  updated_at TIMESTAMP NOT NULL,
  PRIMARY KEY (workspace_id, owner_subject, mission_id),
  FOREIGN KEY (workspace_id, owner_subject, mission_id)
    REFERENCES genie_missions(workspace_id, owner_subject, id) ON DELETE CASCADE
);
`

func OpenStore(path string) (*Store, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("missions: open: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("missions: schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func BuildPlan(objective string) (Plan, error) {
	objective = strings.TrimSpace(objective)
	if len(objective) < 8 {
		return Plan{}, errors.New("describe the ongoing goal in a little more detail")
	}
	if len(objective) > 2000 {
		return Plan{}, errors.New("mission objective must be 2000 characters or fewer")
	}
	words := strings.Fields(objective)
	if len(words) > 7 {
		words = words[:7]
	}
	title := strings.Trim(strings.Join(words, " "), " .,:;!?\t\n")
	if len(title) > 80 {
		title = strings.TrimSpace(title[:80])
	}
	return Plan{
		Title: title, Objective: objective,
		FinishLine:        "Deliver a clear, evidence-backed result and state any unresolved blocker.",
		SuggestedCadences: []string{"Every morning", "Every weekday", "Weekly", "One time"},
		NeedsSchedule:     true, NeedsDelivery: false,
		Guardrails: []string{
			"The mission does not grant new permissions.",
			"Risky actions still require the normal approval.",
			"Soulacy stays quiet when there is no meaningful update.",
			"You can pause, resume, complete, or cancel the mission at any time.",
		},
	}, nil
}

func Normalize(in Mission) (Mission, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.Objective = strings.TrimSpace(in.Objective)
	in.FinishLine = strings.TrimSpace(in.FinishLine)
	in.Cron = strings.TrimSpace(in.Cron)
	in.At = strings.TrimSpace(in.At)
	in.Channel = strings.TrimSpace(in.Channel)
	in.To = strings.TrimSpace(in.To)
	if in.Title == "" || len(in.Title) > 120 || strings.ContainsAny(in.Title, "\r\n\x00") {
		return Mission{}, errors.New("mission title must be a single line of 120 characters or fewer")
	}
	if in.Objective == "" || len(in.Objective) > 2000 {
		return Mission{}, errors.New("mission objective must be between 1 and 2000 characters")
	}
	if in.FinishLine == "" || len(in.FinishLine) > 1000 {
		return Mission{}, errors.New("mission finish line must be between 1 and 1000 characters")
	}
	if (in.Cron == "") == (in.At == "") {
		return Mission{}, errors.New("mission requires exactly one recurring cron schedule or one-time timestamp")
	}
	if (in.Channel == "") != (in.To == "") {
		return Mission{}, errors.New("delivery channel and destination must be supplied together")
	}
	if in.ID == "" {
		in.ID = "mission_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if in.Status == "" {
		in.Status = StatusActive
	}
	if !ValidStatus(in.Status) {
		return Mission{}, fmt.Errorf("invalid mission status %q", in.Status)
	}
	return in, nil
}

func ValidStatus(status string) bool {
	switch status {
	case StatusActive, StatusPaused, StatusBlocked, StatusCompleted, StatusCancelled:
		return true
	default:
		return false
	}
}

func (s *Store) Create(ctx context.Context, in Mission) (Mission, error) {
	item, err := Normalize(in)
	if err != nil {
		return Mission{}, err
	}
	now := time.Now().UTC()
	item.CreatedAt, item.UpdatedAt = now, now
	_, err = s.db.ExecContext(ctx, `INSERT INTO genie_missions
      (id,workspace_id,owner_subject,title,objective,finish_line,cron,run_at,channel,destination,monitor_id,status,progress,next_action,blocker,created_at,updated_at,last_progress_at)
      VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.WorkspaceID, item.OwnerSubject,
		item.Title, item.Objective, item.FinishLine, item.Cron, item.At, item.Channel, item.To,
		item.MonitorID, item.Status, item.Progress, item.NextAction, item.Blocker, now, now, item.LastProgressAt)
	if err != nil {
		return Mission{}, fmt.Errorf("missions: create: %w", err)
	}
	return item, nil
}

func (s *Store) List(ctx context.Context, workspaceID, subject string) ([]Mission, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,workspace_id,owner_subject,title,objective,finish_line,cron,run_at,channel,destination,monitor_id,status,progress,next_action,blocker,created_at,updated_at,last_progress_at
      FROM genie_missions WHERE workspace_id=? AND owner_subject=? ORDER BY updated_at DESC`, workspaceID, subject)
	if err != nil {
		return nil, err
	}
	out := []Mission{}
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.loadExecutionPlan(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) Get(ctx context.Context, workspaceID, subject, id string) (Mission, error) {
	item, err := scan(s.db.QueryRowContext(ctx, `SELECT id,workspace_id,owner_subject,title,objective,finish_line,cron,run_at,channel,destination,monitor_id,status,progress,next_action,blocker,created_at,updated_at,last_progress_at
      FROM genie_missions WHERE workspace_id=? AND owner_subject=? AND id=?`, workspaceID, subject, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return Mission{}, ErrNotFound
	}
	if err != nil {
		return item, err
	}
	if err := s.loadExecutionPlan(ctx, &item); err != nil {
		return Mission{}, err
	}
	return item, nil
}

func (s *Store) SaveExecutionPlan(ctx context.Context, workspaceID, subject, id string, plan ExecutionPlan) (Mission, error) {
	if _, err := s.Get(ctx, workspaceID, subject, id); err != nil {
		return Mission{}, err
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return Mission{}, fmt.Errorf("missions: encode execution plan: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO genie_mission_execution_plans
      (workspace_id,owner_subject,mission_id,plan_json,updated_at) VALUES(?,?,?,?,?)
      ON CONFLICT(workspace_id,owner_subject,mission_id) DO UPDATE SET
      plan_json=excluded.plan_json,updated_at=excluded.updated_at`, workspaceID, subject, id, string(encoded), time.Now().UTC())
	if err != nil {
		return Mission{}, fmt.Errorf("missions: save execution plan: %w", err)
	}
	return s.Get(ctx, workspaceID, subject, id)
}

func (s *Store) loadExecutionPlan(ctx context.Context, item *Mission) error {
	if item == nil {
		return nil
	}
	var encoded string
	err := s.db.QueryRowContext(ctx, `SELECT plan_json FROM genie_mission_execution_plans
      WHERE workspace_id=? AND owner_subject=? AND mission_id=?`, item.WorkspaceID, item.OwnerSubject, item.ID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var plan ExecutionPlan
	if err := json.Unmarshal([]byte(encoded), &plan); err != nil {
		return fmt.Errorf("missions: decode execution plan: %w", err)
	}
	item.ExecutionPlan = &plan
	return nil
}

func (s *Store) UpdateState(ctx context.Context, workspaceID, subject, id, status, progress, nextAction, blocker string) (Mission, error) {
	if !ValidStatus(status) {
		return Mission{}, fmt.Errorf("invalid mission status %q", status)
	}
	progress = strings.TrimSpace(progress)
	nextAction = strings.TrimSpace(nextAction)
	blocker = strings.TrimSpace(blocker)
	if len(progress) > 4000 || len(nextAction) > 1000 || len(blocker) > 2000 {
		return Mission{}, errors.New("mission update is too long")
	}
	now := time.Now().UTC()
	var progressAt any
	if progress != "" {
		progressAt = now
	}
	result, err := s.db.ExecContext(ctx, `UPDATE genie_missions SET status=?,progress=?,next_action=?,blocker=?,updated_at=?,last_progress_at=COALESCE(?,last_progress_at)
      WHERE workspace_id=? AND owner_subject=? AND id=?`, status, progress, nextAction, blocker, now, progressAt, workspaceID, subject, id)
	if err != nil {
		return Mission{}, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return Mission{}, ErrNotFound
	}
	return s.Get(ctx, workspaceID, subject, id)
}

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Mission, error) {
	var item Mission
	err := row.Scan(&item.ID, &item.WorkspaceID, &item.OwnerSubject, &item.Title, &item.Objective,
		&item.FinishLine, &item.Cron, &item.At, &item.Channel, &item.To, &item.MonitorID,
		&item.Status, &item.Progress, &item.NextAction, &item.Blocker, &item.CreatedAt, &item.UpdatedAt, &item.LastProgressAt)
	return item, err
}
