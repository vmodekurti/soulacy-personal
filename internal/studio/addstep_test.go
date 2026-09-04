package studio

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestAppendLinearStepEmptyFlow(t *testing.T) {
	d := Draft{Name: "x"}
	out := AppendLinearStep(d, sdkr.FlowNode{ID: "a", Kind: "tool", Tool: "read_file"})
	if len(out.Flow.Nodes) != 1 {
		t.Fatalf("want 1 node, got %d", len(out.Flow.Nodes))
	}
	if out.Flow.Entry != "a" || out.Flow.Output != "a" {
		t.Errorf("entry/output should be the new node: entry=%q output=%q", out.Flow.Entry, out.Flow.Output)
	}
	if len(out.Flow.Edges) != 0 {
		t.Errorf("first node needs no edge, got %d", len(out.Flow.Edges))
	}
}

func TestAppendLinearStepWiresFromTerminal(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes:  []sdkr.FlowNode{{ID: "a", Kind: "tool", Tool: "fetch_url"}, {ID: "b", Kind: "python"}},
		Edges:  []sdkr.FlowEdge{{From: "a", To: "b"}},
		Entry:  "a",
		Output: "b",
	}}
	out := AppendLinearStep(d, sdkr.FlowNode{ID: "c", Kind: "tool", Tool: "send_telegram"})
	if len(out.Flow.Nodes) != 3 {
		t.Fatalf("want 3 nodes, got %d", len(out.Flow.Nodes))
	}
	if out.Flow.Output != "c" {
		t.Errorf("output should be new node c, got %q", out.Flow.Output)
	}
	// An edge from the previous terminal (b) to c must exist.
	found := false
	for _, e := range out.Flow.Edges {
		if e.From == "b" && e.To == "c" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected edge b→c, edges=%+v", out.Flow.Edges)
	}
	if out.Flow.Entry != "a" {
		t.Errorf("entry should be unchanged, got %q", out.Flow.Entry)
	}
}

func TestAppendLinearStepUniqueID(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{{ID: "step"}}, Output: "step"}}
	out := AppendLinearStep(d, sdkr.FlowNode{ID: "step"})
	ids := map[string]int{}
	for _, n := range out.Flow.Nodes {
		ids[n.ID]++
	}
	for id, count := range ids {
		if count > 1 {
			t.Errorf("duplicate node id %q", id)
		}
	}
	if len(out.Flow.Nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(out.Flow.Nodes))
	}
}
