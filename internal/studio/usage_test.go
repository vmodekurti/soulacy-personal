package studio

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestUsedSkills(t *testing.T) {
	flow := Flow{Nodes: []sdkr.FlowNode{
		{ID: "a", Kind: "tool", Tool: "read_skill", Input: `{"skill_name":"yfinance"}`},
		{ID: "b", Kind: "tool", Tool: "web_search", Input: `{"query":"x"}`},
		{ID: "c", Kind: "tool", Tool: "read_skill", Input: `{"skill_name":"yfinance"}`}, // dup
		{ID: "d", Kind: "tool", Tool: "read_skill", Input: `{"skill_name":"weather"}`},
	}}
	got := usedSkills(flow)
	if len(got) != 2 || got[0] != "yfinance" || got[1] != "weather" {
		t.Errorf("usedSkills = %+v, want [yfinance weather]", got)
	}
}

func TestRecommendKBs(t *testing.T) {
	cat := Catalog{KnowledgeBases: []CatalogKB{
		{Name: "product_docs", Description: "internal product documentation and specs"},
		{Name: "recipes", Description: "cooking recipes"},
	}}
	got := recommendKBs("answer questions about our product specs", cat)
	if len(got) == 0 || got[0] != "product_docs" {
		t.Errorf("expected product_docs recommended first, got %+v", got)
	}
	// Unrelated intent → no recommendation.
	if rec := recommendKBs("translate text to french", cat); len(rec) != 0 {
		t.Errorf("expected no KB recommendation for unrelated intent, got %+v", rec)
	}
}
