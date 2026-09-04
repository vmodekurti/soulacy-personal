package gateway

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/workboard"
	"github.com/soulacy/soulacy/pkg/message"
)

// wbRunTimeout bounds a single workboard agent run.
const wbRunTimeout = 15 * time.Minute

// Workboard handlers (Story 5). All routes live under /api/v1/workboard and
// return 503 until a store is wired via SetWorkboardStore.

// wbTaskBody is the JSON payload for create/update requests. Pointers
// distinguish "field absent" from "field set to zero value" on PATCH.
// DueAt is RFC3339; on PATCH an empty string clears the due date.
type wbTaskBody struct {
	Title       *string   `json:"title"`
	Description *string   `json:"description"`
	AgentID     *string   `json:"agent_id"`
	Status      *string   `json:"status"`
	Owner       *string   `json:"owner"`
	Priority    *string   `json:"priority"`
	Tags        *[]string `json:"tags"`
	DueAt       *string   `json:"due_at"`
}

// parseDueAt turns the wire value into (*time.Time, clear, error).
func parseDueAt(raw *string) (*time.Time, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	if strings.TrimSpace(*raw) == "" {
		return nil, true, nil
	}
	ts, err := time.Parse(time.RFC3339, *raw)
	if err != nil {
		return nil, false, fmt.Errorf("due_at must be RFC3339 (e.g. 2026-07-01T10:00:00Z): %w", err)
	}
	u := ts.UTC()
	return &u, false, nil
}

// wbStoreOr503 returns the store or writes a 503 and returns nil.
func (s *Server) wbStoreOr503(c *fiber.Ctx) *workboard.Store {
	if s.workboardStore == nil {
		_ = c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "workboard not enabled",
		})
		return nil
	}
	return s.workboardStore
}

// wbTaskID parses the :id path param. On failure it writes a 400 and
// returns ok=false.
func wbTaskID(c *fiber.Ctx) (int64, bool) {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		_ = c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "task id must be an integer",
		})
		return 0, false
	}
	return id, true
}

// wbError maps store errors to HTTP responses.
func (s *Server) wbError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, workboard.ErrInvalid):
		return s.errJSON(c, fiber.StatusBadRequest, err)
	case errors.Is(err, workboard.ErrNotFound):
		return s.errMsg(c, fiber.StatusNotFound, "task not found")
	default:
		s.log.Error("workboard: store error", zap.Error(err))
		return s.errMsg(c, fiber.StatusInternalServerError, "internal error")
	}
}

// handleWorkboardList handles GET /api/v1/workboard/tasks?status=&agent_id=
func (s *Server) handleWorkboardList(c *fiber.Ctx) error {
	store := s.wbStoreOr503(c)
	if store == nil {
		return nil
	}
	tasks, err := store.List(c.Context(), workboard.Filter{
		Status:  c.Query("status"),
		AgentID: c.Query("agent_id"),
	})
	if err != nil {
		return s.wbError(c, err)
	}
	return c.JSON(fiber.Map{"tasks": tasks, "statuses": workboard.Statuses})
}

// handleWorkboardCreate handles POST /api/v1/workboard/tasks
func (s *Server) handleWorkboardCreate(c *fiber.Ctx) error {
	store := s.wbStoreOr503(c)
	if store == nil {
		return nil
	}
	var body wbTaskBody
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid JSON body")
	}
	t := workboard.Task{}
	if body.Title != nil {
		t.Title = *body.Title
	}
	if body.Description != nil {
		t.Description = *body.Description
	}
	if body.AgentID != nil {
		t.AgentID = *body.AgentID
	}
	if body.Status != nil {
		t.Status = *body.Status
	}
	if body.Owner != nil {
		t.Owner = *body.Owner
	}
	if body.Priority != nil {
		t.Priority = *body.Priority
	}
	if body.Tags != nil {
		t.Tags = *body.Tags
	}
	due, _, err := parseDueAt(body.DueAt)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	t.DueAt = due
	created, err := store.Create(c.Context(), t)
	if err != nil {
		return s.wbError(c, err)
	}
	return c.Status(fiber.StatusCreated).JSON(created)
}

// handleWorkboardGet handles GET /api/v1/workboard/tasks/:id
func (s *Server) handleWorkboardGet(c *fiber.Ctx) error {
	store := s.wbStoreOr503(c)
	if store == nil {
		return nil
	}
	id, ok := wbTaskID(c)
	if !ok {
		return nil
	}
	task, err := store.Get(c.Context(), id)
	if err != nil {
		return s.wbError(c, err)
	}
	return c.JSON(task)
}

