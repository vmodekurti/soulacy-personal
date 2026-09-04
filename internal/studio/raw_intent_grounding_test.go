package studio

import (
	"context"
	"strings"
	"testing"
)

// The refiner turns one line into a long specification. That expansion shares
// ordinary vocabulary with dozens of unrelated skills, which is why groundSkills
// matches against the user's ORIGINAL words instead — a guard that was inert,
// because nothing on the generation path ever populated Draft.RawIntent.
const weatherRawIntent = "I want to develop a conversation agent that provides timely " +
	"weather updates based on a place or a zipcode"

const weatherRefinedIntent = `A conversational agent that responds to user messages requesting
weather information for a specified place name or postal code. On each message it resolves the
location, retrieves current conditions and the short-term outlook, and returns a readable
summary. It should handle ambiguous locations by asking a follow-up question, report data
sources, stream updates where available, present the result in a clear visual format, and
degrade gracefully when a provider is unavailable or a region cannot be resolved.`

func bloatCatalog() Catalog {
	return Catalog{
		Tools: []string{"web_search", "fetch_url", "channel.send"},
		Skills: []CatalogSkill{
			// Each of these shares vocabulary with the REFINED text — "sources",
			// "region", "streaming", "visual", "resolve", "report" — and nothing at
			// all with what the user actually asked for.
			{Name: "hormuz-strait", Description: "Track shipping conditions and regional updates for a strait region."},
			{Name: "dark-mode-design-expert", Description: "Design a clear visual presentation and format for a readable interface."},
			{Name: "websocket-streaming", Description: "Stream updates over a socket and handle provider unavailability."},
			{Name: "saas-valuation-compression", Description: "Report on valuation multiples using specified data sources."},
			{Name: "finance-sentiment", Description: "Summarise market sentiment from current conditions and outlook."},
			{Name: "company-valuation", Description: "Resolve company identifiers and report a valuation summary."},
			// The one that genuinely fits.
			{Name: "weather-lookup", Description: "Look up weather forecasts by place name or zipcode."},
		},
	}
}

// Matching on the refined text is what produced a weather agent carrying
// finance and design skills.
func TestGroundingMatchesTheUsersWordsNotTheRefinedExpansion(t *testing.T) {
	cat := bloatCatalog()
	draft := &Draft{
		Intent:    weatherRefinedIntent,
		RawIntent: weatherRawIntent,
		Skills: []string{
			"hormuz-strait", "dark-mode-design-expert", "websocket-streaming",
			"saas-valuation-compression", "finance-sentiment", "company-valuation",
		},
	}
	groundSkills(draft, cat)

	for _, unwanted := range []string{
		"hormuz-strait", "dark-mode-design-expert", "saas-valuation-compression",
		"finance-sentiment", "company-valuation",
	} {
		for _, got := range draft.Skills {
			if got == unwanted {
				t.Errorf("kept %q on a weather agent; skills = %v", unwanted, draft.Skills)
			}
		}
	}
}

// Without the raw intent the guard has nothing better to use and falls back to
// the refined text. This pins WHY the field matters, so removing it fails here
// rather than silently returning to bloat.
func TestRefinedIntentAloneLetsUnrelatedSkillsThrough(t *testing.T) {
	cat := bloatCatalog()
	withRaw := &Draft{Intent: weatherRefinedIntent, RawIntent: weatherRawIntent}
	withoutRaw := &Draft{Intent: weatherRefinedIntent}
	for _, d := range []*Draft{withRaw, withoutRaw} {
		d.Skills = []string{
			"hormuz-strait", "dark-mode-design-expert", "websocket-streaming",
			"saas-valuation-compression", "finance-sentiment", "company-valuation",
		}
		groundSkills(d, cat)
	}
	if len(withRaw.Skills) >= len(withoutRaw.Skills) {
		t.Errorf("the raw intent did not tighten grounding: with=%v without=%v",
			withRaw.Skills, withoutRaw.Skills)
	}
}

// End to end: the pipeline must actually put the user's words on the draft.
// The field existed and was read; it was simply never written during generation.
func TestPipelinePopulatesRawIntentOnTheDraft(t *testing.T) {
	llm := &countingLLM{}
	res, err := RunGeneratePipeline(context.Background(), llm,
		trvlWorkflowIntent, trvlCatalog(), PipelineOptions{})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	got := strings.TrimSpace(res.Compile.Workflow.RawIntent)
	if got == "" {
		t.Fatal("draft.RawIntent is empty after generation, so capability grounding " +
			"falls back to the refined intent it is meant to avoid")
	}
	if !strings.Contains(got, "trvl") {
		t.Errorf("RawIntent = %q, expected the user's original prompt", got)
	}
}
