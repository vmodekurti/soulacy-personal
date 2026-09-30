package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/authconnections"
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

type executionPlanRequest struct {
	Goal        string            `json:"goal"`
	KnownInputs map[string]string `json:"known_inputs"`
}

func (s *Server) handlePlanExecution(c *fiber.Ctx) error {
	var body executionPlanRequest
	if c.BodyParser(&body) != nil {
		return s.errMsg(c, 400, "invalid request body")
	}
	workspaceID, subject, _ := authenticatedConnectionActor(c)
	plan, err := s.planExecution(c.UserContext(), workspaceID, subject, "", body.Goal, body.KnownInputs)
	if err != nil {
		return s.missionError(c, err)
	}
	return c.JSON(fiber.Map{"execution_plan": plan})
}

func (s *Server) handlePlanMissionExecution(c *fiber.Ctx) error {
	var body executionPlanRequest
	if len(c.Body()) > 0 && c.BodyParser(&body) != nil {
		return s.errMsg(c, 400, "invalid request body")
	}
	workspaceID, subject, _ := authenticatedConnectionActor(c)
	plan, err := s.planExecution(c.UserContext(), workspaceID, subject, c.Params("id"), body.Goal, body.KnownInputs)
	if err != nil {
		return s.missionError(c, err)
	}
	item, err := s.missionStore.Get(c.UserContext(), workspaceID, subject, c.Params("id"))
	if err != nil {
		return s.missionError(c, err)
	}
	return c.JSON(fiber.Map{"execution_plan": plan, "mission": s.missionView(item)})
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
	executionPlan, planErr := s.buildExecutionPlan(ctx, workspaceID, subject, normalized.Objective, nil)
	prompt := fmt.Sprintf("Standing mission: %s\n\nObjective: %s\n\nFinish line: %s\n\nOn every run, advance this goal. Return a concise evidence-backed progress update, a blocker that needs the user when one exists, or an empty response when nothing meaningful changed. Never treat this mission as permission to bypass approvals.%s", normalized.Title, normalized.Objective, normalized.FinishLine, executionPlanPrompt(executionPlan, planErr))
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
	if planErr == nil {
		if planned, saveErr := s.missionStore.SaveExecutionPlan(ctx, workspaceID, subject, created.ID, executionPlan); saveErr == nil {
			created = planned
		}
	}
	return created, nil
}

