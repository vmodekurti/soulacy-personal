package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/costs"
)

func TestEngineCostStoreAdapterEstimatesCostFromPricing(t *testing.T) {
	store, err := costs.NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	adapter := &engineCostStoreAdapter{
		s: store,
		prices: costs.PriceTable{
			"openai/gpt-test": {InputPerMTok: 1.5, OutputPerMTok: 6},
		},
	}
	if err := adapter.Record(context.Background(), "agent", "session", "openai", "gpt-test", 1000, 2000, 3000, 0); err != nil {
		t.Fatalf("Record: %v", err)
	}

	metrics, ok, err := store.SessionMetrics(context.Background(), "session")
	if err != nil {
		t.Fatalf("SessionMetrics: %v", err)
	}
	if !ok {
		t.Fatal("SessionMetrics found = false")
	}
	if want := 0.0135; metrics.CostUSD != want {
		t.Fatalf("CostUSD = %v, want %v", metrics.CostUSD, want)
	}
}

func TestEngineCostStoreAdapterKeepsExplicitCost(t *testing.T) {
	store, err := costs.NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	adapter := &engineCostStoreAdapter{
		s: store,
		prices: costs.PriceTable{
			"openai/gpt-test": {InputPerMTok: 100, OutputPerMTok: 100},
		},
	}
	if err := adapter.Record(context.Background(), "agent", "session", "openai", "gpt-test", 1000, 1000, 2000, 0.42); err != nil {
		t.Fatalf("Record: %v", err)
	}

	metrics, ok, err := store.SessionMetrics(context.Background(), "session")
	if err != nil {
		t.Fatalf("SessionMetrics: %v", err)
	}
	if !ok {
		t.Fatal("SessionMetrics found = false")
	}
	if metrics.CostUSD != 0.42 {
		t.Fatalf("CostUSD = %v, want explicit 0.42", metrics.CostUSD)
	}
}

func TestCostPriceTableFromConfigNormalizesKeys(t *testing.T) {
	table := costPriceTableFromConfig(map[string]config.CostPricing{
		" OpenAI / GPT-Test ": {InputPerMTok: 1, OutputPerMTok: 2},
	})
	got, ok := table["openai/gpt-test"]
	if !ok {
		t.Fatalf("normalized price key missing: %#v", table)
	}
	if got.InputPerMTok != 1 || got.OutputPerMTok != 2 {
		t.Fatalf("pricing = %+v", got)
	}
}
