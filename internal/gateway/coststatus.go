package gateway

import (
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/costs"
)

type costReadinessCheck struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type costReadiness struct {
	Status                 string               `json:"status"`
	Score                  int                  `json:"score"`
	Ready                  int                  `json:"ready"`
	Total                  int                  `json:"total"`
	PricingRules           int                  `json:"pricing_rules"`
	DailyBudgetUSD         float64              `json:"daily_budget_usd"`
	MonthlyBudgetUSD       float64              `json:"monthly_budget_usd"`
	AlertThreshold         float64              `json:"alert_threshold"`
	Last24hUSD             float64              `json:"last_24h_usd"`
	Last30dUSD             float64              `json:"last_30d_usd"`
	ReservedUSD            float64              `json:"reserved_usd"`
	UnknownPricedCalls     int                  `json:"unknown_priced_calls"`
	AccountingCoverage     float64              `json:"accounting_coverage"`
	RejectedCalls          int                  `json:"rejected_calls"`
	RetryAttempts          int64                `json:"retry_attempts"`
	EnforcementMode        string               `json:"enforcement_mode"`
	UnknownPricing         string               `json:"unknown_pricing"`
	ForecastMonthlyUSD     float64              `json:"forecast_monthly_usd"`
	ReconciliationVariance float64              `json:"reconciliation_variance"`
	Checks                 []costReadinessCheck `json:"checks"`
	NextActions            []string             `json:"next_actions"`
}

func (s *Server) handleCostStatus(c *fiber.Ctx) error {
	return c.JSON(s.costReadiness(c))
}