func executionPlanPrompt(plan missions.ExecutionPlan, planErr error) string {
	if planErr != nil {
		return ""
	}
	var text strings.Builder
	text.WriteString("\n\nExecution approach: ")
	text.WriteString(plan.Summary)
	text.WriteString("\nRoute: ")
	text.WriteString(plan.Route)
	for _, requirement := range plan.RequiredInputs {
		if requirement.Status == "needed" || requirement.Status == "secure_setup" {
			fmt.Fprintf(&text, "\nRequired before execution: %s (%s).", requirement.Label, requirement.Status)
		}
	}
	for _, gap := range plan.CapabilityGaps {
		text.WriteString("\nCapability gap: ")
		text.WriteString(gap)
	}
	for _, approval := range plan.ApprovalCheckpoints {
		text.WriteString("\nMandatory approval: ")
		text.WriteString(approval)
	}
	text.WriteString("\nNever request credential values or payment numbers in chat. Report missing input or setup as a blocker instead of guessing.")
	return text.String()
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
		"execution_plan": item.ExecutionPlan,
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

func (s *Server) planExecution(ctx context.Context, workspaceID, subject, missionID, goal string, knownInputs map[string]string) (missions.ExecutionPlan, error) {
	if s.missionStore == nil {
		return missions.ExecutionPlan{}, errors.New("mission service is unavailable")
	}
	if missionID != "" {
		item, err := s.missionStore.Get(ctx, workspaceID, subject, missionID)
		if err != nil {
			return missions.ExecutionPlan{}, err
		}
		if strings.TrimSpace(goal) == "" {
			goal = item.Objective
		}
	}
	plan, err := s.buildExecutionPlan(ctx, workspaceID, subject, goal, knownInputs)
	if err != nil {
		return missions.ExecutionPlan{}, err
	}
	if missionID != "" {
		if _, err := s.missionStore.SaveExecutionPlan(ctx, workspaceID, subject, missionID, plan); err != nil {
			return missions.ExecutionPlan{}, err
		}
	}
	return plan, nil
}

func (s *Server) buildExecutionPlan(ctx context.Context, workspaceID, subject, goal string, knownInputs map[string]string) (missions.ExecutionPlan, error) {
	inventory := missions.CapabilityInventory{PublicWeb: true, Connectors: []missions.InventoryConnector{}, WebsiteAccess: []missions.InventoryWebsiteAccess{}, Tools: []string{}, Skills: []string{}}
	if s.connectorStore != nil {
		items, err := s.connectorStore.List(ctx, workspaceID, subject)
		if err != nil {
			return missions.ExecutionPlan{}, err
		}
		for _, item := range items {
			domains := make([]string, 0, len(item.Sites))
			for _, site := range item.Sites {
				domains = append(domains, site.Domain)
			}
			inventory.Connectors = append(inventory.Connectors, missions.InventoryConnector{Name: item.Name, Category: item.Category, Domains: domains})
		}
	}
	if s.authConnections != nil {
		connections, err := s.authConnections.ListVisible(ctx, workspaceID, subject)
		if err != nil {
			return missions.ExecutionPlan{}, err
		}
		for _, connection := range connections {
			inventory.WebsiteAccess = append(inventory.WebsiteAccess, missions.InventoryWebsiteAccess{Name: connection.Name, Domains: connection.AllowedDomains,
				Ready: connection.Status == authconnections.StatusReady && connection.HasSecret})
		}
	}
	if s.mcp != nil {
		for _, tool := range s.mcp.AllTools() {
			inventory.Tools = append(inventory.Tools, tool.FullName())
		}
	}
	if s.skillLoader != nil {
		for _, skill := range s.skillLoader.All() {
			inventory.Skills = append(inventory.Skills, skill.Name)
		}
	}
	inventory.BrowserAutomation, inventory.BrowserDetail = missions.DetectBrowserAutomation(inventory.Tools)
	return missions.BuildExecutionPlan(goal, knownInputs, inventory)
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

func (s *Server) PlanActionForGenie(ctx context.Context, goal, missionID string, knownInputs map[string]string) (map[string]any, error) {
	plan, err := s.planExecution(ctx, runtime.PersonalWorkspaceID, "admin", strings.TrimSpace(missionID), goal, knownInputs)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"execution_plan": plan,
		"next":           "Ask only for requirements marked needed. Use prepare_website_access for secure_setup when the user agrees. Never collect credentials or payment numbers in chat.",
	}, nil
}

func (s *Server) PrepareWebsiteAccessForGenie(ctx context.Context, name, baseURL string) (map[string]any, error) {
	if s.authConnections == nil || s.credVault == nil {
		return nil, errors.New("website access is unavailable")
	}
	baseURL, domains, err := normalizeConnectionBoundary(baseURL, nil)
	if err != nil {
		return nil, err
	}
	connections, err := s.authConnections.ListVisible(ctx, runtime.PersonalWorkspaceID, "admin")
	if err != nil {
		return nil, err
	}
	for _, connection := range connections {
		for _, existingDomain := range connection.AllowedDomains {
			if existingDomain == domains[0] {
				return map[string]any{
					"connection": connection, "setup_href": "#websites",
					"message": "Website Access already has a domain-restricted connection for this site. Open Website Access to sign in or refresh it.",
				}, nil
			}
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = domains[0]
	}
	if len(name) > 120 || strings.ContainsAny(name, "\r\n\x00") {
		return nil, errors.New("website access name must be a single line of 120 characters or fewer")
	}
	connection, err := s.authConnections.Create(ctx, authconnections.CreateInput{
		WorkspaceID: runtime.PersonalWorkspaceID, OwnerSubject: "admin", Scope: authconnections.ScopeUser,
		Kind: authconnections.KindBrowser, Name: name, BaseURL: baseURL, AllowedDomains: domains,
	})
	if err != nil {
		return nil, err
	}
	if err := s.authConnections.ReplaceAgentGrants(ctx, runtime.PersonalWorkspaceID, connection.ID, []string{runtime.GenieAgentID}); err != nil {
		_ = s.authConnections.Delete(ctx, runtime.PersonalWorkspaceID, connection.ID)
		return nil, err
	}
	connection, _ = s.authConnections.Get(ctx, runtime.PersonalWorkspaceID, connection.ID)
	return map[string]any{
		"connection": connection, "setup_href": "#websites",
		"message": "A domain-restricted Website Access connection is ready. Open Website Access and sign in directly on the provider's page. Do not send credentials to Genie.",
	}, nil
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
