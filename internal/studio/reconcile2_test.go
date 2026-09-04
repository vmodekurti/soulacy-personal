package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestReconcileVars_SubsetBreaksTie(t *testing.T) {
	// ref notebook_id; two ancestors: notebook (subset of ref) and
	// notebook_params (extra token). Should pick notebook.
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "title", Kind: "tool", Tool: "t", Output: "notebook_params", Input: `{}`},
			{ID: "create", Kind: "tool", Tool: "mk", Output: "notebook", Input: `{}`},
			{ID: "add", Kind: "tool", Tool: "add", Input: `{"id":"{{ .notebook_id }}"}`},
		},
		Edges: []sdkr.FlowEdge{{From: "title", To: "create"}, {From: "create", To: "add"}},
	}}
	if n := ReconcileVars(&d); n != 1 {
		t.Fatalf("expected 1 reconcile, got %d", n)
	}
	if !strings.Contains(d.Flow.Nodes[2].Input, "{{ .notebook }}") {
		t.Errorf("notebook_id should resolve to notebook: %q", d.Flow.Nodes[2].Input)
	}
}
