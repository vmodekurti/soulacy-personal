package costs

import (
	"math"
	"strings"
)

// Pricing describes a model's USD price per 1 million input/output tokens.
type Pricing struct {
	InputPerMTok       float64
	OutputPerMTok      float64
	CachedInputPerMTok float64
	CacheWritePerMTok  float64
	ReasoningPerMTok   float64
	Source             string
	EffectiveDate      string
	Version            string
}

// PricingVersion returns immutable catalog metadata retained with a usage row.
func PricingVersion(table PriceTable, provider, model string) string {
	price, ok := lookupPrice(table, provider, model)
	if !ok {
		return ""
	}
	return strings.Trim(strings.Join([]string{price.Version, price.EffectiveDate, price.Source}, "|"), "|")
}

// UsageDimensions contains normalized provider billing dimensions.
type UsageDimensions struct {
	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
	ReasoningTokens     int
	ToolUsePromptTokens int
}

// PriceTable maps provider/model selectors to per-token prices. Selectors are
// matched in this order: provider/model, provider/*, */model.
type PriceTable map[string]Pricing

// EstimateUSD returns the estimated USD cost for one LLM call. Unknown or
// partially configured pricing returns 0 instead of guessing.
func EstimateUSD(table PriceTable, provider, model string, inputTokens, outputTokens int) float64 {
	usd, _, _ := EstimateDetailed(table, provider, model, UsageDimensions{
		InputTokens: inputTokens, OutputTokens: outputTokens,
	})
	return usd
}

// EstimateDetailed returns estimated dollars, integer micro-dollars, and a
// pricing status. Unknown models are explicitly "unknown", never conflated
// with a configured zero-cost local model.
func EstimateDetailed(table PriceTable, provider, model string, usage UsageDimensions) (float64, int64, string) {
	if len(table) == 0 || usage.InputTokens < 0 || usage.OutputTokens < 0 {
		return 0, 0, "unknown"
	}
	price, ok := lookupPrice(table, provider, model)
	if !ok || price.InputPerMTok < 0 || price.OutputPerMTok < 0 {
		return 0, 0, "unknown"
	}
	cachedRate := price.CachedInputPerMTok
	if cachedRate == 0 {
		cachedRate = price.InputPerMTok
	}
	cacheWriteRate := price.CacheWritePerMTok
	if cacheWriteRate == 0 {
		cacheWriteRate = price.InputPerMTok
	}
	reasoningRate := price.ReasoningPerMTok
	if reasoningRate == 0 {
		reasoningRate = price.OutputPerMTok
	}
	usd := float64(usage.InputTokens+usage.ToolUsePromptTokens)/1_000_000*price.InputPerMTok +
		float64(usage.OutputTokens)/1_000_000*price.OutputPerMTok +
		float64(usage.CacheReadTokens)/1_000_000*cachedRate +
		float64(usage.CacheCreationTokens)/1_000_000*cacheWriteRate +
		float64(usage.ReasoningTokens)/1_000_000*reasoningRate
	status := "priced"
	if usd == 0 {
		status = "free"
	}
	return usd, int64(math.Round(usd * 1_000_000)), status
}

// MaxAffordableOutput returns the number of output tokens affordable for the
// supplied micro-dollar amount. The bool is false when pricing is unknown or
// the output rate is zero.
func MaxAffordableOutput(table PriceTable, provider, model string, availableMicros int64) (int, bool) {
	price, ok := lookupPrice(table, provider, model)
	if !ok || price.OutputPerMTok <= 0 || availableMicros <= 0 {
		return 0, false
	}
	return int(float64(availableMicros) / price.OutputPerMTok), true
}

func lookupPrice(table PriceTable, provider, model string) (Pricing, bool) {
	p := normalizePricePart(provider)
	m := normalizePricePart(model)
	keys := []string{
		p + "/" + m,
		p + "/*",
		"*/" + m,
	}
	for _, key := range keys {
		if price, ok := table[key]; ok {
			return price, true
		}
	}
	return Pricing{}, false
}

func NormalizePriceKey(key string) string {
	key = strings.TrimSpace(strings.ToLower(key))
	if key == "" {
		return ""
	}
	parts := strings.SplitN(key, "/", 2)
	if len(parts) != 2 {
		return normalizePricePart(key)
	}
	return normalizePricePart(parts[0]) + "/" + normalizePricePart(parts[1])
}

func normalizePricePart(s string) string {
	return strings.TrimSpace(strings.ToLower(s))
}
