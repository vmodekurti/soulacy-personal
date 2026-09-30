package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/missions"
	"github.com/soulacy/soulacy/internal/runtime"
)

func (s *Server) handlePlanMission(c *fiber.Ctx) error {
	var body struct {
		Objective string `json:"objective"`
	}
	if c.BodyParser(&body) != nil {
		return s.errMsg(c, 400, "invalid request body")
	}
	plan, err := missions.BuildPlan(body.Objective)
	if err != nil {
		return s.errMsg(c, 400, err.Error())
	}
	return c.JSON(fiber.Map{"plan": plan})
}

func (s *Server) handleListMissions(c *fiber.Ctx) error {
	workspaceID, subject, _ := authenticatedConnectionActor(c)
	items, err := s.listMissions(c.UserContext(), workspaceID, subject)
	if err != nil {
		return s.missionError(c, err)
	}
	return c.JSON(fiber.Map{"missions": items, "count": len(items)})
}

func (s *Server) handleGetMission(c *fiber.Ctx) error {
	workspaceID, subject, _ := authenticatedConnectionActor(c)
	item, err := s.getMission(c.UserContext(), workspaceID, subject, c.Params("id"))
	if err != nil {
		return s.missionError(c, err)
	}
	return c.JSON(fiber.Map{"mission": item})
}

func (s *Server) handleCreateMission(c *fiber.Ctx) error {
	var body missions.Mission
	if c.BodyParser(&body) != nil {
		return s.errMsg(c, 400, "invalid request body")
	}
	workspaceID, subject, _ := authenticatedConnectionActor(c)
	item, err := s.createMission(c.UserContext(), workspaceID, subject, body)
	if err != nil {
		return s.missionError(c, err)
	}
	s.recordAdminAudit(c, "mission.create", "mission", item.ID, "ok", map[string]any{"monitor_id": item.MonitorID})
	return c.Status(201).JSON(fiber.Map{"mission": s.missionView(item), "message": "Mission activated. Genie will keep it moving on the approved schedule."})
}

func (s *Server) handleUpdateMission(c *fiber.Ctx) error {
	var body struct {
		Status     string `json:"status"`
		Progress   string `json:"progress"`
		NextAction string `json:"next_action"`
		Blocker    string `json:"blocker"`
	}
	if c.BodyParser(&body) != nil {
		return s.errMsg(c, 400, "invalid request body")
	}
	workspaceID, subject, _ := authenticatedConnectionActor(c)
	item, err := s.updateMission(c.UserContext(), workspaceID, subject, c.Params("id"), body.Status, body.Progress, body.NextAction, body.Blocker)
	if err != nil {
		return s.missionError(c, err)
	}
	return c.JSON(fiber.Map{"mission": s.missionView(item)})
}

func (s *Server) handlePauseMission(c *fiber.Ctx) error {
	return s.handleMissionAction(c, missions.StatusPaused, "Mission paused.")
}

func (s *Server) handleResumeMission(c *fiber.Ctx) error {
	return s.handleMissionAction(c, missions.StatusActive, "Mission resumed.")
}

func (s *Server) handleCompleteMission(c *fiber.Ctx) error {
	var body struct {
		Progress string `json:"progress"`
	}
	_ = c.BodyParser(&body)
	workspaceID, subject, _ := authenticatedConnectionActor(c)
	item, err := s.finishMission(c.UserContext(), workspaceID, subject, c.Params("id"), missions.StatusCompleted, body.Progress)
	if err != nil {
		return s.missionError(c, err)
	}
	return c.JSON(fiber.Map{"mission": s.missionView(item), "message": "Mission completed and its background runner stopped."})
}

func (s *Server) handleCancelMission(c *fiber.Ctx) error {
	workspaceID, subject, _ := authenticatedConnectionActor(c)
	item, err := s.finishMission(c.UserContext(), workspaceID, subject, c.Params("id"), missions.StatusCancelled, "Mission cancelled")
	if err != nil {
		return s.missionError(c, err)
	}
	return c.JSON(fiber.Map{"mission": s.missionView(item), "message": "Mission cancelled. Its history remains available."})
}

func (s *Server) handleMissionAction(c *fiber.Ctx, status, message string) error {
	workspaceID, subject, _ := authenticatedConnectionActor(c)
	item, err := s.setMissionActivity(c.UserContext(), workspaceID, subject, c.Params("id"), status)
	if err != nil {
		return s.missionError(c, err)
	}
	return c.JSON(fiber.Map{"mission": s.missionView(item), "message": message})
}

