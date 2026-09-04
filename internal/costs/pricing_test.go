package costs

import "testing"

func TestEstimateUSDExactProviderModel(t *testing.T) {
	table := PriceTable{
		"openai/gpt-test": {InputPerMTok: 1.5, OutputPerMTok: 6},
	}
	got := EstimateUSD(table, "OpenAI", "GPT-Test", 1000, 2000)
	want := 0.0135
	if got != want {
		t.Fatalf("EstimateUSD = %v, want %v", got, want)
	}
}

func TestEstimateUSDWildcardFallbacks(t *testing.T) {
	table := PriceTable{
		"openai/*":       {InputPerMTok: 1, OutputPerMTok: 2},
		"*/shared-model": {InputPerMTok: 3, OutputPerMTok: 4},
	}
	if got, want := EstimateUSD(table, "openai", "other", 1_000_000, 1_000_000), 3.0; got != want {
		t.Fatalf("provider wildcard = %v, want %v", got, want)
	}
	if got, want := EstimateUSD(table, "custom", "shared-model", 1_000_000, 1_000_000), 7.0; got != want {
		t.Fatalf("model wildcard = %v, want %v", got, want)
	}
}

func TestEstimateUSDUnknownOrInvalidPricingIsZero(t *testing.T) {
	table := PriceTable{
		"bad/model": {InputPerMTok: -1, OutputPerMTok: 1},
	}
	if got := EstimateUSD(table, "missing", "model", 100, 100); got != 0 {
		t.Fatalf("unknown EstimateUSD = %v, want 0", got)
	}
	if got := EstimateUSD(table, "bad", "model", 100, 100); got != 0 {
		t.Fatalf("invalid EstimateUSD = %v, want 0", got)
	}
}

func TestNormalizePriceKey(t *testing.T) {
	if got, want := NormalizePriceKey(" OpenAI / GPT-4.1-Mini "), "openai/gpt-4.1-mini"; got != want {
		t.Fatalf("NormalizePriceKey = %q, want %q", got, want)
	}
}

func TestEstimateDetailedAllBillingDimensions(t *testing.T) {
	table := PriceTable{"p/m": {
		InputPerMTok: 1, OutputPerMTok: 2, CachedInputPerMTok: 0.1,
		CacheWritePerMTok: 1.25, ReasoningPerMTok: 3,
		Source: "official", EffectiveDate: "2026-08-01", Version: "v1",
	}}
	usd, micros, status := EstimateDetailed(table, "p", "m", UsageDimensions{
		InputTokens: 1_000_000, OutputTokens: 1_000_000,
		CacheReadTokens: 1_000_000, CacheCreationTokens: 1_000_000,
		ReasoningTokens: 1_000_000, ToolUsePromptTokens: 1_000_000,
	})
	if usd != 8.35 || micros != 8_350_000 || status != "priced" {
		t.Fatalf("detailed estimate = %v/%d/%q", usd, micros, status)
	}
	if got := PricingVersion(table, "p", "m"); got != "v1|2026-08-01|official" {
		t.Fatalf("pricing version = %q", got)
	}
}

func TestPricingStatusDistinguishesUnknownAndFree(t *testing.T) {
	if _, _, status := EstimateDetailed(nil, "p", "m", UsageDimensions{}); status != "unknown" {
		t.Fatalf("unknown status = %q", status)
	}
	if usd, micros, status := EstimateDetailed(PriceTable{"p/m": {}}, "p", "m", UsageDimensions{InputTokens: 100}); usd != 0 || micros != 0 || status != "free" {
		t.Fatalf("free pricing = %v/%d/%q", usd, micros, status)
	}
}
