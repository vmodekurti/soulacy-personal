package studio

// The graph that passed every check and died on its first run.
//
// Weekday Market Digest, generated and saved: schedule → data_gatherer →
// fan_out_specialists(parallel, join: all) → three analysts → editor → telegram.
// Contract: VALID, 0 blockers, 0 warnings. Dry run:
//
//	flow: node "fan_out_specialists": branch "fundamentals_analyst":
//	flow: node "editor": render input: execute template:
//	executing "" at <.risk_analysis>: map has no entry for key "risk_analysis"
//
// The editor ran inside the fundamentals branch. `join: all` says HOW to wait;
// `join_node` says WHERE the branches stop. Only the first was set, so each
// branch walked to the end of the graph and ran its own copy of the editor with
// only its own analysis in scope.

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// digestFlow is the live graph, with join_node deliberately absent.
func digestFlow() Flow {
	return Flow{
		Entry: "gather_data",
		Nodes: []sdkr.FlowNode{
			{ID: "gather_data", Kind: "agent", Agent: "data_gatherer", Output: "data"},
			{ID: "fan_out_specialists", Kind: sdkr.FlowNodeParallel, Join: "all"},
			{ID: "fundamentals_analyst", Kind: "agent", Agent: "fundamentals_analyst", Output: "fundamentals_analysis"},
			{ID: "risk_analyst", Kind: "agent", Agent: "risk_analyst", Output: "risk_analysis"},
			{ID: "sentiment_analyst", Kind: "agent", Agent: "sentiment_analyst", Output: "sentiment_analysis"},
			{ID: "editor", Kind: "agent", Agent: "editor", Output: "briefing"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "gather_data", To: "fan_out_specialists"},
			{From: "fan_out_specialists", To: "fundamentals_analyst"},
			{From: "fan_out_specialists", To: "risk_analyst"},
			{From: "fan_out_specialists", To: "sentiment_analyst"},
			{From: "fundamentals_analyst", To: "editor"},
			{From: "risk_analyst", To: "editor"},
			{From: "sentiment_analyst", To: "editor"},
		},
	}
}

func joinNodeByID(f Flow, id string) sdkr.FlowNode {
	for _, n := range f.Nodes {
		if n.ID == id {
			return n
		}
	}
	return sdkr.FlowNode{}
}

func TestInferJoinNodes_FindsTheBarrierInTheLiveGraph(t *testing.T) {
	f := digestFlow()
	if n := InferJoinNodes(&f); n != 1 {
		t.Fatalf("expected to infer 1 barrier, set %d", n)
	}
	if got := joinNodeByID(f, "fan_out_specialists").JoinNode; got != "editor" {
		t.Fatalf("barrier = %q, want \"editor\" — without it the editor runs once per branch", got)
	}
}

// An author who named the barrier owns that decision.
func TestInferJoinNodes_DoesNotOverrideAnExplicitBarrier(t *testing.T) {
	f := digestFlow()
	for i := range f.Nodes {
		if f.Nodes[i].ID == "fan_out_specialists" {
			f.Nodes[i].JoinNode = "risk_analyst"
		}
	}
	if n := InferJoinNodes(&f); n != 0 {
		t.Fatal("overwrote a join_node the author had set")
	}
	if got := joinNodeByID(f, "fan_out_specialists").JoinNode; got != "risk_analyst" {
		t.Fatalf("join_node was changed to %q", got)
	}
}

// Branches that genuinely end separately need no barrier, and inventing one
// would stop them short.
func TestInferJoinNodes_LeavesNonConvergingBranchesAlone(t *testing.T) {
	f := Flow{
		Entry: "fan",
		Nodes: []sdkr.FlowNode{
			{ID: "fan", Kind: sdkr.FlowNodeParallel, Join: "all"},
			{ID: "email", Kind: "tool", Tool: "channel.send"},
			{ID: "slack", Kind: "tool", Tool: "channel.send"},
		},
		Edges: []sdkr.FlowEdge{{From: "fan", To: "email"}, {From: "fan", To: "slack"}},
	}
	if n := InferJoinNodes(&f); n != 0 {
		t.Fatalf("invented a barrier for branches that never meet (set %d)", n)
	}
}

// The barrier is the EARLIEST shared node. Picking a later one would let the
// branches run past the real convergence point and execute it twice.
func TestConvergenceOf_PicksTheEarliestSharedNode(t *testing.T) {
	f := digestFlow()
	f.Nodes = append(f.Nodes, sdkr.FlowNode{ID: "deliver", Kind: "tool", Tool: "channel.send"})
	f.Edges = append(f.Edges, sdkr.FlowEdge{From: "editor", To: "deliver"})

	if got := ConvergenceOf(f, joinNodeByID(f, "fan_out_specialists")); got != "editor" {
		t.Fatalf("barrier = %q, want \"editor\" (deliver is downstream of the join, not the join)", got)
	}
}

