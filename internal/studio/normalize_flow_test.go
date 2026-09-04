package studio

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestNormalizeFlow_CanonicalizesInventedKinds(t *testing.T) {
	d := &Draft{}
	d.Flow.Nodes = []sdkr.FlowNode{
		{ID: "s", Kind: "start"},
		{ID: "x", Kind: "llm_extract"},
		{ID: "e", Kind: "end"},
	}
	normalizeFlow(d)

	want := map[string]string{"s": sdkr.FlowNodeTrigger, "x": sdkr.FlowNodeLLM, "e": sdkr.FlowNodeExit}
	for _, n := range d.Flow.Nodes {
		if got := n.Kind; got != want[n.ID] {
			t.Fatalf("node %s kind = %q, want %q", n.ID, got, want[n.ID])
		}
	}
}

func TestEnsureContentOutput_RepointsAwayFromChannelSend(t *testing.T) {
	d := &Draft{}
	d.Flow.Nodes = []sdkr.FlowNode{
		{ID: "extract", Kind: "llm"},
		{ID: "chart", Kind: "tool", Tool: "generate_chart"},
		{ID: "format", Kind: "agent", Agent: "formatter"},
		{ID: "send", Kind: "tool", Tool: "channel.send"},
	}
	d.Flow.Edges = []sdkr.FlowEdge{
		{From: "format", To: "send"},
	}
	d.Flow.Output = "send" // model wrongly made the delivery node the output

	ensureContentOutput(d)

	if d.Flow.Output != "format" {
		t.Fatalf("output = %q, want the content node 'format' feeding the delivery node", d.Flow.Output)
	}
}

func TestEnsureContentOutput_FallbackToLastContentNode(t *testing.T) {
	d := &Draft{}
	d.Flow.Nodes = []sdkr.FlowNode{
		{ID: "chart", Kind: "tool", Tool: "generate_chart"},
		{ID: "send", Kind: "tool", Tool: "channel.send"},
	}
	// No edge into send; default output = last node = send (delivery).
	ensureContentOutput(d)
	if d.Flow.Output != "chart" {
		t.Fatalf("output = %q, want fallback to last content node 'chart'", d.Flow.Output)
	}
}

func TestEnsureContentOutput_LeavesContentOutputAlone(t *testing.T) {
	d := &Draft{}
	d.Flow.Nodes = []sdkr.FlowNode{
		{ID: "answer", Kind: "agent", Agent: "helper"},
	}
	d.Flow.Output = "answer"
	ensureContentOutput(d)
	if d.Flow.Output != "answer" {
		t.Fatalf("content output should be untouched, got %q", d.Flow.Output)
	}
}
