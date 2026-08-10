package studio

// The size rule must not refuse the shape it just built.
//
// Asked live for three reviewers in parallel plus an editor, the builder model
// produced exactly that — and the contract blocked it:
//
//	architecture.size: This workflow has 9 nodes and is likely too brittle
//	for a visual Macro-Workflow.
//
// with a suggested fix of "switch Mode to Auto", which abandons the fan-out
// entirely, because a reasoning agent cannot express one. Nine nodes is what
// this request costs: fetch, a guard branch, the fan-out, three reviewers, the
// editor, a quiet path, and delivery. There is nothing to merge.

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// liveIncidentFlow is the graph Studio generated on v0.1.4-32-g89669ea.
func liveIncidentFlow() Flow {
	return Flow{
		Entry: "fetch_incidents",
		Nodes: []sdkr.FlowNode{
			{ID: "fetch_incidents", Kind: sdkr.FlowNodePython, Code: "def run(inputs):\n    return {}\n", Output: "incidents"},
			{ID: "have_incidents", Kind: sdkr.FlowNodeBranch},
			{ID: "parallel_reviews", Kind: sdkr.FlowNodeParallel, Join: sdkr.JoinAll, JoinNode: "editor"},
			{ID: "severity_review", Kind: sdkr.FlowNodeAgent, Agent: "severity_reviewer", Output: "severity"},
			{ID: "root_cause_review", Kind: sdkr.FlowNodeAgent, Agent: "root_cause_reviewer", Output: "cause"},
			{ID: "customer_impact_review", Kind: sdkr.FlowNodeAgent, Agent: "impact_reviewer", Output: "impact"},
			{ID: "editor", Kind: sdkr.FlowNodeAgent, Agent: "editor", Output: "content"},
			{ID: "say_quiet", Kind: sdkr.FlowNodeAgent, Agent: "notifier", Output: "quiet"},
			{ID: "deliver", Kind: sdkr.FlowNodeAgent, Agent: "notifier", Output: "sent"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "fetch_incidents", To: "have_incidents"},
			{From: "have_incidents", To: "parallel_reviews"},
			{From: "have_incidents", To: "say_quiet"},
			{From: "parallel_reviews", To: "severity_review"},
			{From: "parallel_reviews", To: "root_cause_review"},
			{From: "parallel_reviews", To: "customer_impact_review"},
			{From: "severity_review", To: "editor"},
			{From: "root_cause_review", To: "editor"},
			{From: "customer_impact_review", To: "editor"},
			{From: "editor", To: "deliver"},
		},
	}
}

func TestContract_DoesNotBlockTheFanOutItAskedFor(t *testing.T) {
	d := Draft{Name: "Incident Digest", Trigger: Trigger{Type: "schedule"}, Flow: liveIncidentFlow()}
	c, ok := checkFor(AssessContract(d, Catalog{}, PreflightInput{}), "architecture.size")
	if ok && c.Status == "block" {
		t.Fatalf("blocked a three-way fan-out for being too big: %s", c.Message)
	}
}

// Three peer reviewers are one idea, so they cost one branch's worth.
func TestMacroStepCount_CountsPeerBranchesOnce(t *testing.T) {
	got := MacroStepCount(liveIncidentFlow())
	// 9 nodes − 2 discounted reviewers = 7.
	if got != 7 {
		t.Errorf("step count = %d, want 7 (9 nodes, two of the three reviewers discounted)", got)
	}
}

// The rule still has to bite. A long straight line is exactly what it was
// written for, and nothing here should soften it.
func TestMacroStepCount_UnchangedWithoutAFanOut(t *testing.T) {
	f := Flow{Entry: "n0"}
	for i := 0; i < 10; i++ {
		id := string(rune('a' + i))
		f.Nodes = append(f.Nodes, sdkr.FlowNode{ID: id, Kind: sdkr.FlowNodeLLM, Output: id + "_out"})
		if i > 0 {
			f.Edges = append(f.Edges, sdkr.FlowEdge{From: string(rune('a' + i - 1)), To: id})
		}
	}
	f.Entry = "a"
	if got := MacroStepCount(f); got != 10 {
		t.Errorf("a 10-step straight line counted as %d — the size rule has been weakened", got)
	}
	d := Draft{Name: "Sprawl", Trigger: Trigger{Type: "schedule"}, Flow: f}
	c, ok := checkFor(AssessContract(d, Catalog{}, PreflightInput{}), "architecture.size")
	if !ok || c.Status != "block" {
		t.Fatalf("a 10-step straight line should still block, got %+v", c)
	}
}

// A branch that does real work of its own is not free. Discounting the LONGEST
// branch would let one fan-out hide an arbitrary pipeline.
func TestMacroStepCount_KeepsTheWidestBranch(t *testing.T) {
	f := Flow{
		Entry: "fan",
		Nodes: []sdkr.FlowNode{
			{ID: "fan", Kind: sdkr.FlowNodeParallel, Join: sdkr.JoinAll, JoinNode: "join"},
			{ID: "short", Kind: sdkr.FlowNodeLLM, Output: "s"},
			{ID: "long1", Kind: sdkr.FlowNodeLLM, Output: "l1"},
			{ID: "long2", Kind: sdkr.FlowNodeLLM, Output: "l2"},
			{ID: "long3", Kind: sdkr.FlowNodeLLM, Output: "l3"},
			{ID: "join", Kind: sdkr.FlowNodeLLM, Output: "j"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "fan", To: "short"}, {From: "fan", To: "long1"},
			{From: "long1", To: "long2"}, {From: "long2", To: "long3"},
			{From: "long3", To: "join"}, {From: "short", To: "join"},
		},
	}
	// 6 nodes − the one-node short branch = 5. The three-node branch is kept.
	if got := MacroStepCount(f); got != 5 {
		t.Errorf("step count = %d, want 5 (only the smaller branch is discounted)", got)
	}
}

// A node several branches pass through is shared work, already counted once.
// Discounting it would undercount the graph.
func TestMacroStepCount_DoesNotDiscountSharedNodes(t *testing.T) {
	f := Flow{
		Entry: "fan",
		Nodes: []sdkr.FlowNode{
			{ID: "fan", Kind: sdkr.FlowNodeParallel, Join: sdkr.JoinAll},
			{ID: "a", Kind: sdkr.FlowNodeLLM, Output: "a"},
			{ID: "b", Kind: sdkr.FlowNodeLLM, Output: "b"},
			{ID: "shared", Kind: sdkr.FlowNodeLLM, Output: "s"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "fan", To: "a"}, {From: "fan", To: "b"},
			{From: "a", To: "shared"}, {From: "b", To: "shared"},
		},
	}
	// 4 nodes; only "b" is exclusive to a second branch, so the discount is 1.
	if got := MacroStepCount(f); got != 3 {
		t.Errorf("step count = %d, want 3 — \"shared\" must not be discounted", got)
	}
}

// The count can only ever relax the rule.
func TestMacroStepCount_NeverExceedsTheNodeCount(t *testing.T) {
	for name, f := range map[string]Flow{
		"live":   liveIncidentFlow(),
		"empty":  {},
		"single": {Entry: "a", Nodes: []sdkr.FlowNode{{ID: "a", Kind: sdkr.FlowNodeLLM}}},
	} {
		if got, n := MacroStepCount(f), len(f.Nodes); got > n {
			t.Errorf("%s: step count %d exceeds node count %d", name, got, n)
		}
	}
}
