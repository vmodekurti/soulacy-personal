package reasoning

// One unrecognised tool name must not cost the whole plan.
//
// The Portfolio Replacement Strategist is configured plan_execute with a
// 12-step plan budget. Its trace opens:
//
//	Plan-Execute downgraded to ReAct before any step ran.
//
// planUnavailableTool rejects the ENTIRE plan if any step names a tool the
// agent cannot call, and what followed was greedy one-tool-at-a-time execution:
// three quotes fetched serially, five screening calls (two of them repeats),
// five fundamentals one ticker at a time, four technicals one ticker at a time
// — 25 steps spent gathering, none left to write the answer.
//
// Its forty tools are all named mcp__maverick-mcp__<something>. A planner
// reading that list will sometimes write the readable half.

import "testing"

// maverickTools is the shape of this agent's allowlist.
var maverickTools = []string{
	"mcp__maverick-mcp__market_data_get_quote",
	"mcp__maverick-mcp__portfolio_correlation_analysis",
	"mcp__maverick-mcp__screening_get_bullish",
	"channel.send",
}

func TestToolAllowed_AcceptsAnMCPToolNamedWithoutItsServerPrefix(t *testing.T) {
	if !toolAllowed("market_data_get_quote", maverickTools) {
		t.Fatal("a plan naming the readable half of an MCP tool was rejected — " +
			"one such name discards the whole plan and drops the run to ReAct")
	}
}

func TestCanonicalAllowedTool_ResolvesToTheFullName(t *testing.T) {
	got, ok := canonicalAllowedTool("portfolio_correlation_analysis", maverickTools)
	if !ok {
		t.Fatal("not resolved")
	}
	if got != "mcp__maverick-mcp__portfolio_correlation_analysis" {
		t.Errorf("resolved to %q — the runtime needs the full name to route the call", got)
	}
}

// A plan that would previously have been binned now survives intact.
func TestPlanUnavailableTool_AcceptsAPlanWrittenInShortNames(t *testing.T) {
	plan := Plan{Steps: []PlannedStep{
		{ID: "s1", Tool: "market_data_get_quote"},
		{ID: "s2", Tool: "portfolio_correlation_analysis"},
		{ID: "s3", Tool: "mcp__maverick-mcp__screening_get_bullish"},
	}}
	if why, bad := planUnavailableTool(plan, maverickTools); bad {
		t.Fatalf("the plan was rejected and the run will downgrade to ReAct: %s", why)
	}
}

// Two servers exposing the same short name is a genuine ambiguity. Calling the
// wrong server's tool is worse than saying the name cannot be resolved.
func TestResolveBareMCPTool_RefusesWhenTwoServersMatch(t *testing.T) {
	tools := []string{
		"mcp__maverick-mcp__get_quote",
		"mcp__other-mcp__get_quote",
	}
	if _, ok := resolveBareMCPTool("get_quote", tools); ok {
		t.Error("guessed which server the author meant")
	}
}

// A name that matches nothing is still unavailable — this must not turn the
// allowlist into a suggestion.
func TestToolAllowed_StillRejectsAToolNobodyExposes(t *testing.T) {
	if toolAllowed("delete_everything", maverickTools) {
		t.Error("an unknown tool was accepted")
	}
}

// A partial word must not match: "quote" is not "market_data_get_quote".
func TestResolveBareMCPTool_RequiresAWholeSegment(t *testing.T) {
	if _, ok := resolveBareMCPTool("quote", maverickTools); ok {
		t.Error("a fragment of a tool name was accepted as the tool")
	}
}
