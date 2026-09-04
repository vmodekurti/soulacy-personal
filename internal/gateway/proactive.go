package gateway

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/proactive"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// handleProactiveSuggestions surfaces the assistant's proactive opportunity
// detector: it scans recent activity across all agents and returns a small
// prioritized list of automations worth considering (schedule a repeated manual
// task, review a flaky agent, enable learning). Read-only aggregation over the
// action log — safe to call on demand from the Dashboard.
func (s *Server) handleProactiveSuggestions(c *fiber.Ctx) error {
	if s.actions == nil {
		return c.JSON(fiber.Map{"enabled": false, "suggestions": []proactive.Suggestion{}})
	}
	perAgent, _ := strconv.Atoi(c.Query("limit", "500"))
	if perAgent <= 0 || perAgent > 5000 {
		perAgent = 500
	}

	defs := s.loader.All()
	snapshots := make(map[string]proactive.AgentSnapshot, len(defs))
	var events []message.Event
	for _, def := range defs {
		if def == nil {
			continue
		}
		hasSchedule := def.Trigger == agent.TriggerCron ||
			(def.Schedule != nil && strings.TrimSpace(def.Schedule.Cron) != "")
		snapshots[def.ID] = proactive.AgentSnapshot{
			ID:              def.ID,
			Name:            def.Name,
			HasSchedule:     hasSchedule,
			LearningEnabled: def.Learning.Enabled,
		}
		evs, err := s.actions.Tail(def.ID, perAgent)
		if err != nil {
			continue
		}
		events = append(events, evs...)
	}

	suggestions := proactive.Detect(events, snapshots)

	// Optional cap so the UI stays focused on the top opportunities.
	if max, _ := strconv.Atoi(c.Query("max", "6")); max > 0 && len(suggestions) > max {
		suggestions = suggestions[:max]
	}
	return c.JSON(fiber.Map{"enabled": true, "suggestions": suggestions})
}