// handleCostEstimate performs a prompt-free preflight using caller-supplied
// token counts. It never accepts or persists prompt text.
func (s *Server) handleCostEstimate(c *fiber.Ctx) error {
	var req struct {
		Provider        string `json:"provider"`
		Model           string `json:"model"`
		InputTokens     int    `json:"input_tokens"`
		MaxOutputTokens int    `json:"max_output_tokens"`
	}
	if err := c.BodyParser(&req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if req.InputTokens < 0 || req.MaxOutputTokens < 0 {
		return s.errMsg(c, fiber.StatusBadRequest, "token counts must be non-negative")
	}
	provider := strings.TrimSpace(req.Provider)
	model := strings.TrimSpace(req.Model)
	defaultProvider, defaultModel := s.defaultAgentLLM(c)
	if provider == "" {
		provider = defaultProvider
	}
	if model == "" {
		if provider == defaultProvider {
			model = defaultModel
		} else if pc, ok := s.effectiveWorkspaceConfig(s.workspaceSettingsFor(c)).LLM.Providers[provider]; ok {
			model = strings.TrimSpace(pc.Model)
		}
	}
	if req.MaxOutputTokens == 0 {
		req.MaxOutputTokens = s.config().Costs.DefaultMaxOutputTokens
	}
	if ceiling := s.config().Costs.MaxOutputTokensCeiling; ceiling > 0 && req.MaxOutputTokens > ceiling {
		req.MaxOutputTokens = ceiling
	}
	table := make(costs.PriceTable, len(s.config().Costs.Pricing))
	for key, price := range s.config().Costs.Pricing {
		table[costs.NormalizePriceKey(key)] = costs.Pricing{
			InputPerMTok: price.InputPerMTok, OutputPerMTok: price.OutputPerMTok,
			CachedInputPerMTok: price.CachedInputPerMTok, CacheWritePerMTok: price.CacheWritePerMTok,
			ReasoningPerMTok: price.ReasoningPerMTok, Source: price.Source,
			EffectiveDate: price.EffectiveDate, Version: price.Version,
		}
	}
	usd, micros, status := costs.EstimateDetailed(table, provider, model, costs.UsageDimensions{
		InputTokens: req.InputTokens, OutputTokens: req.MaxOutputTokens,
	})
	threshold := s.config().Costs.ConfirmationThresholdUSD
	return c.JSON(fiber.Map{
		"provider": provider, "model": model, "input_tokens": req.InputTokens,
		"max_output_tokens": req.MaxOutputTokens, "estimated_tokens": req.InputTokens + req.MaxOutputTokens,
		"estimated_usd": usd, "estimated_micros": micros, "pricing_status": status,
		"pricing_version":            costs.PricingVersion(table, provider, model),
		"confirmation_threshold_usd": threshold, "confirmation_required": threshold > 0 && usd > threshold,
	})
}

func (s *Server) costReadiness(c *fiber.Ctx) costReadiness {
	pricingRules := 0
	dailyBudget := 0.0
	monthlyBudget := 0.0
	threshold := 0.8
	enforcementMode := "off"
	unknownPricing := "allow"
	if s != nil && s.config() != nil {
		pricingRules = len(s.config().Costs.Pricing)
		dailyBudget = s.config().Costs.DailyBudgetUSD
		monthlyBudget = s.config().Costs.MonthlyBudgetUSD
		if s.config().Costs.AlertThreshold > 0 {
			threshold = s.config().Costs.AlertThreshold
		}
		enforcementMode = s.config().Costs.EnforcementMode
		unknownPricing = s.config().Costs.UnknownPricing
	}

	last24h := 0.0
	last30d := 0.0
	reservedUSD := 0.0
	unknownPricedCalls := 0
	accountingCoverage := 1.0
	rejectedCalls := 0
	var retryAttempts int64
	forecastMonthlyUSD := 0.0
	reconciliationVariance := 0.0
	reconciliationConfigured := false
	reconciliationHealthy := false
	reconciliationThreshold := 0.1
	tracking := s != nil && s.costStore != nil
	if s != nil && s.config() != nil {
		reconciliationConfigured = s.config().Costs.Reconciliation.Enabled && len(s.config().Costs.Reconciliation.Providers) > 0
		if s.config().Costs.Reconciliation.VarianceAlertThreshold > 0 {
			reconciliationThreshold = s.config().Costs.Reconciliation.VarianceAlertThreshold
		}
	}
	if tracking {
		now := time.Now().UTC()
		dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		last24h = s.sumCostSince(c, dayStart)
		last30d = s.sumCostSince(c, monthStart)
		elapsedDays := now.Sub(monthStart).Hours() / 24
		if elapsedDays < 1 {
			elapsedDays = 1
		}
		daysInMonth := float64(time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day())
		forecastMonthlyUSD = last30d / elapsedDays * daysInMonth
		if reserved, err := s.costs(c).ReservedCostMicros(c.Context(), now); err == nil {
			reservedUSD = float64(reserved) / 1_000_000
		}
		if stats, err := s.costs(c).StatsSince(c.Context(), monthStart); err == nil {
			unknownPricedCalls = stats.UnknownPriced
			rejectedCalls = stats.RejectedCalls
			if stats.Calls > 0 {
				accountingCoverage = float64(stats.AttributedCalls) / float64(stats.Calls)
			}
			if admitted := stats.Calls - stats.RejectedCalls; stats.Attempts > int64(admitted) {
				retryAttempts = stats.Attempts - int64(admitted)
			}
		}
		if items, err := s.costs(c).ListReconciliations(c.Context(), 20); err == nil && len(items) > 0 {
			reconciliationHealthy = true
			for _, item := range items {
				if ratio := item.VarianceRatio(); ratio > reconciliationVariance {
					reconciliationVariance = ratio
				}
			}
			if reconciliationVariance > reconciliationThreshold {
				reconciliationHealthy = false
			}
		}
	}
	budgetsConfigured := dailyBudget > 0 || monthlyBudget > 0

	checks := []costReadinessCheck{
		{
			Key:    "tracking",
			Label:  "Usage Tracking",
			Status: statusIf(tracking, "ok", "warn"),
			Detail: statusDetail(tracking, "Token and estimated cost usage are persisted.", "Cost store is not enabled; runs cannot be budgeted from usage."),
		},
		{
			Key:    "pricing",
			Label:  "Pricing Rules",
			Status: statusIf(pricingRules > 0, "ok", "warn"),
			Detail: countDetail(pricingRules, "pricing rule", "No pricing rules are configured; usage records will show $0 for unknown models."),
		},
		{
			Key:    "budgets",
			Label:  "Budgets",
			Status: statusIf(budgetsConfigured, "ok", "warn"),
			Detail: costBudgetDetail(dailyBudget, monthlyBudget, threshold) + " Enforcement mode: " + enforcementMode + ".",
		},
		{
			Key: "pricing_coverage", Label: "Pricing Coverage",
			Status: statusIf(unknownPricedCalls == 0, "ok", "warn"),
			Detail: fmt.Sprintf("%d calls in the current billing month have unknown pricing.", unknownPricedCalls),
		},
		{
			Key: "accounting_coverage", Label: "Accounting Coverage",
			Status: statusIf(tracking && accountingCoverage >= 0.999, "ok", "fail"),
			Detail: fmt.Sprintf("%.1f%% of inference outcomes have durable call attribution; %d admission rejections and %d retry attempts this month.", accountingCoverage*100, rejectedCalls, retryAttempts),
		},
		{
			Key:    "daily_threshold",
			Label:  "Daily Guardrail",
			Status: budgetThresholdStatus(dailyBudget, last24h, threshold, tracking),
			Detail: budgetThresholdDetail("24h", dailyBudget, last24h, threshold, tracking),
		},
		{
			Key:    "monthly_threshold",
			Label:  "Monthly Guardrail",
			Status: budgetThresholdStatus(monthlyBudget, last30d, threshold, tracking),
			Detail: budgetThresholdDetail("30d", monthlyBudget, last30d, threshold, tracking),
		},
		{
			Key:    "monthly_forecast",
			Label:  "Monthly Forecast",
			Status: budgetThresholdStatus(monthlyBudget, forecastMonthlyUSD, threshold, tracking),
			Detail: budgetThresholdDetail("forecast month", monthlyBudget, forecastMonthlyUSD, threshold, tracking),
		},
		{
			Key: "reconciliation", Label: "Provider Reconciliation",
			Status: statusIf(reconciliationConfigured && reconciliationHealthy, "ok", "warn"),
			Detail: reconciliationDetail(reconciliationConfigured, reconciliationHealthy, reconciliationVariance),
		},
	}

	ready := 0
	points := 0
	next := make([]string, 0, 4)
	for _, check := range checks {
		switch check.Status {
		case "ok":
			ready++
			points += 100
		case "warn":
			points += 60
			next = append(next, costReadinessNextAction(check.Key))
		default:
			next = append(next, costReadinessNextAction(check.Key))
		}
	}
	score := 0
	if len(checks) > 0 {
		score = points / len(checks)
	}
	if len(next) > 4 {
		next = next[:4]
	}

	return costReadiness{
		Status:                 statusFromScore(score),
		Score:                  score,
		Ready:                  ready,
		Total:                  len(checks),
		PricingRules:           pricingRules,
		DailyBudgetUSD:         dailyBudget,
		MonthlyBudgetUSD:       monthlyBudget,
		AlertThreshold:         threshold,
		Last24hUSD:             last24h,
		Last30dUSD:             last30d,
		ReservedUSD:            reservedUSD,
		UnknownPricedCalls:     unknownPricedCalls,
		AccountingCoverage:     accountingCoverage,
		RejectedCalls:          rejectedCalls,
		RetryAttempts:          retryAttempts,
		EnforcementMode:        enforcementMode,
		UnknownPricing:         unknownPricing,
		ForecastMonthlyUSD:     forecastMonthlyUSD,
		ReconciliationVariance: reconciliationVariance,
		Checks:                 checks,
		NextActions:            compactStrings(next),
	}
}

func reconciliationDetail(configured, healthy bool, variance float64) string {
	if !configured {
		return "Scheduled provider billing reconciliation is not configured."
	}
	if !healthy {
		return fmt.Sprintf("No healthy reconciliation is available or variance is elevated (latest maximum %.1f%%).", variance*100)
	}
	return fmt.Sprintf("Provider billing imports are current; maximum estimate variance is %.1f%%.", variance*100)
}

func (s *Server) sumCostSince(c *fiber.Ctx, since time.Time) float64 {
	if s == nil || s.costStore == nil {
		return 0
	}
	ctx := c.Context()
	rows, err := s.costs(c).SumByAgent(ctx, since)
	if err != nil {
		return 0
	}
	total := 0.0
	for _, row := range rows {
		total += row.CostUSD
	}
	return total
}

func parityOps(providersReady, enabledAgents int, updateManifest string, cost costReadiness, slo sloReadiness, alerts opsAlertReadiness) parityArea {
	if providersReady > 0 && enabledAgents > 0 && updateManifest != "" && cost.Status == "ok" && slo.Status == "ok" && alerts.Status == "ok" {
		return parityArea{Key: "ops", Label: "Ops & Release Confidence", Status: "ok", Score: 95, Detail: "Readiness, doctor, support bundles, action logs, parity harness, updates, cost guardrails, deployment profiles, SLO checks, and ops alert delivery are wired.", Next: "Keep clean-runtime UAT and alert tests in the release checklist.", Benchmark: "Commercial launch", Href: "#dashboard"}
	}
	status := "warn"
	score := 62
	if providersReady == 0 || enabledAgents == 0 {
		status = "fail"
		score = 38
	}
	if cost.Status == "fail" || slo.Status == "fail" {
		status = "fail"
		score = 45
	}
	if cost.Status == "ok" && slo.Status == "ok" && providersReady > 0 && enabledAgents > 0 {
		score = 72
	}
	next := "Run launch checks, configure update manifest, and keep support-bundle download visible."
	if len(cost.NextActions) > 0 {
		next = cost.NextActions[0]
	}
	if len(slo.NextActions) > 0 && (slo.Status == "fail" || cost.Status == "ok") {
		next = slo.NextActions[0]
	}
	if alerts.Status != "ok" {
		if status == "warn" && cost.Status == "ok" && slo.Status == "ok" {
			score = 78
		}
		if len(alerts.NextActions) > 0 {
			next = alerts.NextActions[0]
		}
	}
	return parityArea{Key: "ops", Label: "Ops & Release Confidence", Status: status, Score: score, Detail: fmt.Sprintf("Diagnostics, run metrics, and support bundles exist; cost guardrails are %s, SLO status is %s, and ops alert delivery is %s.", cost.Status, slo.Status, alerts.Status), Next: next, Benchmark: "Commercial launch", Href: "#dashboard"}
}

func costBudgetDetail(daily, monthly, threshold float64) string {
	if daily <= 0 && monthly <= 0 {
		return "No daily or monthly cost budget configured."
	}
	return fmt.Sprintf("Budgets configured: daily $%.2f, monthly $%.2f, alert at %.0f%%.", daily, monthly, threshold*100)
}

func budgetThresholdStatus(limit, spent, threshold float64, tracking bool) string {
	if limit <= 0 {
		return "warn"
	}
	if !tracking {
		return "warn"
	}
	if spent >= limit {
		return "fail"
	}
	if spent >= limit*threshold {
		return "warn"
	}
	return "ok"
}

func budgetThresholdDetail(label string, limit, spent, threshold float64, tracking bool) string {
	if limit <= 0 {
		return fmt.Sprintf("No %s budget configured.", label)
	}
	if !tracking {
		return fmt.Sprintf("%s budget is $%.2f, but usage tracking is unavailable.", label, limit)
	}
	return fmt.Sprintf("%s spend is $%.2f of $%.2f (alert at %.0f%%).", label, spent, limit, threshold*100)
}

func costReadinessNextAction(key string) string {
	switch key {
	case "tracking":
		return "Enable the cost store so token and dollar usage are persisted."
	case "pricing":
		return "Add provider/model pricing rules in Config."
	case "budgets":
		return "Set daily and monthly cost budgets in Config."
	case "accounting_coverage":
		return "Restore central inference accounting coverage to 100% and inspect unattributed call paths."
	case "daily_threshold":
		return "Set a daily budget and alert threshold for run-away agents."
	case "monthly_threshold":
		return "Set a monthly budget and review top-cost agents weekly."
	case "reconciliation":
		return "Enable provider billing reconciliation and investigate estimate variance above the configured threshold."
	default:
		return "Review cost controls in Config."
	}
}
