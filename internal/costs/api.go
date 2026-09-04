package costs

import (
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// API exposes cost-tracking data over HTTP.
type API struct {
	store *Store
	log   *zap.Logger
}

// NewAPI creates an API backed by the given Store.
func NewAPI(s *Store, log *zap.Logger) *API {
	return &API{store: s, log: log}
}

// HandleGetCosts handles GET /api/v1/costs
//
// Query params:
//
//	?since=24h          — duration string (e.g. "1h", "7d", "30m")
//	?since=2026-01-01   — RFC3339 / date string
//	?agent_id=<id>      — optional filter (returns only that agent's rows)
//
// Response: {"by_agent": [...AgentCost...], "period": "24h", "generated_at": "..."}
func (a *API) HandleGetCosts(c *fiber.Ctx) error {
	if a.store == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "cost tracking not enabled"})
	}
	since, periodLabel, err := parseSince(c.Query("since", ""))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": fmt.Sprintf("invalid since param: %v", err)})
	}

	rows, err := a.store.SumByAgent(c.Context(), since)
	if err != nil {
		a.log.Error("costs: SumByAgent failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}

	// Optional agent_id filter applied in-process (cheap — result set is small).
	agentFilter := c.Query("agent_id", "")
	if agentFilter != "" {
		filtered := rows[:0]
		for _, r := range rows {
			if r.AgentID == agentFilter {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}

	// Ensure we always return an array, never null.
	if rows == nil {
		rows = []AgentCost{}
	}

	return c.JSON(fiber.Map{
		"by_agent":     rows,
		"period":       periodLabel,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// HandleGetAgentCosts handles GET /api/v1/costs/:agent_id
//
// Query params:
//
//	?since=24h  — same as HandleGetCosts
//
// Response: {"agent_id": "...", "by_session": [...SessionCost...], "total": {...}}
func (a *API) HandleGetAgentCosts(c *fiber.Ctx) error {
	if a.store == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "cost tracking not enabled"})
	}
	agentID := c.Params("agent_id")
	if agentID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "agent_id is required"})
	}

	since, _, err := parseSince(c.Query("since", ""))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": fmt.Sprintf("invalid since param: %v", err)})
	}

	sessions, err := a.store.SumBySession(c.Context(), agentID, since)
	if err != nil {
		a.log.Error("costs: SumBySession failed", zap.String("agent_id", agentID), zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	if sessions == nil {
		sessions = []SessionCost{}
	}

	// Compute totals across all sessions.
	var totalTokens int
	var totalCost float64
	for _, s := range sessions {
		totalTokens += s.TotalTokens
		totalCost += s.CostUSD
	}

	return c.JSON(fiber.Map{
		"agent_id":   agentID,
		"by_session": sessions,
		"total": fiber.Map{
			"total_tokens": totalTokens,
			"cost_usd":     totalCost,
		},
	})
}

// parseSince parses the ?since query parameter.
// Accepts:
//   - empty string   → zero time (all records), period label ""
//   - duration like "24h", "7d", "30m" → time.Now().Add(-d), period label = input
//   - date/datetime string → parsed via RFC3339 or "2006-01-02"
func parseSince(s string) (time.Time, string, error) {
	if s == "" {
		return time.Time{}, "", nil
	}

	// Try as Go duration first (handles "30m", "24h", "168h", etc.).
	// Also handle informal "Xd" for X days which Go doesn't natively support.
	durationStr := s
	if len(s) > 1 && s[len(s)-1] == 'd' {
		// Convert days to hours for time.ParseDuration.
		days := s[:len(s)-1]
		durationStr = days + "h"
		// But we want N*24 hours. Parse the number directly.
		var days64 float64
		if _, err := fmt.Sscanf(days, "%f", &days64); err == nil {
			d := time.Duration(days64 * 24 * float64(time.Hour))
			return time.Now().Add(-d), s, nil
		}
	}
	if d, err := time.ParseDuration(durationStr); err == nil {
		if d < 0 {
			d = -d
		}
		return time.Now().Add(-d), s, nil
	}

	// Try RFC3339.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, s, nil
	}

	// Try plain date "2006-01-02".
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, s, nil
	}

	return time.Time{}, "", fmt.Errorf("cannot parse %q as duration or date", s)
}
