package autopilot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	maxGoalTasks          = 64
	maxGoalTitleBytes     = 256
	maxGoalObjectiveBytes = 16 * 1024
	maxGoalPromptBytes    = 64 * 1024
	maxTaskResultBytes    = 16 * 1024
	maxTaskErrorBytes     = 4 * 1024
	maxGoalDurationMS     = int64((30 * 24 * time.Hour) / time.Millisecond)
)

func validGoalStatus(status GoalStatus) bool {
	switch status {
	case GoalStatusDraft, GoalRunning, GoalSucceeded, GoalFailed, GoalCancelled:
		return true
	default:
		return false
	}
}

func validateBudget(budget ExecutionBudget, label string) error {
	if budget.MaxCostUSD < 0 || budget.MaxCostUSD > maxMicrosCostUSD ||
		math.IsNaN(budget.MaxCostUSD) || math.IsInf(budget.MaxCostUSD, 0) {
		return fmt.Errorf("%w: %s max_cost_usd must be finite, non-negative, and representable in micro-dollars", ErrInvalid, label)
	}
	if budget.MaxDurationMS <= 0 || budget.MaxDurationMS > maxGoalDurationMS {
		return fmt.Errorf("%w: %s max_duration_ms must be positive and no more than 30 days", ErrInvalid, label)
	}
	return nil
}

