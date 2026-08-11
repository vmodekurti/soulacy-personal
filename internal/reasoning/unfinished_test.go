package reasoning

// A fragment of the agent's working must never be served as the answer.
//
// Live, on the Portfolio Replacement Strategist. Asked "provide me advice with
// replacement of below portfolio MU - 40 STX - 20 SNDK - 32", the agent did
// everything right for 21 tool calls: quoted all three holdings, built a
// correlation matrix, ran three screens, pulled fundamentals for nine candidate
// replacements. Then it hit its step ceiling with no report written, and the
// user received, in full:
//
//	ticker: CVX; status: success; current_price: 194.91; outlook: strongly bullish
//
// One line, about a ticker they never mentioned. Nothing marked it as
// unfinished — not the wording, not the shape, not the run status, which was
// recorded "success". The information is worth keeping; presenting it as a
// reply is not.

import (
	"strings"
	"testing"
)

// gatheringSteps is the tail of that run: useful data, no conclusion.
func gatheringSteps() []Step {
	return []Step{
		{Action: ToolCall{Tool: "market_data_get_quote"}, Obs: Observation{
			Source: "market_data_get_quote", Content: `{"symbol":"MU","price":861.0}`}},
		{Action: ToolCall{Tool: "technical_get_full_technical_analysis"}, Obs: Observation{
			Source:  "technical_get_full_technical_analysis",
			Content: "ticker: CVX; status: success; current_price: 194.91; outlook: strongly bullish"}},
	}
}

func TestSanitizeFinalOutput_DoesNotPassOffAnObservationAsTheAnswer(t *testing.T) {
	got := SanitizeFinalOutput("", gatheringSteps())

	if !strings.HasPrefix(got, UnfinishedAnswerPrefix) {
		t.Fatalf("a leftover observation was returned as the reply, unlabelled:\n%s", got)
	}
}

// Labelling must not mean discarding: the fragment is the only evidence the
// reader has of what the agent actually did.
func TestSanitizeFinalOutput_KeepsTheFragmentItLabels(t *testing.T) {
	got := SanitizeFinalOutput("", gatheringSteps())

	if !strings.Contains(got, "outlook: strongly bullish") {
		t.Errorf("the observation was dropped along with the pretence:\n%s", got)
	}
	if !strings.Contains(got, "step budget") {
		t.Errorf("the message does not say what the reader can change:\n%s", got)
	}
}

// A real answer is untouched. This guard sits on every reply, so a false
// positive would prepend an apology to good output.
func TestSanitizeFinalOutput_LeavesARealAnswerAlone(t *testing.T) {
	answer := "Here is your replacement portfolio: swap SNDK for CVX, keeping MU and STX."
	if got := SanitizeFinalOutput(answer, gatheringSteps()); got != answer {
		t.Errorf("a finished answer was rewritten:\n%s", got)
	}
}

// With nothing readable to show, the old plain message still stands — there is
// no fragment to label.
func TestSanitizeFinalOutput_StillHasAMessageWhenThereIsNothingToShow(t *testing.T) {
	got := SanitizeFinalOutput("", nil)
	if strings.HasPrefix(got, UnfinishedAnswerPrefix) {
		t.Errorf("claimed to show a fragment when there was none:\n%s", got)
	}
	if strings.TrimSpace(got) == "" {
		t.Error("the user got an empty reply")
	}
}