func (s *Server) createMission(ctx context.Context, workspaceID, subject string, in missions.Mission) (missions.Mission, error) {
	if s.missionStore == nil || s.missionMonitor == nil {
		return missions.Mission{}, errors.New("mission service is unavailable")
	}
	in.WorkspaceID, in.OwnerSubject, in.Status = workspaceID, subject, missions.StatusActive
	normalized, err := missions.Normalize(in)
	if err != nil {
		return missions.Mission{}, err
	}
	prompt := fmt.Sprintf("Standing mission: %s\n\nObjective: %s\n\nFinish line: %s\n\nOn every run, advance this goal. Return a concise evidence-backed progress update, a blocker that needs the user when one exists, or an empty response when nothing meaningful changed. Never treat this mission as permission to bypass approvals.", normalized.Title, normalized.Objective, normalized.FinishLine)
	runner, err := s.missionMonitor.CreateGenieMonitor(prompt, normalized.Cron, normalized.At, normalized.Channel, normalized.To)
	if err != nil {
		return missions.Mission{}, err
	}
	monitorID, _ := runner["id"].(string)
	if monitorID == "" {
		return missions.Mission{}, errors.New("mission runner did not return an ID")
	}
	normalized.MonitorID = monitorID
	normalized.Progress = "Mission activated"
	normalized.NextAction = "Wait for the next scheduled run"
	created, err := s.missionStore.Create(ctx, normalized)
	if err != nil {
		_ = s.missionMonitor.CancelGenieMonitor(monitorID)
		return missions.Mission{}, err
	}
	return created, nil
}

func (s *Server) listMissions(ctx context.Context, workspaceID, subject string) ([]map[string]any, error) {
	if s.missionStore == nil {
		return nil, errors.New("mission service is unavailable")
	}
	items, err := s.missionStore.List(ctx, workspaceID, subject)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, s.missionView(item))
	}
	return out, nil
}

func (s *Server) getMission(ctx context.Context, workspaceID, subject, id string) (map[string]any, error) {
	if s.missionStore == nil {
		return nil, errors.New("mission service is unavailable")
	}
	item, err := s.missionStore.Get(ctx, workspaceID, subject, id)
	if err != nil {
		return nil, err
	}
	return s.missionView(item), nil
}

func (s *Server) missionView(item missions.Mission) map[string]any {
	view := map[string]any{
		"id": item.ID, "title": item.Title, "objective": item.Objective, "finish_line": item.FinishLine,
		"cron": item.Cron, "at": item.At, "channel": item.Channel, "to": item.To, "monitor_id": item.MonitorID,
		"status": item.Status, "progress": item.Progress, "next_action": item.NextAction, "blocker": item.Blocker,
		"created_at": item.CreatedAt, "updated_at": item.UpdatedAt, "last_progress_at": item.LastProgressAt,
	}
	if s.missionMonitor != nil {
		for _, runner := range s.missionMonitor.ListGenieMonitors() {
			if runner["id"] == item.MonitorID {
				view["runner"] = runner
				break
			}
		}
	}
	return view
}

func (s *Server) setMissionActivity(ctx context.Context, workspaceID, subject, id, status string) (missions.Mission, error) {
	if s.missionStore == nil || s.missionMonitor == nil {
		return missions.Mission{}, errors.New("mission service is unavailable")
	}
	item, err := s.missionStore.Get(ctx, workspaceID, subject, id)
	if err != nil {
		return missions.Mission{}, err
	}
	if status == missions.StatusPaused {
		err = s.missionMonitor.PauseGenieMonitor(item.MonitorID)
	} else {
		err = s.missionMonitor.ResumeGenieMonitor(item.MonitorID)
	}
	if err != nil {
		return missions.Mission{}, err
	}
	var next string
	if status == missions.StatusPaused {
		next = "Resume when you want Genie to continue"
	} else {
		next = "Wait for the next scheduled run"
	}
	return s.missionStore.UpdateState(ctx, workspaceID, subject, id, status, item.Progress, next, "")
}

