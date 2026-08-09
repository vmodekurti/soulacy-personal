package reasoning

// A fan-out that bounds its own width must compile, and the bound must mean
// something.
//
// A builder model wrote max_parallel onto a kind=parallel node — which reads
// exactly right, since a fan-out is the thing that runs work at once — and
// CompileFlow refused the whole graph:
//
//	flow: node "parallel_reviewers" declares item_var/max_parallel without for_each
//
// One inert field, and a valid three-specialist workflow was discarded; the
// user got a two-node template instead. Observed live on v0.1.4-33-g5ec2b14,
// once the context-cancel failure stopped masking it.

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func boundedFanOut(maxParallel int, itemVar string) sdkr.FlowSpec {
	return sdkr.FlowSpec{
		Entry: "fan",
		Nodes: []sdkr.FlowNode{
			{ID: "fan", Kind: sdkr.FlowNodeParallel, Join: sdkr.JoinAll, JoinNode: "join",
				MaxParallel: maxParallel, ItemVar: itemVar},
			{ID: "a", Kind: sdkr.FlowNodeLLM, Output: "a_out"},
			{ID: "b", Kind: sdkr.FlowNodeLLM, Output: "b_out"},
			{ID: "c", Kind: sdkr.FlowNodeLLM, Output: "c_out"},
			{ID: "join", Kind: sdkr.FlowNodeLLM, Input: "{{ .a_out }}{{ .b_out }}{{ .c_out }}", Output: "joined"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "fan", To: "a"}, {From: "fan", To: "b"}, {From: "fan", To: "c"},
			{From: "a", To: "join"}, {From: "b", To: "join"}, {From: "c", To: "join"},
		},
	}
}

func TestCompileFlow_AcceptsMaxParallelOnAFanOut(t *testing.T) {
	g, err := CompileFlow(boundedFanOut(2, ""))
	if err != nil {
		t.Fatalf("a three-specialist graph was discarded over one field: %v", err)
	}
	if got := g.Node("fan").MaxParallel; got != 2 {
		t.Errorf("max_parallel = %d, want 2 — the bound was dropped rather than kept", got)
	}
}

// Accepting the field but ignoring it would be its own quiet lie.
func TestRunFlowParallel_HonoursTheDeclaredBound(t *testing.T) {
	if got := fanOutLimit(sdkr.FlowNode{MaxParallel: 2}, 3); got != 2 {
		t.Errorf("limit = %d, want 2 — the author's bound was ignored", got)
	}
	// Unset means "engine default", not "one at a time".
	if got := fanOutLimit(sdkr.FlowNode{}, 3); got != 3 {
		t.Errorf("limit = %d, want 3 (all branches) when no bound is declared", got)
	}
	// It can only narrow: a bound above the engine cap does not widen it.
	if got := fanOutLimit(sdkr.FlowNode{MaxParallel: 1000}, 40); got > maxFlowParallelism {
		t.Errorf("limit = %d exceeds the engine cap %d", got, maxFlowParallelism)
	}
}

// A fan-out iterates over EDGES. There is no item to bind, so saying otherwise
// is an authoring mistake worth naming rather than ignoring.
func TestCompileFlow_RejectsItemVarOnAFanOut(t *testing.T) {
	_, err := CompileFlow(boundedFanOut(0, "row"))
	if err == nil {
		t.Fatal("item_var was accepted on a fan-out, where it does nothing")
	}
	if !strings.Contains(err.Error(), "item_var") {
		t.Errorf("the error does not name the offending field: %v", err)
	}
}

// The original rule still has to hold everywhere it was right.
func TestCompileFlow_StillRejectsIterationFieldsOnAPlainNode(t *testing.T) {
	spec := sdkr.FlowSpec{
		Entry: "a",
		Nodes: []sdkr.FlowNode{{ID: "a", Kind: sdkr.FlowNodeLLM, MaxParallel: 4, Output: "o"}},
	}
	if _, err := CompileFlow(spec); err == nil {
		t.Fatal("max_parallel on a plain step is still meaningless and must be reported")
	}
}
