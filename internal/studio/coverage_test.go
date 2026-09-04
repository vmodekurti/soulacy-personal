package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func coverageCatalog() Catalog {
	return Catalog{
		Tools: []string{"web_search", "fetch_url"},
		MCP: []CatalogMCPServer{{
			Server: "trvl",
			Tools:  []CatalogMCPTool{{Name: "mcp__trvl__travel", Description: "Search flights and hotels."}},
		}},
		Skills: []CatalogSkill{{Name: "stock_performance", Description: "Equity performance analysis."}},
	}
}

// A workflow draft whose single node uses the given tool.
func flowUsing(tool string) Result {
	return Result{Workflow: Draft{
		Name: "X",
		Flow: Flow{Nodes: []sdkr.FlowNode{{
			ID: "step", Kind: sdkr.FlowNodeTool, Tool: tool, Output: "out",
		}}},
	}}
}

func TestCoverageShortfall_FlagsANamedToolTheGraphIgnored(t *testing.T) {
	// The exact reported symptom: the prompt names the travel tool, the graph
	// solves it with web_search, and nothing says so.
	got := CoverageShortfall(travelAdvisorIntent, coverageCatalog(), flowUsing("web_search"))
	if got == "" {
		t.Fatal("a graph that ignored the named MCP tool must report a shortfall")
	}
	if !strings.Contains(got, "mcp__trvl__travel") {
		t.Errorf("the shortfall must name the missing tool, got %q", got)
	}
}

func TestCoverageShortfall_SilentWhenTheGraphUsesIt(t *testing.T) {
	if got := CoverageShortfall(travelAdvisorIntent, coverageCatalog(), flowUsing("mcp__trvl__travel")); got != "" {
		t.Fatalf("no shortfall expected, got %q", got)
	}
}

func TestCoverageShortfall_MatchesToolNamesCaseInsensitively(t *testing.T) {
	// draftToolSet is case-preserving by design; the comparison must not be.
	res := flowUsing("MCP__TRVL__TRAVEL")
	if got := CoverageShortfall(travelAdvisorIntent, coverageCatalog(), res); got != "" {
		t.Fatalf("case should not matter, got %q", got)
	}
}

func TestCoverageShortfall_SeesAgentLevelTools(t *testing.T) {
	// A reasoning agent carries tools on the draft rather than on nodes.
	res := Result{Workflow: Draft{Name: "A", Strategy: "auto", Tools: []string{"mcp__trvl__travel"}}}
	if got := CoverageShortfall(travelAdvisorIntent, coverageCatalog(), res); got != "" {
		t.Fatalf("agent-level tools count as coverage, got %q", got)
	}
}

func TestCoverageShortfall_FlagsANamedSkill(t *testing.T) {
	intent := "analyse my portfolio using the stock performance skill"
	got := CoverageShortfall(intent, coverageCatalog(), flowUsing("web_search"))
	if !strings.Contains(got, "stock_performance") {
		t.Fatalf("a named skill the draft does not carry must be reported, got %q", got)
	}
}

func TestCoverageShortfall_SkillMatchesSpacedForm(t *testing.T) {
	// Users write "stock performance"; the skill id is "stock_performance".
	res := Result{Workflow: Draft{Name: "A", Skills: []string{"stock_performance"}}}
	if got := CoverageShortfall("use the stock performance skill", coverageCatalog(), res); got != "" {
		t.Fatalf("the skill IS carried, so no shortfall: %q", got)
	}
}

func TestCoverageShortfall_QuietWhenNothingWasNamed(t *testing.T) {
	// The check must never fire on a vague topic guess — a warning on every
	// generation trains people to ignore warnings.
	if got := CoverageShortfall("summarise my email each morning", coverageCatalog(), flowUsing("web_search")); got != "" {
		t.Fatalf("no capability was named, so nothing to report: %q", got)
	}
}

func TestCoverageShortfall_QuietWhenTheCapabilityIsNotInstalled(t *testing.T) {
	// Naming a tool this workspace does not have is not a coverage failure —
	// there is nothing that could have been wired.
	empty := Catalog{Tools: []string{"web_search"}}
	if got := CoverageShortfall(travelAdvisorIntent, empty, flowUsing("web_search")); got != "" {
		t.Fatalf("uninstalled capabilities must not be reported: %q", got)
	}
}

func TestNamedSkills_IgnoresShortNames(t *testing.T) {
	cat := Catalog{Skills: []CatalogSkill{{Name: "ab"}}}
	if got := namedSkills("absolutely unrelated text", cat); len(got) != 0 {
		t.Fatalf("short skill names must not match substrings, got %v", got)
	}
}
