package gateway

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/actionlog"
	"github.com/soulacy/soulacy/internal/costs"
	"github.com/soulacy/soulacy/pkg/message"
)

// Run-level observability (Story 7, milestone M1). Combines the costs store
// (tokens/cost/model per session) with the actionlog event trail (tool calls,
// duration, failure) into one compact metrics object — no new storage.

// sessionStatser is satisfied by storage backends whose action log can
// aggregate per-session stats (storagesqlite.ActionLog via the embedded
// actionlog.Logger). Checked at request time so other backends degrade
// gracefully to costs-only metrics.
type sessionStatser interface {
	SessionStats(agentID, sessionID string) (actionlog.SessionStats, error)
}

type opsSummarizer interface {
	OpsSummary(since time.Time, window string, limit int) (actionlog.OpsSummary, error)
}

type eventQuerier interface {
	QueryEvents(agentID, sessionID string, limit int, allowed map[string]bool) ([]message.Event, error)
}

// handleRunMetrics handles GET /api/v1/runs/:session_id/metrics?agent_id=
func (s *Server) handleRunMetrics(c *fiber.Ctx) error {
	if s.costStore == nil && s.actions == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "run metrics not available (no cost store or action log)",
		})
	}
	sessionID := c.Params("session_id")
	agentID := c.Query("agent_id")

	var (
		cm        costs.SessionMetrics
		haveCosts bool
		st        actionlog.SessionStats
		haveTrail bool
	)
	if s.costStore != nil {
		m, found, err := s.costStore.SessionMetrics(c.Context(), sessionID)
		if err != nil {
			s.log.Warn("run metrics: costs query failed")
		} else if found {
			cm = m
			haveCosts = true
		}
	}
	if s.actions != nil {
		if sp, ok := s.actions.(sessionStatser); ok {
			got, err := sp.SessionStats(agentID, sessionID)
			if err != nil {
				s.log.Warn("run metrics: session stats query failed")
			} else if got.Events > 0 {
				st = got
				haveTrail = true
			}
		}
	}
	if !haveCosts && !haveTrail {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "no metrics recorded for this session",
		})
	}

	out := fiber.Map{
		"session_id":    sessionID,
		"provider":      cm.Provider,
		"model":         cm.Model,
		"llm_calls":     cm.LLMCalls,
		"prompt_tokens": cm.PromptTokens,
		"comp_tokens":   cm.CompTokens,
		"total_tokens":  cm.TotalTokens,
		"cost_usd":      cm.CostUSD,
		"tool_calls":    st.ToolCalls,
		"events":        st.Events,
	}
	if st.LastError != "" {
		out["failure"] = st.LastError
	}

	// Duration: prefer the event trail (covers the whole run including tool
	// time); fall back to the LLM call span when no events were recorded.
	switch {
	case haveTrail:
		out["started_at"] = st.FirstEvent
		out["ended_at"] = st.LastEvent
		out["duration_ms"] = st.LastEvent.Sub(st.FirstEvent).Milliseconds()
	case haveCosts:
		out["started_at"] = cm.FirstCallAt
		out["ended_at"] = cm.LastCallAt
		out["duration_ms"] = cm.LastCallAt.Sub(cm.FirstCallAt).Milliseconds()
	}
	return c.JSON(out)
}

