package gateway

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/costs"
)

func TestCostStatusReportsBudgetsAndUsage(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfg.Costs.DailyBudgetUSD = 10
	s.cfg.Costs.MonthlyBudgetUSD = 100
	s.cfg.Costs.AlertThreshold = 0.8
	s.cfg.Costs.Pricing = map[string]config.CostPricing{
		"openai/*": {InputPerMTok: 1, OutputPerMTok: 2},
	}
	store, err := costs.NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatalf("cost store: %v", err)
	}
	s.SetCostStore(store)
	if err := store.Record(context.Background(), costs.UsageRecord{
		AgentID:      "agent",
		SessionID:    "session",
		Provider:     "openai",
		Model:        "gpt",
		PromptTokens: 100,
		CompTokens:   50,
		TotalTokens:  150,
		CostUSD:      1.25,
		CreatedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("record cost: %v", err)
	}

	status, resp := gatewayJSON(t, s, http.MethodGet, "/api/v1/costs/status", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, resp)
	}
	if resp["daily_budget_usd"] != float64(10) {
		t.Fatalf("daily_budget_usd = %v", resp["daily_budget_usd"])
	}
	if resp["pricing_rules"] != float64(1) {
		t.Fatalf("pricing_rules = %v", resp["pricing_rules"])
	}
	if got, _ := resp["last_24h_usd"].(float64); got <= 0 {
		t.Fatalf("last_24h_usd = %v, want usage", resp["last_24h_usd"])
	}
}

func TestReadinessIncludesCostPosture(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfg.Costs.DailyBudgetUSD = 1
	s.cfg.Costs.MonthlyBudgetUSD = 10

	status, resp := gatewayJSON(t, s, http.MethodGet, "/api/v1/readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, resp)
	}
	costsResp, ok := resp["costs"].(map[string]any)
	if !ok {
		t.Fatalf("costs readiness missing: %v", resp)
	}
	if _, ok := costsResp["checks"].([]any); !ok {
		t.Fatalf("cost checks missing: %v", costsResp)
	}
}
