package gateway

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/costs"
	"github.com/soulacy/soulacy/internal/workspacepolicy"
)

type workspaceUsageReport struct {
	Period          string                `json:"period"`
	GeneratedAt     time.Time             `json:"generated_at"`
	MonitoringMode  string                `json:"monitoring_mode"`
	Health          string                `json:"health"`
	Totals          workspaceUsageTotals  `json:"totals"`
	Limits          fiber.Map             `json:"limits"`
	TopUsers        []costs.ChargebackRow `json:"top_users"`
	TopAgents       []costs.AgentCost     `json:"top_agents"`
	TopModels       []costs.ChargebackRow `json:"top_models"`
	Recommendations []string              `json:"recommendations"`
}

type workspaceUsageTotals struct {
	Calls               int     `json:"calls"`
	FailedCalls         int     `json:"failed_calls"`
	RejectedCalls       int     `json:"rejected_calls"`
	UnknownPricedCalls  int     `json:"unknown_priced_calls"`
	TotalTokens         int64   `json:"total_tokens"`
	EstimatedCostUSD    float64 `json:"estimated_cost_usd"`
	AccountingCoverage  float64 `json:"accounting_coverage"`
	HighestUserTokens   int64   `json:"highest_user_tokens"`
	SuggestedUserTokens int64   `json:"suggested_user_tokens"`
}

// handleGetWorkspaceUsageReport turns the prompt-free cost ledger into the
// context an administrator needs before choosing a hard ceiling. It is kept
// workspace-scoped by costScope and deliberately contains no prompt content.
func (s *Server) handleGetWorkspaceUsageReport(c *fiber.Ctx) error {
	identity, err := workspacePolicyAdmin(c)
	if err != nil {
		return err
	}
	if s.costStore == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "cost and token monitoring is not enabled")
	}
	since, label, err := parseCostSince(c.Query("since", "24h"))
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if label == "" {
		label = "all"
	}

	stored := workspacepolicy.Policy{WorkspaceID: identity.WorkspaceID()}
	if s.workspacePolicies != nil {
		stored, err = s.workspacePolicies.Get(c.UserContext(), identity.WorkspaceID())
		if err != nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "the workspace policy could not be read")
		}
	}
	scope := s.costs(c)
	stats, err := scope.StatsSince(c.Context(), since)
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "workspace usage could not be summarized")
	}
	users, err := scope.Chargeback(c.Context(), since, []string{"user"})
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "workspace user usage could not be summarized")
	}
	models, err := scope.Chargeback(c.Context(), since, []string{"provider", "model"})
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "workspace model usage could not be summarized")
	}
	agents, err := scope.SumByAgent(c.Context(), since)
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "workspace agent usage could not be summarized")
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].TotalTokens > agents[j].TotalTokens })

	highestUser := int64(0)
	for _, row := range users {
		if row.TotalTokens > highestUser {
			highestUser = row.TotalTokens
		}
	}
	coverage := 1.0
	if stats.Calls > 0 {
		coverage = float64(stats.AttributedCalls) / float64(stats.Calls)
	}
	limits := s.effectiveWorkspaceLimits(stored)
	monitorOnly := effectiveLimitsAreMonitorOnly(limits)
	health := "healthy"
	if stats.RejectedCalls > 0 {
		health = "blocked"
	} else if stats.FailedCalls > 0 || stats.UnknownPriced > 0 || coverage < .999 {
		health = "attention"
	}
	recommendations := usageRecommendations(stats, monitorOnly, highestUser, limits)

	return c.JSON(workspaceUsageReport{
		Period: label, GeneratedAt: time.Now().UTC(), MonitoringMode: map[bool]string{true: "monitor_only", false: "hard_limits_active"}[monitorOnly],
		Health: health,
		Totals: workspaceUsageTotals{
			Calls: stats.Calls, FailedCalls: stats.FailedCalls, RejectedCalls: stats.RejectedCalls,
			UnknownPricedCalls: stats.UnknownPriced, TotalTokens: stats.TotalTokens,
			EstimatedCostUSD: float64(stats.CostMicros) / 1_000_000, AccountingCoverage: coverage,
			HighestUserTokens: highestUser, SuggestedUserTokens: suggestedTokenCeilingForPeriod(label, highestUser),
		},
		Limits: limits, TopUsers: firstChargeback(users, 10), TopAgents: firstAgents(agents, 10),
		TopModels: firstChargeback(models, 10), Recommendations: recommendations,
	})
}

func effectiveLimitsAreMonitorOnly(limits fiber.Map) bool {
	for _, key := range []string{"daily_usd", "monthly_usd", "daily_tokens", "per_user_daily_tokens"} {
		switch value := limits[key].(type) {
		case int:
			if value > 0 {
				return false
			}
		case int64:
			if value > 0 {
				return false
			}
		case float64:
			if value > 0 {
				return false
			}
		}
	}
	return true
}

func suggestedTokenCeilingForPeriod(period string, observed int64) int64 {
	if period != "24h" || observed <= 0 {
		return 0
	}
	// Two times the observed high-water mark, rounded up to a readable 50k.
	const step = int64(50_000)
	return int64(math.Ceil(float64(observed*2)/float64(step))) * step
}

func usageRecommendations(stats costs.UsageStats, monitorOnly bool, highestUser int64, limits fiber.Map) []string {
	out := make([]string, 0, 4)
	if monitorOnly {
		out = append(out, "Workspace hard limits are off; usage is being measured without Soulacy blocking calls. Deployment and provider safeguards still apply.")
	} else {
		out = append(out, "One or more workspace hard limits are active. Crossing an effective limit stops the model call immediately.")
	}
	if stats.RejectedCalls > 0 {
		out = append(out, fmt.Sprintf("%d model calls were rejected in this period. Review the active limits before users retry.", stats.RejectedCalls))
	}
	if stats.UnknownPriced > 0 {
		out = append(out, fmt.Sprintf("%d calls have unknown pricing; token counts are complete, but estimated spend is understated.", stats.UnknownPriced))
	}
	if highestUser > 0 {
		if current, ok := limits["per_user_daily_tokens"].(int64); ok && current > 0 && highestUser*5 >= current*4 {
			out = append(out, fmt.Sprintf("The busiest user consumed %d tokens in this period, at least 80%% of the effective per-user ceiling.", highestUser))
		}
	}
	if len(out) == 1 && stats.Calls == 0 {
		out = append(out, "No model calls were recorded in this period. Keep monitoring on until normal usage is visible before setting a hard limit.")
	}
	return out
}

func firstChargeback(rows []costs.ChargebackRow, limit int) []costs.ChargebackRow {
	if len(rows) > limit {
		rows = rows[:limit]
	}
	if rows == nil {
		return []costs.ChargebackRow{}
	}
	return rows
}

func firstAgents(rows []costs.AgentCost, limit int) []costs.AgentCost {
	if len(rows) > limit {
		rows = rows[:limit]
	}
	if rows == nil {
		return []costs.AgentCost{}
	}
	return rows
}
