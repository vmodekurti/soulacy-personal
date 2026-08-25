package studio

// A template must not claim a request whose shape it cannot build.
//
// The deterministic patterns are chosen by TOPIC keywords — "digest", "report",
// "telegram" — and every one of them emits a straight line. Asked live, on the
// shipped build, for three reviewers in parallel plus an editor, Studio returned
// a two-node search-and-summarize graph and said so plainly in its notes:
//
//	"Studio used Soulacy's deterministic fixed-workflow planner for
//	 research_digest; no LLM designed the graph."
//
// alongside generation{pattern_matched: true, confidence: "high",
// next_action: "save"} and a contract reporting 0 blockers. Every downstream
// protection — the structure retry, peer-agent materialisation, the join
// barrier — sits after a decision that had already been made on a keyword.

import (
	"strings"
	"testing"
)

// The exact prompt that produced the two-node graph on the running build.
const liveFanOutIntent = "Every weekday morning pull the latest incident reports from our support queue, " +
	"then run three reviewers in parallel over that material: a severity reviewer who ranks impact, " +
	"a root-cause reviewer who groups them by likely cause, and a customer-impact reviewer who counts " +
	"affected accounts. Finally an editor combines all three reviews into one incident digest and posts " +
	"it to Telegram."

func TestDeterministicWorkflow_PreservesTheLiveFanOutRequest(t *testing.T) {
	// It matches research_digest on topic words alone, so the guard is the only
	// thing standing between this prompt and a canned two-node graph.
	if !researchDigestWorkflow(liveFanOutIntent) {
		t.Fatal("the digest pattern no longer matches this prompt — this test is guarding nothing")
	}
	assertDeterministicFanOut(t, liveFanOutIntent, 3)
}

// The first live failure of the session, same shape, different words.
func TestDeterministicWorkflow_PreservesTheMarketDigestFanOut(t *testing.T) {
	intent := "Every weekday at 7am gather market data, then have a fundamentals analyst, a risk analyst " +
		"and a sentiment analyst work in parallel on it, and an editor combine their analyses into one " +
		"briefing sent to Telegram."
	assertDeterministicFanOut(t, intent, 3)
}

func TestDeterministicWorkflow_PreservesAdjectivalResearcherCount(t *testing.T) {
	intent := "On manual invocation run two independent market researchers in parallel, then a risk critic " +
		"compares their reports and a coordinator merges the result into one market report."
	plan := PlanFromIntent(intent)
	if got := plan.wantedWorkers(intent); got != 2 {
		t.Fatalf("wanted workers = %d, want 2", got)
	}
	assertDeterministicFanOut(t, intent, 2)
}

func assertDeterministicFanOut(t *testing.T, intent string, workers int) {
	t.Helper()
	res, ok := CompileDeterministicWorkflow(intent, Catalog{}, nil)
	if !ok {
		t.Fatal("explicit specialist fan-out received no deterministic floor")
	}
	gotWorkers := 0
	for _, n := range res.Workflow.Flow.Nodes {
		if n.Kind == "parallel" && n.JoinNode != "risk_critic" {
			t.Fatalf("parallel node has no critic barrier: %+v", n)
		}
		if strings.HasPrefix(n.ID, "specialist_") {
			gotWorkers++
		}
	}
	if gotWorkers != workers {
		t.Fatalf("specialist workers = %d, want %d", gotWorkers, workers)
	}
}

// The templates exist because they are genuinely better than a model guess for
// the linear requests they were built for. Declining those would be the worse
// regression.
func TestDeterministicWorkflow_StillClaimsAPlainDigest(t *testing.T) {
	intent := "Every morning search the web for the latest AI research news, summarize it, and send me a " +
		"digest on Telegram."
	if _, ok := CompileDeterministicWorkflow(intent, Catalog{}, nil); !ok {
		t.Fatal("the guard swallowed an ordinary linear digest request")
	}
}

// "in parallel" with nothing to run in parallel is not a described structure.
func TestDeterministicWorkflow_IgnoresAStrayParallelMention(t *testing.T) {
	intent := "Each morning summarize yesterday's sales report and send it to Slack. In parallel with that, " +
		"nothing else needs to happen."
	if _, ok := CompileDeterministicWorkflow(intent, Catalog{}, nil); !ok {
		t.Fatal("one loose use of the word \"parallel\" cost a template that was right for the job")
	}
}

// The decline and the after-the-fact judgement have to agree about what counts
// as a described structure. If the template declines shapes the shortfall check
// would not have flagged, requests get pushed to the model for no reason; if it
// claims shapes the shortfall check WOULD flag, the retry never gets a chance
// because no model was asked.
func TestStructureGuardMatchesTheShortfallBar(t *testing.T) {
	cases := []string{
		liveFanOutIntent,
		"run three reviewers in parallel then combine",
		"have a quality reviewer and a cost reviewer work at the same time",
		"summarize my email",
		"search the web and send me a digest",
		"a single analyst reviews the report each morning",
	}
	for _, intent := range cases {
		guard := StructureNamedButUnbuildable(intent)
		// An empty graph: if the intent describes a structure at all, a graph
		// with nothing in it must fall short of it.
		shortfall := StructureShortfall(intent, Result{}) != ""
		if guard != shortfall {
			t.Errorf("guard=%v but shortfall=%v for %q — the two checks disagree about what was asked for",
				guard, shortfall, truncateForTest(intent))
		}
	}
}

func truncateForTest(s string) string {
	if len(s) <= 60 {
		return s
	}
	return strings.TrimSpace(s[:60]) + "…"
}
