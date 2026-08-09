package studio

// Studio must not hand the engine a field the engine will reject the graph for.
//
// The live failure, on v0.1.4-33-g5ec2b14:
//
//	studio: compiled flow is invalid: flow: node "parallel_reviewers"
//	declares item_var/max_parallel without for_each
//
// A whole three-specialist workflow discarded, and the user shown a two-node
// template, because the model decorated its fan-out with settings that read
// correct. max_parallel now means something on a fan-out; item_var never can,
// so it is stripped here rather than fatal there.

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestReconcileIterationFields_KeepsTheBoundOnAFanOut(t *testing.T) {
	d := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "fan", Kind: sdkr.FlowNodeParallel, MaxParallel: 3, ItemVar: "row"},
	}}}
	reconcileIterationFields(d)

	if got := d.Flow.Nodes[0].MaxParallel; got != 3 {
		t.Errorf("max_parallel = %d, want 3 — it bounds fan-out width and must survive", got)
	}
	if got := d.Flow.Nodes[0].ItemVar; got != "" {
		t.Errorf("item_var = %q, want empty — a fan-out has no item to bind", got)
	}
}

func TestReconcileIterationFields_StripsBothFromAPlainStep(t *testing.T) {
	d := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "step", Kind: sdkr.FlowNodeLLM, MaxParallel: 4, ItemVar: "row"},
	}}}
	reconcileIterationFields(d)

	if d.Flow.Nodes[0].MaxParallel != 0 || d.Flow.Nodes[0].ItemVar != "" {
		t.Errorf("a plain step kept iteration settings it cannot use: %+v", d.Flow.Nodes[0])
	}
}

// A real for_each owns both fields and must be left completely alone.
func TestReconcileIterationFields_LeavesARealForEachIntact(t *testing.T) {
	d := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "each", Kind: sdkr.FlowNodeLLM, ForEach: "{{ toJson .items }}", ItemVar: "row", MaxParallel: 4},
	}}}
	reconcileIterationFields(d)

	n := d.Flow.Nodes[0]
	if n.ItemVar != "row" || n.MaxParallel != 4 {
		t.Errorf("a genuine for_each was stripped: %+v", n)
	}
}
