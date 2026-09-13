package costs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestOperationsUsageBoundariesLegacyPricingAndPrivacy(t *testing.T) {
	s := newStore(t)
	start := time.Date(2026, 9, 12, 12, 0, 0, 0, time.FixedZone("test", -5*3600))
	end := start.Add(time.Hour)
	records := []UsageRecord{
		{AgentID: "a", Provider: "p", Model: "m", TotalTokens: 100, CostMicros: 120000, Status: "success", PricingStatus: "priced", AttemptCount: 2},
		{AgentID: "a", Provider: "p", Model: "m", TotalTokens: 50, Status: "error", PricingStatus: "unknown"},
		{AgentID: "b", Provider: "p", Model: "m", TotalTokens: 10, Status: "success", PricingStatus: "free"},
		{AgentID: "a", Provider: "p", Model: "m", Status: "rejected", PricingStatus: "unknown"},
		{AgentID: "legacy", Provider: "other", Model: "m", TotalTokens: 20, CostUSD: 0.0025},
		{TotalTokens: 1, Status: "success", PricingStatus: "unknown"},
	}
	for _, r := range records {
		r.CreatedAt = start
		r.Subject = "private-owner"
		r.SessionID = "private-session"
		r.ErrorCode = "private-error"
		if err := s.Record(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	for _, at := range []time.Time{start.Add(-time.Second), end} {
		if err := s.Record(t.Context(), UsageRecord{AgentID: "outside", TotalTokens: 9999, CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.OperationsUsage(t.Context(), start, end)
	if err != nil {
		t.Fatal(err)
	}
	want := OperationsUsageTotals{Requests: 6, Attempts: 6, Failed: 1, Rejected: 1, UnknownPriced: 3, TotalTokens: 181, CostMicros: 122500}
	if got.Totals != want {
		t.Fatalf("got %+v want %+v", got.Totals, want)
	}
	var agents, models OperationsUsageTotals
	for _, r := range got.ByAgent {
		agents.add(r.OperationsUsageTotals)
	}
	for _, r := range got.ByModel {
		models.add(r.OperationsUsageTotals)
	}
	if agents != want || models != want || len(got.ByModel) != 3 {
		t.Fatalf("breakdowns disagree: %+v", got)
	}
	data, _ := json.Marshal(got)
	for _, secret := range []string{"private-owner", "private-session", "private-error", "outside"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("report leaked %q", secret)
		}
	}
}

func TestOperationsUsageEmptyAndCanceled(t *testing.T) {
	s := newStore(t)
	start := time.Now().UTC()
	got, err := s.OperationsUsage(t.Context(), start, start.Add(time.Hour))
	if err != nil || got.Totals.Requests != 0 || got.ByAgent == nil || got.ByModel == nil {
		t.Fatalf("empty = %+v %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.OperationsUsage(ctx, start, start.Add(time.Hour)); err == nil {
		t.Fatal("cancellation ignored")
	}
}
