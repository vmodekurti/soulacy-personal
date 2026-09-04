package studio

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// The exact shape a weak local model produced: bare branch nodes, no tools, no
// edges — which used to render as a canvas full of meaningless BRANCH boxes.
func TestDegenerateReason_PlaceholderBranches(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "A", Kind: "branch"},
			{ID: "B", Kind: "branch"},
			{ID: "C", Kind: "branch"},
		},
	}}
	if got := DegenerateReason(d); got == "" {
		t.Fatal("expected a placeholder-only draft to be flagged degenerate")
	}
}

func TestDegenerateReason_DisconnectedSteps(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Kind: "tool", Tool: "fetch_url"},
			{ID: "b", Kind: "tool", Tool: "send_telegram"},
		},
		// real tools, but no edges at all
	}}
	if got := DegenerateReason(d); got == "" {
		t.Fatal("expected disconnected multi-step flow to be flagged degenerate")
	}
}

func TestDegenerateReason_AcceptsRealWorkflow(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Kind: "tool", Tool: "fetch_url"},
			{ID: "b", Kind: "python", Code: "def run(inputs):\n    return inputs"},
		},
		Edges: []sdkr.FlowEdge{{From: "a", To: "b"}},
		Entry: "a",
	}}
	if got := DegenerateReason(d); got != "" {
		t.Fatalf("a real workflow must not be flagged: %s", got)
	}
}

func TestDegenerateReason_AcceptsSingleNode(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{{ID: "a", Kind: "tool", Tool: "get_weather"}},
		Entry: "a",
	}}
	if got := DegenerateReason(d); got != "" {
		t.Fatalf("a single-step workflow needs no edges: %s", got)
	}
}

// A reasoning agent has no flow by design — it must never be flagged.
func TestDegenerateReason_IgnoresAgentDraft(t *testing.T) {
	d := Draft{Strategy: "react", Tools: []string{"web_search"}}
	if got := DegenerateReason(d); got != "" {
		t.Fatalf("agent draft must not be flagged: %s", got)
	}
}

// A branch node alongside real work is legitimate.
func TestDegenerateReason_AcceptsBranchWithRealWork(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Kind: "tool", Tool: "fetch_url"},
			{ID: "gate", Kind: "branch"},
		},
		Edges: []sdkr.FlowEdge{{From: "a", To: "gate"}},
		Entry: "a",
	}}
	if got := DegenerateReason(d); got != "" {
		t.Fatalf("branch + real tool must not be flagged: %s", got)
	}
}