// CreateGoal validates and persists a bounded multi-agent DAG atomically.
func (s *Store) CreateGoal(ctx context.Context, draft GoalDraft) (Goal, error) {
	draft.Subject = strings.TrimSpace(draft.Subject)
	draft.Title = strings.TrimSpace(draft.Title)
	draft.Objective = strings.TrimSpace(draft.Objective)
	if !validSubject(draft.Subject) || draft.Title == "" || draft.Objective == "" {
		return Goal{}, fmt.Errorf("%w: subject, title, and objective are required", ErrInvalid)
	}
	if len(draft.Title) > maxGoalTitleBytes || !utf8.ValidString(draft.Title) {
		return Goal{}, fmt.Errorf("%w: goal title must be UTF-8 and at most %d bytes", ErrInvalid, maxGoalTitleBytes)
	}
	if len(draft.Objective) > maxGoalObjectiveBytes || !utf8.ValidString(draft.Objective) {
		return Goal{}, fmt.Errorf("%w: goal objective must be UTF-8 and at most %d bytes", ErrInvalid, maxGoalObjectiveBytes)
	}
	if err := validateBudget(draft.Budget, "goal budget"); err != nil {
		return Goal{}, err
	}
	if len(draft.Tasks) == 0 || len(draft.Tasks) > maxGoalTasks {
		return Goal{}, fmt.Errorf("%w: a goal requires 1-%d tasks", ErrInvalid, maxGoalTasks)
	}

	tasks := make([]GoalTaskDraft, len(draft.Tasks))
	copy(tasks, draft.Tasks)
	ids := make(map[string]int, len(tasks))
	for i := range tasks {
		tasks[i].ID = strings.TrimSpace(tasks[i].ID)
		if tasks[i].ID == "" {
			tasks[i].ID = uuid.NewString()
		}
		tasks[i].Title = strings.TrimSpace(tasks[i].Title)
		tasks[i].AgentID = strings.TrimSpace(tasks[i].AgentID)
		tasks[i].DependsOn = append([]string(nil), tasks[i].DependsOn...)
		for j := range tasks[i].DependsOn {
			tasks[i].DependsOn[j] = strings.TrimSpace(tasks[i].DependsOn[j])
		}
		if tasks[i].Title == "" || strings.TrimSpace(tasks[i].Prompt) == "" || tasks[i].AgentID == "" {
			return Goal{}, fmt.Errorf("%w: task %d requires title, prompt, and agent_id", ErrInvalid, i)
		}
		if len(tasks[i].ID) > 512 || len(tasks[i].AgentID) > 512 ||
			len(tasks[i].Title) > maxGoalTitleBytes || len(tasks[i].Prompt) > maxGoalPromptBytes ||
			!utf8.ValidString(tasks[i].Title) || !utf8.ValidString(tasks[i].Prompt) {
			return Goal{}, fmt.Errorf("%w: task %q title or prompt is too large", ErrInvalid, tasks[i].ID)
		}
		if _, exists := ids[tasks[i].ID]; exists {
			return Goal{}, fmt.Errorf("%w: duplicate task id %q", ErrInvalid, tasks[i].ID)
		}
		ids[tasks[i].ID] = i
		if err := validateBudget(tasks[i].Budget, "task "+tasks[i].ID+" budget"); err != nil {
			return Goal{}, err
		}
		if tasks[i].Budget.MaxCostUSD > draft.Budget.MaxCostUSD || tasks[i].Budget.MaxDurationMS > draft.Budget.MaxDurationMS {
			return Goal{}, fmt.Errorf("%w: task %q budget exceeds the goal budget", ErrInvalid, tasks[i].ID)
		}
	}
	if err := validateGoalDAG(tasks, ids); err != nil {
		return Goal{}, err
	}

	id := strings.TrimSpace(draft.ID)
	if id == "" {
		id = uuid.NewString()
	}
	if len(id) > 512 {
		return Goal{}, fmt.Errorf("%w: goal id is too large", ErrInvalid)
	}
	now := s.timestamp()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Goal{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO autopilot_goals
		(id, subject, title, objective, status, max_cost_usd, max_duration_ms, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, draft.Subject, draft.Title,
		draft.Objective, GoalStatusDraft, draft.Budget.MaxCostUSD,
		draft.Budget.MaxDurationMS, timeString(now), timeString(now))
	if err != nil {
		if isConstraintError(err) {
			return Goal{}, fmt.Errorf("%w: goal id %q already exists", ErrConflict, id)
		}
		return Goal{}, err
	}
	for _, task := range tasks {
		deps, err := json.Marshal(task.DependsOn)
		if err != nil {
			return Goal{}, err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO autopilot_goal_tasks
			(id, goal_id, title, prompt, agent_id, status, depends_on_json, max_cost_usd, max_duration_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, task.ID, id, task.Title, task.Prompt,
			task.AgentID, GoalTaskPending, deps, task.Budget.MaxCostUSD, task.Budget.MaxDurationMS)
		if err != nil {
			return Goal{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Goal{}, err
	}
	return s.GetGoal(ctx, draft.Subject, id)
}

func validateGoalDAG(tasks []GoalTaskDraft, ids map[string]int) error {
	visiting := make(map[string]bool, len(tasks))
	visited := make(map[string]bool, len(tasks))
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("%w: goal task dependencies contain a cycle at %q", ErrInvalid, id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		task := tasks[ids[id]]
		seenDeps := make(map[string]struct{}, len(task.DependsOn))
		for _, dependency := range task.DependsOn {
			dependency = strings.TrimSpace(dependency)
			if dependency == id {
				return fmt.Errorf("%w: task %q depends on itself", ErrInvalid, id)
			}
			if _, ok := ids[dependency]; !ok {
				return fmt.Errorf("%w: task %q has missing dependency %q", ErrInvalid, id, dependency)
			}
			if _, duplicate := seenDeps[dependency]; duplicate {
				return fmt.Errorf("%w: task %q repeats dependency %q", ErrInvalid, id, dependency)
			}
			seenDeps[dependency] = struct{}{}
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetGoal(ctx context.Context, subject, id string) (Goal, error) {
	if !validSubject(subject) || strings.TrimSpace(id) == "" {
		return Goal{}, fmt.Errorf("%w: subject and goal id are required", ErrInvalid)
	}
	goal, err := scanGoal(s.db.QueryRowContext(ctx, goalSelect+` WHERE subject = ? AND id = ?`, subject, id))
	if err != nil {
		return Goal{}, err
	}
	goal.Tasks, err = s.listGoalTasks(ctx, goal.ID)
	return goal, err
}

const goalSelect = `SELECT id, subject, title, objective, status, error, max_cost_usd,
	max_duration_ms, cost_usd, reserved_cost_usd, duration_ms, created_at, updated_at, started_at, finished_at
	FROM autopilot_goals`

func scanGoal(row rowScanner) (Goal, error) {
	var goal Goal
	var created, updated string
	var started, finished sql.NullString
	err := row.Scan(&goal.ID, &goal.Subject, &goal.Title, &goal.Objective, &goal.Status, &goal.Error,
		&goal.Budget.MaxCostUSD, &goal.Budget.MaxDurationMS, &goal.CostUSD,
		&goal.ReservedCostUSD, &goal.DurationMS, &created, &updated, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return Goal{}, ErrNotFound
	}
	if err != nil {
		return Goal{}, err
	}
	if goal.CreatedAt, err = parseTime(created); err != nil {
		return Goal{}, err
	}
	if goal.UpdatedAt, err = parseTime(updated); err != nil {
		return Goal{}, err
	}
	if goal.StartedAt, err = stringPtrTime(started); err != nil {
		return Goal{}, err
	}
	if goal.FinishedAt, err = stringPtrTime(finished); err != nil {
		return Goal{}, err
	}
	goal.Tasks = []GoalTask{}
	return goal, nil
}

func (s *Store) listGoalTasks(ctx context.Context, goalID string) ([]GoalTask, error) {
	rows, err := s.db.QueryContext(ctx, taskSelect+` WHERE goal_id = ? ORDER BY rowid`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGoalTasks(rows)
}

const taskSelect = `SELECT id, goal_id, title, prompt, agent_id, status, depends_on_json,
	max_cost_usd, max_duration_ms, run_id, proof_id, result, error, cost_usd, duration_ms,
	started_at, finished_at FROM autopilot_goal_tasks`

func scanGoalTasks(rows *sql.Rows) ([]GoalTask, error) {
	out := make([]GoalTask, 0)
	for rows.Next() {
		task, err := scanGoalTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

func scanGoalTask(row rowScanner) (GoalTask, error) {
	var task GoalTask
	var deps []byte
	var cost sql.NullFloat64
	var duration sql.NullInt64
	var started, finished sql.NullString
	err := row.Scan(&task.ID, &task.GoalID, &task.Title, &task.Prompt, &task.AgentID,
		&task.Status, &deps, &task.Budget.MaxCostUSD, &task.Budget.MaxDurationMS,
		&task.RunID, &task.ProofID, &task.Result, &task.Error, &cost, &duration, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return GoalTask{}, ErrNotFound
	}
	if err != nil {
		return GoalTask{}, err
	}
	if err := json.Unmarshal(deps, &task.DependsOn); err != nil {
		return GoalTask{}, fmt.Errorf("autopilot: decode task dependencies: %w", err)
	}
	if task.DependsOn == nil {
		task.DependsOn = []string{}
	}
	if cost.Valid {
		task.CostUSD = &cost.Float64
	}
	if duration.Valid {
		task.DurationMS = &duration.Int64
	}
	if task.StartedAt, err = stringPtrTime(started); err != nil {
		return GoalTask{}, err
	}
	if task.FinishedAt, err = stringPtrTime(finished); err != nil {
		return GoalTask{}, err
	}
	return task, nil
}

func (s *Store) ListGoals(ctx context.Context, subject string, filter GoalFilter) ([]Goal, error) {
	if !validSubject(subject) {
		return nil, fmt.Errorf("%w: subject is required", ErrInvalid)
	}
	if filter.Status != "" && !validGoalStatus(filter.Status) {
		return nil, fmt.Errorf("%w: unknown goal status %q", ErrInvalid, filter.Status)
	}
	q := `SELECT id FROM autopilot_goals WHERE subject = ?`
	args := []any{subject}
	if filter.Status != "" {
		q += ` AND status = ?`
		args = append(args, filter.Status)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	q += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]Goal, 0, len(ids))
	for _, id := range ids {
		goal, err := s.GetGoal(ctx, subject, id)
		if err != nil {
			return nil, err
		}
		out = append(out, goal)
	}
	return out, nil
}

// StartGoal makes a draft executable exactly once.
func (s *Store) StartGoal(ctx context.Context, subject, id string) (Goal, error) {
	if !validSubject(subject) || strings.TrimSpace(id) == "" {
		return Goal{}, fmt.Errorf("%w: subject and goal id are required", ErrInvalid)
	}
	now := s.timestamp()
	res, err := s.db.ExecContext(ctx, `
		UPDATE autopilot_goals SET status = ?, started_at = ?, updated_at = ?
		WHERE subject = ? AND id = ? AND status = ?`,
		GoalRunning, timeString(now), timeString(now), subject, id, GoalStatusDraft)
	if err != nil {
		return Goal{}, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return Goal{}, err
	}
	if rows != 1 {
		if _, err := s.GetGoal(ctx, subject, id); err != nil {
			return Goal{}, err
		}
		return Goal{}, fmt.Errorf("%w: goal is not a draft", ErrConflict)
	}
	return s.GetGoal(ctx, subject, id)
}

// ReadyGoalTasks returns pending tasks whose dependencies succeeded. A crash-
// blocked task stops scheduling for the whole goal until an operator resolves
// its unknown external effects.
func (s *Store) ReadyGoalTasks(ctx context.Context, subject, goalID string) ([]GoalTask, error) {
	goal, err := s.GetGoal(ctx, subject, goalID)
	if err != nil {
		return nil, err
	}
	if goal.Status != GoalRunning {
		return nil, fmt.Errorf("%w: goal is not running", ErrConflict)
	}
	states := make(map[string]GoalTaskStatus, len(goal.Tasks))
	for _, task := range goal.Tasks {
		states[task.ID] = task.Status
		if task.Status == GoalTaskBlocked {
			return []GoalTask{}, nil
		}
	}
	ready := make([]GoalTask, 0)
	for _, task := range goal.Tasks {
		if task.Status != GoalTaskPending {
			continue
		}
		ok := true
		for _, dependency := range task.DependsOn {
			if states[dependency] != GoalTaskSucceeded {
				ok = false
				break
			}
		}
		if ok {
			ready = append(ready, task)
		}
	}
	return ready, nil
}

// StartGoalTask atomically reserves a pending, dependency-ready task. Its run
// ID is persisted before execution so the caller can use the same ClaimRun ID.
func (s *Store) StartGoalTask(ctx context.Context, subject, goalID, taskID, runID string) (GoalTask, error) {
	if !validSubject(subject) || strings.TrimSpace(goalID) == "" || strings.TrimSpace(taskID) == "" || strings.TrimSpace(runID) == "" {
		return GoalTask{}, fmt.Errorf("%w: subject, goal_id, task_id, and run_id are required", ErrInvalid)
	}
	if len(goalID) > 512 || len(taskID) > 512 || len(runID) > 1024 {
		return GoalTask{}, fmt.Errorf("%w: goal task identifiers are too large", ErrInvalid)
	}
	goal, err := s.GetGoal(ctx, subject, goalID)
	if err != nil {
		return GoalTask{}, err
	}
	if goal.Status != GoalRunning || goal.StartedAt == nil {
		return GoalTask{}, fmt.Errorf("%w: goal is not running", ErrConflict)
	}
	var selected *GoalTask
	states := make(map[string]GoalTaskStatus, len(goal.Tasks))
	for i := range goal.Tasks {
		states[goal.Tasks[i].ID] = goal.Tasks[i].Status
		if goal.Tasks[i].Status == GoalTaskBlocked {
			return GoalTask{}, fmt.Errorf("%w: goal has a crash-uncertain task requiring review", ErrConflict)
		}
		if goal.Tasks[i].ID == taskID {
			selected = &goal.Tasks[i]
		}
	}
	if selected == nil {
		return GoalTask{}, ErrNotFound
	}
	if selected.Status != GoalTaskPending {
		return GoalTask{}, fmt.Errorf("%w: task is %s", ErrConflict, selected.Status)
	}
	for _, dependency := range selected.DependsOn {
		if states[dependency] != GoalTaskSucceeded {
			return GoalTask{}, fmt.Errorf("%w: dependency %q has not succeeded", ErrConflict, dependency)
		}
	}
	if goal.CostUSD+goal.ReservedCostUSD+selected.Budget.MaxCostUSD > goal.Budget.MaxCostUSD+1e-9 {
		return GoalTask{}, fmt.Errorf("%w: goal cost budget cannot reserve this task", ErrConflict)
	}
	elapsed := s.timestamp().Sub(*goal.StartedAt).Milliseconds()
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed+selected.Budget.MaxDurationMS > goal.Budget.MaxDurationMS {
		return GoalTask{}, fmt.Errorf("%w: goal duration budget cannot reserve this task", ErrConflict)
	}
	now := s.timestamp()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GoalTask{}, err
	}
	defer func() { _ = tx.Rollback() }()
	reserve, err := tx.ExecContext(ctx, `
		UPDATE autopilot_goals
		SET reserved_cost_usd = reserved_cost_usd + ?, updated_at = ?
		WHERE id = ? AND subject = ? AND status = ?
		  AND cost_usd + reserved_cost_usd + ? <= max_cost_usd + 0.000000001`,
		selected.Budget.MaxCostUSD, timeString(now), goalID, subject, GoalRunning,
		selected.Budget.MaxCostUSD)
	if err != nil {
		return GoalTask{}, err
	}
	reserved, err := reserve.RowsAffected()
	if err != nil {
		return GoalTask{}, err
	}
	if reserved != 1 {
		return GoalTask{}, fmt.Errorf("%w: goal cost budget cannot reserve this task", ErrConflict)
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE autopilot_goal_tasks SET status = ?, run_id = ?, started_at = ?
		WHERE id = ? AND goal_id = ? AND status = ?
		  AND EXISTS (SELECT 1 FROM autopilot_goals WHERE id = ? AND subject = ? AND status = ?)`,
		GoalTaskRunning, runID, timeString(now), taskID, goalID, GoalTaskPending,
		goalID, subject, GoalRunning)
	if err != nil {
		return GoalTask{}, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return GoalTask{}, err
	}
	if rows != 1 {
		return GoalTask{}, fmt.Errorf("%w: task was claimed concurrently", ErrConflict)
	}
	if err := tx.Commit(); err != nil {
		return GoalTask{}, err
	}
	return scanGoalTask(s.db.QueryRowContext(ctx, taskSelect+` WHERE id = ? AND goal_id = ?`, taskID, goalID))
}

// FinishGoalTask records a terminal result and advances or fails the goal.
// Missing measurements and exceeded task/goal budgets fail closed.
func (s *Store) FinishGoalTask(ctx context.Context, request FinishGoalTaskRequest) (Goal, error) {
	return s.finishGoalTask(ctx, request, GoalTaskRunning)
}

// ResolveBlockedGoalTask lets an operator resolve a crash-uncertain task from
// external evidence. It never retries the task. A successful resolution makes
// downstream tasks ready again; a failed resolution terminates the goal.
func (s *Store) ResolveBlockedGoalTask(ctx context.Context, request FinishGoalTaskRequest) (Goal, error) {
	return s.finishGoalTask(ctx, request, GoalTaskBlocked)
}

// FailGoal makes a running goal terminal when the coordinator cannot admit
// more work (for example, because the remaining cost or duration budget is
// insufficient). Pending tasks are blocked with the durable reason. Running
// tasks are deliberately left running: their effects may already be in flight,
// and callers must finish them or let restart quarantine mark them uncertain.
func (s *Store) FailGoal(ctx context.Context, subject, id, reason string) (Goal, error) {
	subject = strings.TrimSpace(subject)
	id = strings.TrimSpace(id)
	reason = strings.TrimSpace(reason)
	if !validSubject(subject) || id == "" || reason == "" {
		return Goal{}, fmt.Errorf("%w: subject, goal id, and failure reason are required", ErrInvalid)
	}
	if len(id) > 512 || len(reason) > maxTaskErrorBytes || !utf8.ValidString(reason) {
		return Goal{}, fmt.Errorf("%w: goal id or failure reason is too large", ErrInvalid)
	}
	reason = boundedReceiptText(reason, maxTaskErrorBytes)
	now := s.timestamp()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Goal{}, err
	}
	defer func() { _ = tx.Rollback() }()
	goal, err := scanGoal(tx.QueryRowContext(ctx, goalSelect+` WHERE subject = ? AND id = ?`, subject, id))
	if err != nil {
		return Goal{}, err
	}
	if goal.Status != GoalRunning {
		return Goal{}, fmt.Errorf("%w: goal is %s, expected running", ErrConflict, goal.Status)
	}
	duration := goal.DurationMS
	if goal.StartedAt != nil {
		duration = now.Sub(*goal.StartedAt).Milliseconds()
		if duration < 0 {
			duration = 0
		}
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE autopilot_goals
		SET status = ?, error = ?, duration_ms = ?, updated_at = ?, finished_at = ?
		WHERE subject = ? AND id = ? AND status = ?`, GoalFailed, reason, duration,
		timeString(now), timeString(now), subject, id, GoalRunning)
	if err != nil {
		return Goal{}, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return Goal{}, err
	}
	if rows != 1 {
		return Goal{}, fmt.Errorf("%w: goal changed concurrently", ErrConflict)
	}
	blockedReason := boundedReceiptText("goal stopped before task admission: "+reason, maxTaskErrorBytes)
	if _, err := tx.ExecContext(ctx, `
		UPDATE autopilot_goal_tasks SET status = ?, error = ?, finished_at = ?
		WHERE goal_id = ? AND status = ?`, GoalTaskBlocked, blockedReason,
		timeString(now), id, GoalTaskPending); err != nil {
		return Goal{}, err
	}
	if err := tx.Commit(); err != nil {
		return Goal{}, err
	}
	return s.GetGoal(ctx, subject, id)
}

func (s *Store) finishGoalTask(ctx context.Context, request FinishGoalTaskRequest, from GoalTaskStatus) (Goal, error) {
	if !validSubject(request.Subject) || strings.TrimSpace(request.GoalID) == "" || strings.TrimSpace(request.TaskID) == "" {
		return Goal{}, fmt.Errorf("%w: subject, goal_id, and task_id are required", ErrInvalid)
	}
	if request.Status != GoalTaskSucceeded && request.Status != GoalTaskFailed {
		return Goal{}, fmt.Errorf("%w: task must finish as succeeded or failed", ErrInvalid)
	}
	if len(request.Result) > maxTaskResultBytes || !utf8.ValidString(request.Result) {
		return Goal{}, fmt.Errorf("%w: task result must be UTF-8 and at most %d bytes", ErrInvalid, maxTaskResultBytes)
	}
	if len(request.Error) > maxTaskErrorBytes || !utf8.ValidString(request.Error) {
		return Goal{}, fmt.Errorf("%w: task error must be UTF-8 and at most %d bytes", ErrInvalid, maxTaskErrorBytes)
	}
	if request.CostUSD == nil || request.DurationMS == nil || *request.CostUSD < 0 ||
		math.IsNaN(valueOrNaN(request.CostUSD)) || math.IsInf(valueOrNaN(request.CostUSD), 0) || *request.DurationMS < 0 {
		request.Status = GoalTaskFailed
		request.Error = appendReason(request.Error, "required cost or duration measurement is missing or invalid")
		zeroCost, zeroDuration := 0.0, int64(0)
		if request.CostUSD == nil || math.IsNaN(valueOrNaN(request.CostUSD)) || math.IsInf(valueOrNaN(request.CostUSD), 0) || *request.CostUSD < 0 {
			request.CostUSD = &zeroCost
		}
		if request.DurationMS == nil || *request.DurationMS < 0 {
			request.DurationMS = &zeroDuration
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Goal{}, err
	}
	defer func() { _ = tx.Rollback() }()
	goal, err := scanGoal(tx.QueryRowContext(ctx, goalSelect+` WHERE subject = ? AND id = ?`, request.Subject, request.GoalID))
	if err != nil {
		return Goal{}, err
	}
	task, err := scanGoalTask(tx.QueryRowContext(ctx, taskSelect+` WHERE id = ? AND goal_id = ?`, request.TaskID, request.GoalID))
	if err != nil {
		return Goal{}, err
	}
	if task.Status != from {
		return Goal{}, fmt.Errorf("%w: task is %s, expected %s", ErrConflict, task.Status, from)
	}
	if *request.CostUSD > task.Budget.MaxCostUSD+1e-9 {
		request.Status = GoalTaskFailed
		request.Error = appendReason(request.Error, "task exceeded its cost budget")
	}
	if *request.DurationMS > task.Budget.MaxDurationMS {
		request.Status = GoalTaskFailed
		request.Error = appendReason(request.Error, "task exceeded its duration budget")
	}
	newCost := goal.CostUSD + *request.CostUSD
	if newCost > goal.Budget.MaxCostUSD+1e-9 {
		request.Status = GoalTaskFailed
		request.Error = appendReason(request.Error, "goal exceeded its cost budget")
	}
	now := s.timestamp()
	goalDuration := goal.DurationMS
	if goal.StartedAt != nil {
		goalDuration = now.Sub(*goal.StartedAt).Milliseconds()
		if goalDuration < 0 {
			goalDuration = 0
		}
	}
	if goalDuration > goal.Budget.MaxDurationMS {
		request.Status = GoalTaskFailed
		request.Error = appendReason(request.Error, "goal exceeded its duration budget")
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE autopilot_goal_tasks
		SET status = ?, proof_id = ?, result = ?, error = ?, cost_usd = ?, duration_ms = ?, finished_at = ?
		WHERE id = ? AND goal_id = ? AND status = ?`, request.Status, request.ProofID,
		request.Result, request.Error, *request.CostUSD, *request.DurationMS, timeString(now),
		request.TaskID, request.GoalID, from)
	if err != nil {
		return Goal{}, err
	}
	goalStatus := goal.Status
	goalError := goal.Error
	var finished any
	if goal.FinishedAt != nil {
		finished = timeString(*goal.FinishedAt)
	}
	if request.Status == GoalTaskFailed {
		goalStatus = GoalFailed
		goalError = appendReason(goalError, request.Error)
		finished = timeString(now)
		_, err = tx.ExecContext(ctx, `
			UPDATE autopilot_goal_tasks SET status = ?, error = ?, finished_at = ?
			WHERE goal_id = ? AND status = ?`, GoalTaskBlocked,
			"blocked because another goal task failed", timeString(now), request.GoalID, GoalTaskPending)
		if err != nil {
			return Goal{}, err
		}
	} else {
		var remaining int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM autopilot_goal_tasks WHERE goal_id = ? AND status != ?`,
			request.GoalID, GoalTaskSucceeded).Scan(&remaining); err != nil {
			return Goal{}, err
		}
		// The current row has already been updated inside this transaction.
		if remaining == 0 && goal.Status == GoalRunning {
			goalStatus = GoalSucceeded
			finished = timeString(now)
		}
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE autopilot_goals SET status = ?, error = ?, cost_usd = ?,
		reserved_cost_usd = MAX(0, reserved_cost_usd - ?),
		duration_ms = ?, updated_at = ?, finished_at = ?
		WHERE subject = ? AND id = ?`, goalStatus, goalError, newCost, task.Budget.MaxCostUSD,
		goalDuration, timeString(now), finished, request.Subject, request.GoalID)
	if err != nil {
		return Goal{}, err
	}
	if err := tx.Commit(); err != nil {
		return Goal{}, err
	}
	return s.GetGoal(ctx, request.Subject, request.GoalID)
}

func valueOrNaN(v *float64) float64 {
	if v == nil {
		return math.NaN()
	}
	return *v
}

func appendReason(existing, reason string) string {
	if reason == "" {
		return existing
	}
	if existing == "" {
		return reason
	}
	combined := existing + "; " + reason
	if len(combined) > maxTaskErrorBytes {
		combined = combined[:maxTaskErrorBytes]
		for !utf8.ValidString(combined) {
			combined = combined[:len(combined)-1]
		}
	}
	return combined
}

// CancelGoal makes all nonterminal tasks terminal without deleting history.
// The runtime remains responsible for cancelling active contexts immediately.
func (s *Store) CancelGoal(ctx context.Context, subject, id string) (Goal, error) {
	if !validSubject(subject) || strings.TrimSpace(id) == "" {
		return Goal{}, fmt.Errorf("%w: subject and goal id are required", ErrInvalid)
	}
	now := s.timestamp()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Goal{}, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		UPDATE autopilot_goals SET status = ?, reserved_cost_usd = 0, updated_at = ?, finished_at = ?
		WHERE subject = ? AND id = ? AND status IN (?, ?)`, GoalCancelled,
		timeString(now), timeString(now), subject, id, GoalStatusDraft, GoalRunning)
	if err != nil {
		return Goal{}, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return Goal{}, err
	}
	if rows != 1 {
		return Goal{}, fmt.Errorf("%w: goal is not cancellable", ErrConflict)
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE autopilot_goal_tasks SET status = ?, finished_at = ?
		WHERE goal_id = ? AND status IN (?, ?, ?)`, GoalTaskCancelled, timeString(now),
		id, GoalTaskPending, GoalTaskRunning, GoalTaskBlocked)
	if err != nil {
		return Goal{}, err
	}
	if err := tx.Commit(); err != nil {
		return Goal{}, err
	}
	return s.GetGoal(ctx, subject, id)
}
