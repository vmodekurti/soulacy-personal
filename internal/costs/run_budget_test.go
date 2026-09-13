package costs

import (
	"context"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
)

func TestMissionBudgetRejectsUnknownPricesEvenInSoftMode(t *testing.T) {
	p := &governedProvider{response: &llm.CompletionResponse{Content: "ok"}}
	r, _ := newGovernedRouter(t, GovernanceConfig{UnknownPricing: "allow", EnforcementMode: "soft"}, p)
	ctx := llm.WithRunCostBudget(context.Background(), 1000000)
	if _, err := r.Complete(ctx, "paid", llm.CompletionRequest{Model: "unpriced"}); err == nil {
		t.Fatal("unknown price admitted")
	}
	if p.calls != 0 {
		t.Fatal("unpriced call reached provider")
	}
}
func TestMissionZeroBudgetRejectsPaidCalls(t *testing.T) {
	p := &governedProvider{response: &llm.CompletionResponse{Content: "ok"}}
	r, _ := newGovernedRouter(t, GovernanceConfig{}, p)
	ctx := llm.WithRunCostBudget(context.Background(), 0)
	if _, err := r.Complete(ctx, "paid", llm.CompletionRequest{Model: "model"}); err == nil {
		t.Fatal("paid call admitted with zero budget")
	}
	if p.calls != 0 {
		t.Fatal("zero-budget call reached provider")
	}
}