// handleWorkboardUpdate handles PATCH /api/v1/workboard/tasks/:id
func (s *Server) handleWorkboardUpdate(c *fiber.Ctx) error {
	store := s.wbStoreOr503(c)
	if store == nil {
		return nil
	}
	id, ok := wbTaskID(c)
	if !ok {
		return nil
	}
	var body wbTaskBody
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid JSON body")
	}
	due, clearDue, err := parseDueAt(body.DueAt)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	updated, err := store.Update(c.Context(), id, workboard.Update{
		Title:       body.Title,
		Description: body.Description,
		AgentID:     body.AgentID,
		Status:      body.Status,
		Owner:       body.Owner,
		Priority:    body.Priority,
		Tags:        body.Tags,
		DueAt:       due,
		ClearDueAt:  clearDue,
	})
	if err != nil {
		return s.wbError(c, err)
	}
	return c.JSON(updated)
}

// ---------------------------------------------------------------------------
// Comments & reviewer notes (Story 14)
// ---------------------------------------------------------------------------

// handleWorkboardComments handles GET /api/v1/workboard/tasks/:id/comments
func (s *Server) handleWorkboardComments(c *fiber.Ctx) error {
	store := s.wbStoreOr503(c)
	if store == nil {
		return nil
	}
	id, ok := wbTaskID(c)
	if !ok {
		return nil
	}
	comments, err := store.ListComments(c.Context(), id)
	if err != nil {
		return s.wbError(c, err)
	}
	return c.JSON(fiber.Map{"comments": comments, "count": len(comments)})
}

