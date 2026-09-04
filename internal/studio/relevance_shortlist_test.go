package studio

import (
	"fmt"
	"strings"
	"testing"
)

// A realistic install: far more skills than the prompt can carry, so the
// shortlist matters. The observed workspace had 210 installed against a cap
// of 24 — at that ratio the ranking decides everything the model ever sees.
func bigSkillCatalog() Catalog {
	cat := Catalog{
		MCP: []CatalogMCPServer{{
			Server: "trvl",
			Tools:  []CatalogMCPTool{{Name: "mcp__trvl__travel", Description: "Search flights and hotels."}},
		}},
	}
	// Decoys that share only ordinary English with a travel prompt.
	decoys := []CatalogSkill{
		{Name: "options-payoff", Description: "Compute option strategy payoff and break-even data for a user."},
		{Name: "earnings-recap", Description: "Recap an earnings call and answer user questions about the results."},
		{Name: "earnings-preview", Description: "Preview earnings data and generate an analysis report for the user."},
		{Name: "tradingview-reader", Description: "Reader tool that searches chart data and returns results to the agent."},
		{Name: "funda-data", Description: "Fundamental data search tool for an agent to request company information."},
		{Name: "agent-creator", Description: "Create an agent from a user request using the available tools and data."},
		{Name: "mcp-creator", Description: "Create an MCP server; generates tool definitions from a user request."},
		{Name: "llm-router", Description: "Route a user request to a model and return the result."},
	}
	cat.Skills = append(cat.Skills, decoys...)
	// The one genuinely on-topic skill.
	cat.Skills = append(cat.Skills, CatalogSkill{
		Name: "trip-planner", Description: "Plan a trip itinerary with flights, hotels and destinations.",
	})
	// Padding to push the catalogue well past the cap.
	for i := 0; i < 220; i++ {
		cat.Skills = append(cat.Skills, CatalogSkill{
			Name:        fmt.Sprintf("filler-skill-%03d", i),
			Description: "A tool that helps a user request data and generate a result report.",
		})
	}
	return cat
}

const shortlistIntent = "A travel advisor that answers questions about flight and hotel options " +
	"using the trvl MCP travel tool"

// The shortlist is the whole game: a capability that is not shown cannot be
// chosen, and one that is shown for no good reason invites a wrong choice.
func TestShortlistPrefersTopicalSkillsOverGenericWordMatches(t *testing.T) {
	cat := bigSkillCatalog()
	if len(cat.Skills) <= maxGroundedSkills {
		t.Fatalf("test catalogue (%d) must exceed the cap (%d) for trimming to apply",
			len(cat.Skills), maxGroundedSkills)
	}

	out := FilterCatalogForIntent(shortlistIntent, cat)

	var kept []string
	for _, s := range out.Skills {
		kept = append(kept, s.Name)
	}
	joined := strings.Join(kept, ",")

	if !strings.Contains(joined, "trip-planner") {
		t.Errorf("the one on-topic skill was not shortlisted; kept = %v", kept)
	}
	// These share only "user"/"data"/"agent"/"options"/"tool"/"request" with the
	// prompt. Ranking on raw token overlap put them at the top of a travel
	// shortlist, which is how they ended up attached to a travel agent.
	for _, unwanted := range []string{"options-payoff", "earnings-recap", "earnings-preview"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("%q was shortlisted for a travel prompt on generic-word overlap; kept = %v",
				unwanted, kept)
		}
	}
}

// A skill the user names must survive regardless of how it scores.
func TestShortlistAlwaysKeepsAnExplicitlyNamedSkill(t *testing.T) {
	cat := bigSkillCatalog()
	out := FilterCatalogForIntent("use the earnings-recap skill on today's call", cat)
	var found bool
	for _, s := range out.Skills {
		if s.Name == "earnings-recap" {
			found = true
		}
	}
	if !found {
		t.Error("a skill named outright in the prompt was trimmed out of the shortlist")
	}
}

// Trimming must never drop the MCP tool the prompt named — that would make the
// coverage retry impossible to satisfy, since the model would never see it.
func TestShortlistKeepsTheNamedMCPTool(t *testing.T) {
	out := FilterCatalogForIntent(shortlistIntent, bigSkillCatalog())
	var found bool
	for _, srv := range out.MCP {
		for _, tool := range srv.Tools {
			if tool.Name == "mcp__trvl__travel" {
				found = true
			}
		}
	}
	if !found {
		t.Error("the MCP tool the prompt named is missing from the shortlist")
	}
}

// Small installs must be passed through untouched.
func TestSmallCatalogIsNotTrimmed(t *testing.T) {
	cat := Catalog{Skills: []CatalogSkill{
		{Name: "one", Description: "first"}, {Name: "two", Description: "second"},
	}}
	if out := FilterCatalogForIntent(shortlistIntent, cat); len(out.Skills) != 2 {
		t.Errorf("a small catalogue was trimmed to %d", len(out.Skills))
	}
}
