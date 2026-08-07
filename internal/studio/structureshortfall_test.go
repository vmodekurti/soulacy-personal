package studio

// The two live runs, as fixtures.
//
// Same prompt, same build, same model, minutes apart. One produced the fan-out
// the user described; the other collapsed it to two nodes. Both passed every
// existing check, so nothing told the user which one they had got.

import (
	"errors"
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

const supplierIntent = "Every weekday at 6:30am, for each of my three suppliers pull yesterday's " +
	"delivery records and open complaints. Then run three reviewers in parallel over that material: " +
	"a quality reviewer who judges defect rates, a cost reviewer who checks invoiced price against " +
	"contract, and a reliability reviewer who scores on-time delivery. Finally a scorecard writer " +
	"combines all three reviews into one supplier scorecard."

// What run B actually built.
func collapsedGraph() Result {
	return Result{Workflow: Draft{
		Name: "Supplier Scorecard Workflow", Trigger: Trigger{Type: "cron"},
		Flow: Flow{Entry: "retrieve_data", Nodes: []sdkr.FlowNode{
			{ID: "retrieve_data", Kind: "python", Output: "supplier_data"},
			{ID: "produce", Kind: "agent", Agent: "scorecard_writer", Output: "content"},
		}},
	}}
}

// What run A actually built.
func fannedOutGraph() Result {
	return Result{Workflow: Draft{
		Name: "Supplier Scorecard Workflow", Trigger: Trigger{Type: "cron"},
		Flow: Flow{Entry: "fetch_data", Nodes: []sdkr.FlowNode{
			{ID: "fetch_data", Kind: "python", Output: "supplier_data"},
			{ID: "check_data", Kind: "branch"},
			{ID: "run_reviews", Kind: "parallel"},
			{ID: "quality_review", Kind: "agent", Agent: "quality_reviewer"},
			{ID: "cost_review", Kind: "agent", Agent: "cost_reviewer"},
			{ID: "reliability_review", Kind: "agent", Agent: "reliability_reviewer"},
			{ID: "write_scorecard", Kind: "agent", Agent: "scorecard_writer"},
		}},
	}}
}

func TestStructureShortfall_CatchesTheCollapsedGraph(t *testing.T) {
	short := StructureShortfall(supplierIntent, collapsedGraph())
	if short == "" {
		t.Fatal("a two-node graph for a three-specialist fan-out passed unremarked — this is the failure the file exists for")
	}
	if !strings.Contains(short, "3") {
		t.Errorf("the message should say how many were asked for, got: %s", short)
	}
}

func TestStructureShortfall_AcceptsTheGraphThatDidTheJob(t *testing.T) {
	if short := StructureShortfall(supplierIntent, fannedOutGraph()); short != "" {
		t.Fatalf("flagged a graph that built exactly what was described: %s", short)
	}
}

// False positives are the expensive failure here: each one costs a model call
// and puts a warning on a graph that was right. An intent that describes no
// particular structure must never be judged against one.
func TestStructureShortfall_SaysNothingAboutOrdinaryIntents(t *testing.T) {
	quiet := []string{
		"Summarise my unread email every morning and send it to Slack.",
		"When someone messages me, look up the order and reply with its status.",
		"Every hour, check the build and tell me if it broke.",
		"Fetch the news and write a digest.",
		"Ask an analyst to review the numbers.", // ONE role, no parallel cue
		"",
	}
	for _, intent := range quiet {
		if short := StructureShortfall(intent, collapsedGraph()); short != "" {
			t.Errorf("invented a structural requirement from %q:\n    %s", intent, short)
		}
	}
}

// "in parallel" with no enumerated workers is not enough to demand a fan-out —
// the model may well be right that one step does it.
func TestStructureShortfall_NeedsBothAFanOutCueAndSeveralWorkers(t *testing.T) {
	if s := StructureShortfall("Do the lookups in parallel and reply.", collapsedGraph()); s != "" {
		t.Errorf("fired on a parallel cue with no named workers: %s", s)
	}
	if s := StructureShortfall("Use a quality reviewer and a cost reviewer, one after another.", collapsedGraph()); s != "" {
		t.Errorf("fired on named workers with no parallel cue: %s", s)
	}
}

// A serial chain of the right agents is still a shortfall — the user asked for
// concurrency, and serial-when-parallel-was-asked-for is a real difference in
// how long the run takes.
func TestStructureShortfall_FlagsRightAgentsWiredSerially(t *testing.T) {
	serial := fannedOutGraph()
	nodes := serial.Workflow.Flow.Nodes
	kept := nodes[:0]
	for _, n := range nodes {
		if n.Kind == "parallel" {
			continue
		}
		kept = append(kept, n)
	}
	serial.Workflow.Flow.Nodes = kept

	if s := StructureShortfall(supplierIntent, serial); s == "" {
		t.Fatal("three specialists chained one after another was accepted for a request that said in parallel")
	}
}

// A reasoning agent has no graph; judging it by node shape would flag every
// Plan-Execute draft ever generated.
func TestStructureShortfall_IgnoresReasoningAgents(t *testing.T) {
	res := collapsedGraph()
	res.Workflow.Strategy = StrategyPlanExecute
	res.Workflow.Flow = Flow{}
	if s := StructureShortfall(supplierIntent, res); s != "" {
		t.Fatalf("judged a reasoning agent by graph shape: %s", s)
	}
}

func TestPlanFromIntent_ReadsTheNamedRoles(t *testing.T) {
	p := PlanFromIntent(supplierIntent)
	if !p.WantsFanOut {
		t.Error("missed the explicit \"in parallel\"")
	}
	if !p.WantsJoin {
		t.Error("missed \"combines all three reviews into one\"")
	}
	joined := strings.Join(p.Roles, "|")
	for _, want := range []string{"quality reviewer", "cost reviewer", "reliability reviewer"} {
		if !strings.Contains(joined, want) {
			t.Errorf("did not read the role %q out of the intent; got %v", want, p.Roles)
		}
	}
}

// The stock prompt from the first live session, which names its roles a
// different way. The reader has to work on more than one phrasing.
func TestPlanFromIntent_ReadsADifferentPhrasing(t *testing.T) {
	intent := "have three specialists work in parallel: a fundamentals analyst who explains the numbers, " +
		"a risk analyst who flags concerns, and a sentiment analyst who reads the news tone. " +
		"Finally an editor agent combines all three views into one briefing."
	p := PlanFromIntent(intent)
	if !p.WantsFanOut || !p.WantsJoin {
		t.Fatalf("missed the cues: %+v", p)
	}
	if n := p.wantedWorkers(intent); n < 3 {
		t.Errorf("counted %d workers, expected at least 3 (roles: %v)", n, p.Roles)
	}
}

func TestStructureCorrection_NamesTheOmissionAndTheRoles(t *testing.T) {
	c := StructureCorrection(supplierIntent)
	if c == "" {
		t.Fatal("no correction produced for an intent that clearly describes a fan-out")
	}
	for _, want := range []string{"parallel", "quality reviewer", "new_agents"} {
		if !strings.Contains(c, want) {
			t.Errorf("the correction never mentions %q:\n%s", want, c)
		}
	}
	// It must forbid the exact thing the model did, or it is just the brief again.
	if !strings.Contains(strings.ToLower(c), "do not merge") {
		t.Error("the correction should forbid collapsing the workers into one step")
	}
}

func TestStructureCorrection_StaysSilentWhenNothingWasAsked(t *testing.T) {
	if c := StructureCorrection("Summarise my email."); c != "" {
		t.Errorf("produced a correction for an intent with no structure: %s", c)
	}
}

func TestRetryForStructure_KeepsTheRetryWhenItClosesTheGap(t *testing.T) {
	var sawCorrection bool
	build := func(c Catalog) (Result, error) {
		sawCorrection = strings.Contains(c.StructureCorrection, "parallel")
		return fannedOutGraph(), nil
	}
	res, changed, msg := RetryForStructure(supplierIntent, collapsedGraph(), build, Catalog{})
	if !changed {
		t.Fatal("a retry that fixed the structure was discarded")
	}
	if !sawCorrection {
		t.Error("the retry was issued without telling the model what it had missed")
	}
	if len(res.Workflow.Flow.Nodes) != 7 {
		t.Errorf("wrong graph kept: %d nodes", len(res.Workflow.Flow.Nodes))
	}
	if msg == "" {
		t.Error("nothing was said in the transcript about the retry")
	}
	// The draft should carry why it was rebuilt.
	if len(res.Notes) == 0 {
		t.Error("the rebuilt draft carries no note explaining the rebuild")
	}
}

// A second graph that misses too is not evidence of anything; swapping it in
// would churn the canvas for nothing.
func TestRetryForStructure_KeepsTheFirstGraphWhenTheRetryAlsoMisses(t *testing.T) {
	build := func(Catalog) (Result, error) { return collapsedGraph(), nil }
	res, changed, msg := RetryForStructure(supplierIntent, collapsedGraph(), build, Catalog{})
	if changed {
		t.Fatal("kept a retry that had the same shortfall as the first attempt")
	}
	if len(res.Workflow.Flow.Nodes) != 2 {
		t.Error("the first graph was not preserved")
	}
	if msg == "" {
		t.Error("a retry that did not help should still be reported, not hidden")
	}
}

func TestRetryForStructure_SurvivesAFailedRetry(t *testing.T) {
	build := func(Catalog) (Result, error) { return Result{}, errors.New("model timed out") }
	res, changed, _ := RetryForStructure(supplierIntent, collapsedGraph(), build, Catalog{})
	if changed || len(res.Workflow.Flow.Nodes) != 2 {
		t.Fatal("a failed retry lost the graph the user already had")
	}
}

// No shortfall means no retry — the expensive part is the model call, and
// spending one on a graph that was already right is the cost of a false positive.
func TestRetryForStructure_DoesNotCallTheModelWhenTheGraphIsFine(t *testing.T) {
	called := false
	build := func(Catalog) (Result, error) { called = true; return fannedOutGraph(), nil }
	if _, changed, _ := RetryForStructure(supplierIntent, fannedOutGraph(), build, Catalog{}); changed {
		t.Error("reported a change for a graph that needed none")
	}
	if called {
		t.Fatal("spent a model call re-building a graph that already matched the request")
	}
}
