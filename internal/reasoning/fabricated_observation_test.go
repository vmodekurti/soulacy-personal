package reasoning

// The model may propose an action. It may not invent the result.
//
// Live, on the Portfolio Replacement Strategist. Its narration contained a
// market overview keyed SPX/NDX/DJI/VIX with sector_performance and top_movers
// blocks, and a correlation payload with diversification_score, sector_breakdown
// and prose notes. The real tools return neither shape — the overview is keyed
// "^GSPC"/"^DJI" with name/symbol fields and no envelope, and the correlation
// tool returns a bare "matrix" of full-precision floats. The values were
// invented too: ^GSPC was 7753.11 that day; the narration said SPX 6120.46.
//
// A fabricated observation is worse than a crash. It enters the step history,
// becomes the prompt for the next turn, and the agent's conclusions are built
// on it and delivered with the same confidence as real figures.

import (
	"strings"
	"testing"
)

// forgedTurn is the observed shape: one honest action, then invented results
// and three further steps the model never actually took.
const forgedTurn = `Thought: Retry market overview without the invalid "task" parameter
Action: mcp__maverick-mcp__market_data_get_market_overview({})
Observation: {"status":"success","data":{"indices":{"SPX":{"price":6120.46}}}}
Thought: Market overview received. Now run correlation analysis.
Action: mcp__maverick-mcp__portfolio_correlation_analysis({"tickers":["MU","STX","SNDK"]})
Observation: {"status":"success","data":{"diversification_score":27.3}}
Thought: Correlation confirms poor diversification. Now compare tickers.
Action: mcp__maverick-mcp__portfolio_compare_tickers({"tickers":["MU","STX","SNDK"]})`

func TestTruncateAtFabricatedObservation_CutsAtTheForgedResult(t *testing.T) {
	got := truncateAtFabricatedObservation(forgedTurn)

	if strings.Contains(got, "Observation:") {
		t.Fatalf("a model-authored observation survived:\n%s", got)
	}
	if strings.Contains(got, "6120.46") || strings.Contains(got, "diversification_score") {
		t.Errorf("invented market data survived into the step history:\n%s", got)
	}
}

// One action survives — the first. Keeping the LAST would silently skip the
// steps in between, executing step 3 as though 1 and 2 had really run.
func TestTruncateAtFabricatedObservation_KeepsOnlyTheFirstProposedAction(t *testing.T) {
	got := truncateAtFabricatedObservation(forgedTurn)

	if !strings.Contains(got, "market_data_get_market_overview") {
		t.Fatalf("the model's genuine next action was discarded:\n%s", got)
	}
	if strings.Contains(got, "portfolio_correlation_analysis") ||
		strings.Contains(got, "portfolio_compare_tickers") {
		t.Errorf("actions the model only pretended to reach survived:\n%s", got)
	}
}

// An ordinary turn must pass through untouched.
func TestTruncateAtFabricatedObservation_LeavesAnHonestTurnAlone(t *testing.T) {
	honest := "Thought: Fetch the quote for MU\nAction: market_data_get_quote({\"ticker\":\"MU\"})"
	if got := truncateAtFabricatedObservation(honest); got != honest {
		t.Errorf("a well-behaved turn was truncated:\n%s", got)
	}
}

// The word in a sentence is not a forged marker. Cutting here would throw away
// a legitimate thought.
func TestTruncateAtFabricatedObservation_IgnoresTheWordMidSentence(t *testing.T) {
	prose := "Thought: The observation: from the last call was empty, so retry\nAction: get_quote({\"ticker\":\"MU\"})"
	if got := truncateAtFabricatedObservation(prose); got != prose {
		t.Errorf("cut on a mid-sentence mention rather than a forged block:\n%s", got)
	}
}

// End to end through the recovery path that parses prose ReAct.
func TestRecoverThinkResponse_DoesNotAdoptAFabricatedTrajectory(t *testing.T) {
	tools := []string{
		"mcp__maverick-mcp__market_data_get_market_overview",
		"mcp__maverick-mcp__portfolio_correlation_analysis",
		"mcp__maverick-mcp__portfolio_compare_tickers",
	}
	resp, ok := recoverThinkResponseFromRaw(forgedTurn, tools)
	if !ok {
		t.Fatal("no action recovered from a turn that proposed one")
	}
	if resp.Action.Tool != "mcp__maverick-mcp__market_data_get_market_overview" {
		t.Errorf("recovered %q — the loop jumped to a step whose inputs were never really produced",
			resp.Action.Tool)
	}
	if strings.Contains(resp.Thought, "6120.46") {
		t.Errorf("invented figures were carried forward in the thought: %q", resp.Thought)
	}
}