// When the branches meet in more than one place with no single first point,
// guessing runs the wrong node once instead of the right node three times.
// Silence here hands the case to the contract check instead.
func TestConvergenceOf_DeclinesToGuessAnAmbiguousShape(t *testing.T) {
	f := Flow{
		Entry: "fan",
		Nodes: []sdkr.FlowNode{
			{ID: "fan", Kind: sdkr.FlowNodeParallel, Join: "all"},
			{ID: "a", Kind: "llm"}, {ID: "b", Kind: "llm"},
			{ID: "x", Kind: "llm"}, {ID: "y", Kind: "llm"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "fan", To: "a"}, {From: "fan", To: "b"},
			// Both branches reach x and y, but neither x nor y reaches the other.
			{From: "a", To: "x"}, {From: "a", To: "y"},
			{From: "b", To: "x"}, {From: "b", To: "y"},
		},
	}
	if got := ConvergenceOf(f, joinNodeByID(f, "fan")); got != "" {
		t.Fatalf("guessed %q for a shape with two equally-first candidates", got)
	}
}

func TestRepairWiring_SetsTheBarrier(t *testing.T) {
	d := Draft{Name: "Digest", Trigger: Trigger{Type: "cron"}, Flow: digestFlow()}
	RepairWiring(&d, Catalog{})
	if got := joinNodeByID(d.Flow, "fan_out_specialists").JoinNode; got != "editor" {
		t.Fatalf("RepairWiring left the barrier as %q — generated graphs still ship unrunnable", got)
	}
}

// What repair cannot infer, the contract must report — as a blocker, because
// the workflow cannot complete.
func TestContract_BlocksAConvergingFanOutWithNoBarrier(t *testing.T) {
	d := Draft{Name: "Digest", Trigger: Trigger{Type: "cron"}, Flow: digestFlow()}
	r := AssessContract(d, Catalog{}, PreflightInput{})

	c, ok := checkFor(r, "graph.joinbarrier")
	if !ok || c.Status != "block" {
		t.Fatalf("a graph that dies on its first run was reported clean; checks: %+v", r.Checks)
	}
	if c.NodeID != "fan_out_specialists" {
		t.Errorf("the blocker should point at the fan-out, got node %q", c.NodeID)
	}
	// Studio computed where the branches meet, so the button should APPLY that
	// rather than send the user to the canvas to work it out again.
	if c.Action != FixSetJoinNode {
		t.Errorf("an inferable barrier should be offered as a one-click fix, got action %q", c.Action)
	}
	if c.ActionParams["join"] != "editor" || c.ActionParams["node"] != "fan_out_specialists" {
		t.Errorf("the fix carries the wrong target: %v", c.ActionParams)
	}
	if c.ActionLabel == "" {
		t.Error("the button would render with no text on it")
	}
}

// When the barrier cannot be worked out, the button must fall back to showing
// the step rather than offering to apply a value Studio does not have.
func TestContract_FallsBackToShowingTheStepWhenAmbiguous(t *testing.T) {
	d := Draft{Name: "Ambiguous", Trigger: Trigger{Type: "cron"}, Flow: Flow{
		Entry: "fan",
		Nodes: []sdkr.FlowNode{
			{ID: "fan", Kind: sdkr.FlowNodeParallel, Join: "all"},
			{ID: "a", Kind: "llm"}, {ID: "b", Kind: "llm"},
			{ID: "x", Kind: "llm"}, {ID: "y", Kind: "llm"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "fan", To: "a"}, {From: "fan", To: "b"},
			{From: "a", To: "x"}, {From: "a", To: "y"},
			{From: "b", To: "x"}, {From: "b", To: "y"},
		},
	}}
	c, ok := checkFor(AssessContract(d, Catalog{}, PreflightInput{}), "graph.joinbarrier")
	if !ok || c.Status != "block" {
		t.Fatal("an ambiguous converging fan-out should still block")
	}
	if c.Action == FixSetJoinNode {
		t.Error("offered to apply a barrier Studio could not work out")
	}
}

func TestContract_PassesOnceTheBarrierIsNamed(t *testing.T) {
	d := Draft{Name: "Digest", Trigger: Trigger{Type: "cron"}, Flow: digestFlow()}
	RepairWiring(&d, Catalog{})
	r := AssessContract(d, Catalog{}, PreflightInput{})

	if c, ok := checkFor(r, "graph.joinbarrier"); !ok || c.Status != "pass" {
		t.Fatalf("expected a passing join check after repair, got %+v", c)
	}
}

// Fan-outs that never reconverge must not be blocked.
func TestContract_DoesNotBlockBranchesThatEndSeparately(t *testing.T) {
	d := Draft{Name: "Notify", Trigger: Trigger{Type: "cron"}, Flow: Flow{
		Entry: "fan",
		Nodes: []sdkr.FlowNode{
			{ID: "fan", Kind: sdkr.FlowNodeParallel, Join: "all"},
			{ID: "email", Kind: "tool", Tool: "channel.send"},
			{ID: "slack", Kind: "tool", Tool: "channel.send"},
		},
		Edges: []sdkr.FlowEdge{{From: "fan", To: "email"}, {From: "fan", To: "slack"}},
	}}
	if c, ok := checkFor(AssessContract(d, Catalog{}, PreflightInput{}), "graph.joinbarrier"); ok && c.Status == "block" {
		t.Fatal("blocked a fan-out whose branches are meant to end separately")
	}
}