// handleWorkboardAddComment handles POST /api/v1/workboard/tasks/:id/comments
func (s *Server) handleWorkboardAddComment(c *fiber.Ctx) error {
	store := s.wbStoreOr503(c)
	if store == nil {
		return nil
	}
	id, ok := wbTaskID(c)
	if !ok {
		return nil
	}
	var body struct {
		Author string `json:"author"`
		Body   string `json:"body"`
		Kind   string `json:"kind"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid JSON body")
	}
	comment, err := store.AddComment(c.Context(), id, workboard.Comment{
		Author: body.Author, Body: body.Body, Kind: body.Kind,
	})
	if err != nil {
		return s.wbError(c, err)
	}
	return c.Status(fiber.StatusCreated).JSON(comment)
}

// handleWorkboardDeleteComment handles DELETE /api/v1/workboard/comments/:id
func (s *Server) handleWorkboardDeleteComment(c *fiber.Ctx) error {
	store := s.wbStoreOr503(c)
	if store == nil {
		return nil
	}
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "comment id must be an integer")
	}
	if err := store.DeleteComment(c.Context(), id); err != nil {
		return s.wbError(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// handleWorkboardRun handles POST /api/v1/workboard/tasks/:id/run (Story 6).
// Starts a new attempt for the task through its assigned agent and returns
// 202 with the run record; the agent executes asynchronously.
func (s *Server) handleWorkboardRun(c *fiber.Ctx) error {
	store := s.wbStoreOr503(c)
	if store == nil {
		return nil
	}
	id, ok := wbTaskID(c)
	if !ok {
		return nil
	}
	task, err := store.Get(c.Context(), id)
	if err != nil {
		return s.wbError(c, err)
	}
	if strings.TrimSpace(task.AgentID) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "task has no agent assigned",
		})
	}

	sessionID := fmt.Sprintf("wb-%d-%d", task.ID, time.Now().UnixNano())
	logPath := ""
	if s.actions != nil {
		if p, ok := s.actions.(interface{ EventFilePath(string) string }); ok {
			logPath = p.EventFilePath(task.AgentID)
		}
	}

	run, err := store.StartRun(c.Context(), task.ID, task.AgentID, sessionID, logPath)
	if err != nil {
		if errors.Is(err, workboard.ErrRunActive) {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{
				"error": "task already has an active run",
			})
		}
		return s.wbError(c, err)
	}

	running := workboard.StatusRunning
	if _, err := store.Update(c.Context(), task.ID, workboard.Update{Status: &running}); err != nil {
		s.log.Warn("workboard: failed to mark task running", zap.Int64("task", task.ID), zap.Error(err))
	}

	s.emitRunEvent("run.started", run, task, "")
	go s.executeWorkboardRun(run, task)
	return c.Status(fiber.StatusAccepted).JSON(run)
}

// executeWorkboardRun runs the task through the engine and records the
// outcome. Runs in its own goroutine; uses fresh contexts so neither the
// HTTP request lifetime nor the run timeout can lose the result write.
func (s *Server) executeWorkboardRun(run workboard.Run, task workboard.Task) {
	finish := func(runStatus, result, reason, taskStatus string) {
		fctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := s.workboardStore.FinishRun(fctx, run.ID, runStatus, result, reason); err != nil {
			s.log.Error("workboard: finish run failed", zap.Int64("run", run.ID), zap.Error(err))
		}
		if _, err := s.workboardStore.Update(fctx, task.ID, workboard.Update{Status: &taskStatus}); err != nil {
			s.log.Error("workboard: task status update failed", zap.Int64("task", task.ID), zap.Error(err))
		}
	}

	if s.engine == nil {
		finish(workboard.RunStatusFailed, "", "agent engine not available", workboard.StatusFailed)
		s.emitRunEvent("run.failed", run, task, "agent engine not available")
		return
	}

	prompt := task.Title
	if strings.TrimSpace(task.Description) != "" {
		prompt += "\n\n" + task.Description
	}
	msg := message.Message{
		ID:        uuid.New().String(),
		SessionID: run.SessionID,
		AgentID:   task.AgentID,
		Channel:   "internal",
		ThreadID:  "workboard",
		UserID:    "workboard",
		Username:  "workboard",
		Role:      message.RoleUser,
		Parts:     message.Text(prompt),
		Metadata: map[string]string{
			"trigger": "workboard",
			"task_id": strconv.FormatInt(task.ID, 10),
		},
		CreatedAt: time.Now().UTC(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), wbRunTimeout)
	defer cancel()
	reply, err := s.engine.Handle(ctx, msg)
	if err != nil {
		finish(workboard.RunStatusFailed, "", err.Error(), workboard.StatusFailed)
		s.emitRunEvent("run.failed", run, task, err.Error())
		// Partial outputs from a failed run are still worth surfacing.
		s.recordRunArtifacts(run, task)
		return
	}
	finish(workboard.RunStatusDone, wbResultSummary(reply), "", workboard.StatusNeedsReview)
	s.emitRunEvent("run.finished", run, task, "")
	s.recordRunArtifacts(run, task) // Story 13: attach produced files + run.artifact events
}

// emitRunEvent publishes a workboard run lifecycle event (run.started,
// run.finished, run.failed) through the EventHub so it reaches the WebSocket
// stream, the action log, and the queue publisher (story E1).
func (s *Server) emitRunEvent(eventType string, run workboard.Run, task workboard.Task, failureReason string) {
	if s.hub == nil {
		return
	}
	payload := map[string]any{
		"task_id":    task.ID,
		"task_title": task.Title,
		"run_id":     run.ID,
		"attempt":    run.Attempt,
	}
	if failureReason != "" {
		payload["failure_reason"] = failureReason
	}
	s.hub.Emit(message.Event{
		Type:      eventType,
		AgentID:   task.AgentID,
		SessionID: run.SessionID,
		Payload:   payload,
		Timestamp: time.Now().UTC(),
	})
}

// wbResultSummary extracts a short text summary from the agent's reply.
func wbResultSummary(reply message.Message) string {
	var b strings.Builder
	for _, p := range reply.Parts {
		if p.Type == message.ContentText && p.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Text)
		}
	}
	sum := strings.TrimSpace(b.String())
	const maxRunes = 500
	r := []rune(sum)
	if len(r) > maxRunes {
		sum = string(r[:maxRunes]) + "…"
	}
	if sum == "" {
		sum = "(agent returned an empty reply)"
	}
	return sum
}

// handleWorkboardListRuns handles GET /api/v1/workboard/tasks/:id/runs
func (s *Server) handleWorkboardListRuns(c *fiber.Ctx) error {
	store := s.wbStoreOr503(c)
	if store == nil {
		return nil
	}
	id, ok := wbTaskID(c)
	if !ok {
		return nil
	}
	if _, err := store.Get(c.Context(), id); err != nil {
		return s.wbError(c, err)
	}
	runs, err := store.ListRuns(c.Context(), id)
	if err != nil {
		return s.wbError(c, err)
	}
	return c.JSON(fiber.Map{"runs": runs})
}

// handleWorkboardDelete handles DELETE /api/v1/workboard/tasks/:id
func (s *Server) handleWorkboardDelete(c *fiber.Ctx) error {
	store := s.wbStoreOr503(c)
	if store == nil {
		return nil
	}
	id, ok := wbTaskID(c)
	if !ok {
		return nil
	}
	if err := store.Delete(c.Context(), id); err != nil {
		return s.wbError(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}
