package studio

import (
	"fmt"
	"testing"
)

// Regression for the "wall of skills" bug: on a machine with many installed
// skills, a stock-advisor intent had ~every skill injected because their
// descriptions shared generic words ("user", "questions", "data") with the
// intent. Injection must only count DISTINCTIVE (rare) shared tokens, so noise
// skills that merely share filler words are not attached, and the total is
// capped.
func TestGroundSkills_NoFloodFromGenericTokens(t *testing.T) {
	cat := Catalog{Skills: []CatalogSkill{
		{Name: "stock-quotes", Description: "Live stock quotes and prices by ticker symbol"},
		{Name: "ticker-lookup", Description: "Resolve a company name to its stock ticker symbol"},
	}}
	// 40 unrelated skills whose descriptions all share the generic words
	// "answers", "user", "questions", "data" with the intent. Under a raw
	// token-count match these would each clear the >=2 bar and flood in.
	for i := 0; i < 40; i++ {
		cat.Skills = append(cat.Skills, CatalogSkill{
			Name:        fmt.Sprintf("noise-%02d", i),
			Description: "Answers user questions and returns data for the report.",
		})
	}

	d := Draft{
		Strategy:  "react",
		RawIntent: "A conversational stock advisor that answers user questions about stocks by ticker symbol or company name; it resolves tickers and returns quotes.",
		Skills:    nil,
	}
	GroundAgentCapabilities(&d, cat)

	// No noise skill may be injected.
	for _, s := range d.Skills {
		if len(s) >= 6 && s[:6] == "noise-" {
			t.Errorf("generic-word noise skill %q was injected — the flood is back", s)
		}
	}
	// The genuinely on-topic skills (rare tokens: stock/ticker/quotes/symbol) are
	// still caught.
	if !contains(d.Skills, "stock-quotes") {
		t.Errorf("expected on-topic 'stock-quotes' to be injected; got %v", d.Skills)
	}
	// And the total is bounded regardless.
	if len(d.Skills) > 12 {
		t.Errorf("injected %d skills, want <= 12: %v", len(d.Skills), d.Skills)
	}
}
