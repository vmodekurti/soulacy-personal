package studio

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func catalogWithFinanceSkills() Catalog {
	return Catalog{
		Skills: []CatalogSkill{
			{Name: "yfinance", Description: "Yahoo Finance market data: stock quotes, history, fundamentals"},
			{Name: "market_news", Description: "Latest financial and market news headlines"},
			{Name: "weather", Description: "Current weather and forecasts"},
		},
		Tools: []string{"web_search", "read_skill"},
		MCP: []CatalogMCPServer{{
			Server: "finance",
			Tools:  []CatalogMCPTool{{Name: "mcp__finance__quote", Description: "Get a stock quote"}},
		}},
	}
}

// A near-miss skill name the model invented ("yahoo finance") must be mapped to
// the real installed skill via its description, not dropped.
func TestGroundSkills_CorrectsNearMiss(t *testing.T) {
	d := Draft{
		Strategy: "react",
		Intent:   "answer questions about how a stock is performing",
		Skills:   []string{"yahoo finance"},
	}
	notes := GroundAgentCapabilities(&d, catalogWithFinanceSkills())
	if !reflect.DeepEqual(d.Skills, []string{"yfinance"}) {
		t.Fatalf("expected near-miss mapped to yfinance; got %v (notes %v)", d.Skills, notes)
	}
}

// An installed skill the intent clearly references but the model omitted must be
// injected as part of the baseline.
func TestGroundSkills_InjectsRelevantInstalled(t *testing.T) {
	d := Draft{
		Strategy: "react",
		Intent:   "give me the latest market news and stock quotes",
		Skills:   nil,
	}
	GroundAgentCapabilities(&d, catalogWithFinanceSkills())
	got := append([]string(nil), d.Skills...)
	sort.Strings(got)
	// "market news" → market_news (name mentioned); "stock quotes" → yfinance (desc overlap).
	want := []string{"market_news", "yfinance"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected relevant skills injected; got %v want %v", got, want)
	}
	// An unrelated installed skill must NOT be injected.
	for _, s := range d.Skills {
		if s == "weather" {
			t.Errorf("unrelated 'weather' skill should not be injected")
		}
	}
}

// A large, domain-heavy system prompt must NOT cause unrelated installed skills
// to be injected. Injection matches the user's SHORT raw intent, not the giant
// generated prompt (regression for the "~30 skills of bloat" bug).
func TestGroundSkills_NoBloatFromHugeSystemPrompt(t *testing.T) {
	d := Draft{
		Strategy:  "react",
		RawIntent: "screen momentum stocks and send a telegram report",
		Intent:    "Run a daily momentum stock screen and deliver a ranked report",
		// Huge prompt that mentions market, news, weather, etc. — shares generic
		// tokens with the unrelated catalog skills. This USED to over-inject.
		SystemPrompt: strings.Repeat("financial market news stock data quotes earnings weather forecast headlines ", 40),
		Skills:       []string{"yfinance"}, // the model's single relevant pick
	}
	GroundAgentCapabilities(&d, catalogWithFinanceSkills())
	for _, s := range d.Skills {
		if s == "market_news" || s == "weather" {
			t.Fatalf("system prompt caused skill bloat: injected %q; skills=%v", s, d.Skills)
		}
	}
	found := false
	for _, s := range d.Skills {
		if s == "yfinance" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the model's yfinance pick to be kept; got %v", d.Skills)
	}
}

// A skill that doesn't exist and matches nothing is dropped (not silently kept).
func TestGroundSkills_DropsUnknown(t *testing.T) {
	d := Draft{Strategy: "react", Intent: "do a thing", Skills: []string{"totally_made_up_skill_xyz"}}
	GroundAgentCapabilities(&d, catalogWithFinanceSkills())
	for _, s := range d.Skills {
		if s == "totally_made_up_skill_xyz" {
			t.Fatalf("unknown skill should have been dropped; got %v", d.Skills)
		}
	}
}

// A workflow's read_skill node with a near-miss skill name is fuzzy-corrected to
// the real installed skill (symmetric with the agent path), preserving other
// input keys; an exact name is canonicalised; an unknown one is left for "Needs
// setup" to surface.
func TestGroundFlowSkills_CorrectsReadSkillNodes(t *testing.T) {
	d := Draft{
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "load", Tool: "read_skill", Input: `{"skill_name":"yahoo finance","query":"AAPL"}`},
			{ID: "ok", Tool: "read_skill", Input: `{"skill_name":"weather"}`},
			{ID: "unknown", Tool: "read_skill", Input: `{"skill_name":"no_such_skill_zzz"}`},
		}},
	}
	notes := GroundFlowSkills(&d, catalogWithFinanceSkills())

	if got := skillNameFromInput(d.Flow.Nodes[0].Input); got != "yfinance" {
		t.Fatalf("near-miss read_skill not corrected; got %q (notes %v)", got, notes)
	}
	// Other keys in the corrected node must be preserved.
	if !strings.Contains(d.Flow.Nodes[0].Input, "AAPL") {
		t.Errorf("correcting skill_name dropped sibling keys: %s", d.Flow.Nodes[0].Input)
	}
	if got := skillNameFromInput(d.Flow.Nodes[1].Input); got != "weather" {
		t.Errorf("exact skill name should be left intact; got %q", got)
	}
	if got := skillNameFromInput(d.Flow.Nodes[2].Input); got != "no_such_skill_zzz" {
		t.Errorf("unknown skill should be left for validation; got %q", got)
	}
}

// Tools are verified/corrected but never auto-injected.
func TestGroundTools_VerifiesAndCorrects(t *testing.T) {
	d := Draft{
		Strategy: "react",
		Intent:   "x",
		Tools:    []string{"web_search", "mcp__financee__quote", "bogus_tool"},
	}
	GroundAgentCapabilities(&d, catalogWithFinanceSkills())
	got := append([]string(nil), d.Tools...)
	sort.Strings(got)
	// web_search kept; mcp server typo corrected by matching tool segment; bogus dropped.
	want := []string{"mcp__finance__quote", "web_search"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tool grounding wrong; got %v want %v", got, want)
	}
}
