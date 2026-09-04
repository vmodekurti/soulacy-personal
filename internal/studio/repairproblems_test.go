package studio

import (
	"context"
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestRepairWithProblems_FixesFromMessages(t *testing.T) {
	d := Draft{Name: "X", Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "a", Kind: "tool", Tool: "mk", Output: "notebook", Input: `{"title":"t"}`},
		{ID: "b", Kind: "tool", Tool: "use", Input: `{"id":"{{ .missing }}"}`},
	}, Edges: []sdkr.FlowEdge{{From: "a", To: "b"}}}}
	// Model returns the corrected full draft (wires id from notebook).
	fixedJSON := `{"name":"X","trigger":{"type":"manual"},"flow":{"nodes":[
	  {"id":"a","kind":"tool","tool":"mk","output":"notebook","input":"{\"title\":\"t\"}"},
	  {"id":"b","kind":"tool","tool":"use","input":"{\"id\":\"{{ .notebook }}\"}"}
	],"edges":[{"from":"a","to":"b"}],"entry":"a"}}`
	out, changed := RepairWithProblems(context.Background(), fakeLLM{out: fixedJSON}, d, []string{`node "b": references {{ .missing }} but no step produces it`}, Catalog{})
	if !changed {
		t.Fatal("expected repair to apply")
	}
	// The model's fix (wire id from the notebook output) is now lowered to a typed
	// PORT wire, not a Go-template string: node b declares an input port "id" and
	// an edge a→b carries it (to_port "id"). The template handoff is gone — that is
	// the redesign's "typed ports, not magic strings" working end to end.
	if strings.Contains(out.Flow.Nodes[1].Input, "{{") {
		t.Errorf("handoff should be a port, not a template: %q", out.Flow.Nodes[1].Input)
	}
	if !outputPortExists(out.Flow.Nodes[1].Inputs, "id") {
		t.Errorf("expected input port \"id\" on node b; got %+v", out.Flow.Nodes[1].Inputs)
	}
	var wired bool
	for _, e := range out.Flow.Edges {
		if e.From == "a" && e.To == "b" && e.ToPort == "id" {
			wired = true
		}
	}
	if !wired {
		t.Errorf("expected edge a→b wired to_port \"id\"; got %+v", out.Flow.Edges)
	}
}

func TestRepairWithProblems_NoProblemsNoCall(t *testing.T) {
	d := Draft{Name: "X"}
	if _, changed := RepairWithProblems(context.Background(), fakeLLM{err: context.DeadlineExceeded}, d, nil, Catalog{}); changed {
		t.Error("no problems → no call/change")
	}
}
