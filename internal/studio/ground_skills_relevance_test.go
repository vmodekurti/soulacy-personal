package studio

import (
	"strings"
	"testing"
)

// The catalogue from the workspace where this was observed: a dozen mostly
// finance/tooling skills, none of them about travel.
func installedSkillsCatalog() Catalog {
	return Catalog{
		Skills: []CatalogSkill{
			{Name: "funda-data", Description: "Fundamental company data for equities."},
			{Name: "agent-creator", Description: "Create a new agent from a description."},
			{Name: "automatic-stateful-prompt-improver", Description: "Improve a prompt over time using stored results."},
			{Name: "find-skills", Description: "Search the installed skills for a capability."},
			{Name: "human-gate-designer", Description: "Design a human approval gate for an agent."},
			{Name: "llm-router", Description: "Route a request to the cheapest capable model."},
			{Name: "mcp-creator", Description: "Scaffold a new MCP server."},
			{Name: "options-payoff", Description: "Compute option strategy payoff diagrams and break-evens."},
			{Name: "tradingview-reader", Description: "Read TradingView charts and indicators."},
			{Name: "earnings-preview", Description: "Preview an upcoming company earnings report."},
			{Name: "earnings-recap", Description: "Recap a company earnings call."},
			{Name: "generative-ui", Description: "Render a generative UI surface."},
			{Name: "trip-planner", Description: "Plan a trip itinerary with flights and hotels."},
		},
	}
}

const travelIntent = "A conversational travel advisor agent that answers user travel " +
	"questions about flight/hotel options using the trvl MCP travel tool"

// The observed failure: the builder listed twelve skills, every one of them was
// approved because it existed, and a travel advisor shipped with options-payoff
// and earnings-recap attached.
func TestModelChosenSkillsAreCheckedForRelevance(t *testing.T) {
	draft := &Draft{
		RawIntent: travelIntent,
		Skills: []string{
			"funda-data", "agent-creator", "automatic-stateful-prompt-improver",
			"find-skills", "human-gate-designer", "llm-router", "mcp-creator",
			"options-payoff", "tradingview-reader", "earnings-preview",
			"earnings-recap", "generative-ui",
		},
	}
	notes := groundSkills(draft, installedSkillsCatalog())

	for _, unwanted := range []string{"options-payoff", "earnings-recap", "earnings-preview", "tradingview-reader"} {
		for _, got := range draft.Skills {
			if got == unwanted {
				t.Errorf("finance skill %q survived on a travel agent (skills: %v)", unwanted, draft.Skills)
			}
		}
	}
	if len(draft.Skills) >= 12 {
		t.Errorf("kept %d skills, essentially the whole catalogue: %v", len(draft.Skills), draft.Skills)
	}
	// Removals must be visible, not silent.
	var explained bool
	for _, n := range notes {
		if strings.Contains(n, "Dropped skill") {
			explained = true
		}
	}
	if !explained && len(draft.Skills) < 12 {
		t.Error("skills were removed without any note explaining it")
	}
}

// A small, deliberate selection must be trusted — the builder can have reasons
// a token comparison cannot see, and this guard is for catalogue-dumping.
func TestSmallDeliberateSkillSelectionIsTrusted(t *testing.T) {
	draft := &Draft{
		RawIntent: travelIntent,
		Skills:    []string{"trip-planner", "generative-ui"},
	}
	groundSkills(draft, installedSkillsCatalog())

	if len(draft.Skills) < 2 {
		t.Errorf("a two-skill choice was second-guessed: %v", draft.Skills)
	}
	for _, want := range []string{"trip-planner", "generative-ui"} {
		var found bool
		for _, got := range draft.Skills {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("deliberate pick %q was dropped: %v", want, draft.Skills)
		}
	}
}

// An on-topic skill must survive the guard even in a long list.
func TestRelevantSkillSurvivesTheGuard(t *testing.T) {
	draft := &Draft{
		RawIntent: travelIntent,
		Skills: []string{
			"trip-planner", "options-payoff", "earnings-recap", "funda-data",
			"llm-router", "generative-ui", "mcp-creator",
		},
	}
	groundSkills(draft, installedSkillsCatalog())

	var found bool
	for _, got := range draft.Skills {
		if got == "trip-planner" {
			found = true
		}
	}
	if !found {
		t.Errorf("the one genuinely relevant skill was dropped: %v", draft.Skills)
	}
}

// Generic English must not read as topical evidence. This is what let
// "agent-creator" and "funda-data" be INJECTED into a travel prompt: on a small
// catalogue the document-frequency filter discounts nothing.
func TestGenericWordsAreNotTopicEvidence(t *testing.T) {
	draft := &Draft{RawIntent: travelIntent}
	groundSkills(draft, installedSkillsCatalog())

	for _, unwanted := range []string{"agent-creator", "funda-data", "find-skills", "llm-router"} {
		for _, got := range draft.Skills {
			if got == unwanted {
				t.Errorf("injected %q into a travel prompt on generic-word overlap (skills: %v)",
					unwanted, draft.Skills)
			}
		}
	}
}