func (s *Server) updateMission(ctx context.Context, workspaceID, subject, id, status, progress, nextAction, blocker string) (missions.Mission, error) {
	if s.missionStore == nil {
		return missions.Mission{}, errors.New("mission service is unavailable")
	}
	current, err := s.missionStore.Get(ctx, workspaceID, subject, id)
	if err != nil {
		return missions.Mission{}, err
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = current.Status
	}
	if status != missions.StatusActive && status != missions.StatusBlocked {
		return missions.Mission{}, errors.New("progress updates may set status to active or blocked")
	}
	return s.missionStore.UpdateState(ctx, workspaceID, subject, id, status, progress, nextAction, blocker)
}

func (s *Server) finishMission(ctx context.Context, workspaceID, subject, id, status, progress string) (missions.Mission, error) {
	if s.missionStore == nil || s.missionMonitor == nil {
		return missions.Mission{}, errors.New("mission service is unavailable")
	}
	item, err := s.missionStore.Get(ctx, workspaceID, subject, id)
	if err != nil {
		return missions.Mission{}, err
	}
	if item.Status == missions.StatusCompleted || item.Status == missions.StatusCancelled {
		return item, nil
	}
	if err := s.missionMonitor.CancelGenieMonitor(item.MonitorID); err != nil {
		return missions.Mission{}, err
	}
	if strings.TrimSpace(progress) == "" {
		progress = item.Progress
	}
	return s.missionStore.UpdateState(ctx, workspaceID, subject, id, status, progress, "", "")
}

func (s *Server) missionError(c *fiber.Ctx, err error) error {
	if errors.Is(err, missions.ErrNotFound) {
		return s.errMsg(c, 404, "mission not found")
	}
	if strings.Contains(err.Error(), "unavailable") {
		return s.errMsg(c, 503, err.Error())
	}
	return s.errMsg(c, 400, err.Error())
}

func (s *Server) PlanMissionForGenie(objective string) (map[string]any, error) {
	plan, err := missions.BuildPlan(objective)
	if err != nil {
		return nil, err
	}
	return map[string]any{"plan": plan, "next": "Confirm the finish line, schedule, and optional delivery destination before creating the mission."}, nil
}

func (s *Server) CreateMissionForGenie(ctx context.Context, title, objective, finishLine, cron, at, channel, to string) (map[string]any, error) {
	item, err := s.createMission(ctx, runtime.PersonalWorkspaceID, "admin", missions.Mission{Title: title, Objective: objective, FinishLine: finishLine, Cron: cron, At: at, Channel: channel, To: to})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "mission": s.missionView(item), "message": "Mission activated and visible on the Missions page."}, nil
}

func (s *Server) ListMissionsForGenie(ctx context.Context) (map[string]any, error) {
	items, err := s.listMissions(ctx, runtime.PersonalWorkspaceID, "admin")
	return map[string]any{"count": len(items), "missions": items}, err
}

func (s *Server) GetMissionForGenie(ctx context.Context, id string) (map[string]any, error) {
	return s.getMission(ctx, runtime.PersonalWorkspaceID, "admin", id)
}

func (s *Server) UpdateMissionForGenie(ctx context.Context, id, status, progress, nextAction, blocker string) (map[string]any, error) {
	item, err := s.updateMission(ctx, runtime.PersonalWorkspaceID, "admin", id, status, progress, nextAction, blocker)
	if err != nil {
		return nil, err
	}
	return s.missionView(item), nil
}

func (s *Server) PauseMissionForGenie(ctx context.Context, id string) (map[string]any, error) {
	item, err := s.setMissionActivity(ctx, runtime.PersonalWorkspaceID, "admin", id, missions.StatusPaused)
	if err != nil {
		return nil, err
	}
	return s.missionView(item), nil
}

func (s *Server) ResumeMissionForGenie(ctx context.Context, id string) (map[string]any, error) {
	item, err := s.setMissionActivity(ctx, runtime.PersonalWorkspaceID, "admin", id, missions.StatusActive)
	if err != nil {
		return nil, err
	}
	return s.missionView(item), nil
}

func (s *Server) CompleteMissionForGenie(ctx context.Context, id, progress string) (map[string]any, error) {
	item, err := s.finishMission(ctx, runtime.PersonalWorkspaceID, "admin", id, missions.StatusCompleted, progress)
	if err != nil {
		return nil, err
	}
	return s.missionView(item), nil
}

func (s *Server) CancelMissionForGenie(ctx context.Context, id string) (map[string]any, error) {
	item, err := s.finishMission(ctx, runtime.PersonalWorkspaceID, "admin", id, missions.StatusCancelled, "Mission cancelled")
	if err != nil {
		return nil, err
	}
	return s.missionView(item), nil
}