// handleOpsSummary handles GET /api/v1/runs/ops-summary?window=24h.
// It is backed by the durable action log so it remains accurate even after
// per-agent JSONL files rotate or the Activity view is filtered.
func (s *Server) handleOpsSummary(c *fiber.Ctx) error {
	if s.actions == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "ops summary not available (action log disabled)",
		})
	}
	sp, ok := s.actions.(opsSummarizer)
	if !ok {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "ops summary requires a durable action log backend",
		})
	}
	window := c.Query("window", "24h")
	since, label, err := parseCostSince(window)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	limit := c.QueryInt("limit", 8)
	summary, err := sp.OpsSummary(since, label, limit)
	if err != nil {
		s.log.Warn("ops summary: actionlog query failed")
		return s.errMsg(c, fiber.StatusInternalServerError, "internal error")
	}
	out := fiber.Map{
		"generated_at":       summary.GeneratedAt,
		"since":              summary.Since,
		"window":             summary.Window,
		"total_runs":         summary.TotalRuns,
		"successful_runs":    summary.SuccessfulRuns,
		"failed_runs":        summary.FailedRuns,
		"incomplete_runs":    summary.IncompleteRuns,
		"failure_rate":       summary.FailureRate,
		"incomplete_rate":    summary.IncompleteRate,
		"avg_duration_ms":    summary.AvgDurationMS,
		"p95_duration_ms":    summary.P95DurationMS,
		"max_duration_ms":    summary.MaxDurationMS,
		"total_events":       summary.TotalEvents,
		"tool_calls":         summary.ToolCalls,
		"top_failing_agents": summary.TopFailing,
		"top_errors":         summary.TopErrors,
		"recent_failures":    summary.RecentFailures,
	}
	if s.costStore != nil {
		rows, err := s.costStore.SumByAgent(c.Context(), since)
		if err != nil {
			s.log.Warn("ops summary: cost query failed")
		} else {
			var tokens int
			var usd float64
			for _, row := range rows {
				tokens += row.TotalTokens
				usd += row.CostUSD
			}
			out["total_tokens"] = tokens
			out["cost_usd"] = usd
		}
	}
	return c.JSON(out)
}

// handleRunEvents handles GET /api/v1/runs/events and returns durable events
// across all agents by default. Optional agent_id, session_id, limit, and types
// filters let Activity, Schedule, and support tooling inspect the same source
// of truth instead of depending on per-agent JSONL tails.
func (s *Server) handleRunEvents(c *fiber.Ctx) error {
	if s.actions == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "run events not available (action log disabled)",
		})
	}
	q, ok := s.actions.(eventQuerier)
	if !ok {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "run events require a durable action log backend",
		})
	}
	limit := c.QueryInt("limit", 500)
	if limit <= 0 {
		limit = 500
	}
	if limit > 10000 {
		limit = 10000
	}
	allowed := map[string]bool{}
	if typesParam := c.Query("types", ""); typesParam != "" {
		for _, t := range strings.Split(typesParam, ",") {
			if t = strings.TrimSpace(t); t != "" {
				allowed[t] = true
			}
		}
	}
	events, err := q.QueryEvents(c.Query("agent_id"), c.Query("session_id"), limit, allowed)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	for i := range events {
		events[i].Payload = compactActionPayload(events[i].Payload, 64*1024)
	}
	return c.JSON(fiber.Map{
		"agent_id":   c.Query("agent_id"),
		"session_id": c.Query("session_id"),
		"events":     events,
		"count":      len(events),
		"durable":    true,
	})
}

// handleActivityRunning handles GET /api/v1/activity/running and returns the
// EventHub's per-session heartbeat snapshot: every in-flight session with
// elapsed / silent-for timers and a `hung` flag when the silent window has
// crossed the tracker's threshold. Serves the Activity page's "Running now"
// strip (E4c — Cohort E Activity failure handling). The response is safe to
// poll at a few-second cadence: it's an in-memory map snapshot with no I/O.
func (s *Server) handleActivityRunning(c *fiber.Ctx) error {
	if s.hub == nil {
		return c.JSON(fiber.Map{
			"sessions":       []RunningSession{},
			"hung_threshold": int64(defaultHungSilentThreshold / 1000000000), // seconds
		})
	}
	sessions := s.hub.RunningSessions()
	hung := 0
	for _, sess := range sessions {
		if sess.Hung {
			hung++
		}
	}
	return c.JSON(fiber.Map{
		"sessions":       sessions,
		"count":          len(sessions),
		"hung_count":     hung,
		"hung_threshold": int64(defaultHungSilentThreshold / 1000000000), // seconds
	})
}
