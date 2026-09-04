package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestReconcileVars_FixesNearMissName(t *testing.T) {
	// get_date outputs "date_info"; create_notebook references {{ .date_str }}.
	// No node produces date_str → reconcile to the ancestor's date_info (shared
	// "date" token, unique winner).
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "get_date", Kind: "tool", Tool: "clock", Output: "date_info", Input: `{}`},
			{ID: "create_notebook", Kind: "tool", Tool: "mk", Output: "notebook_info", Input: `{"title":"AI News - {{ .date_str }}"}`},
		},
		Edges: []sdkr.FlowEdge{{From: "get_date", To: "create_notebook"}},
	}}
	n := ReconcileVars(&d)
	if n != 1 {
		t.Fatalf("expected 1 reconcile, got %d", n)
	}
	if !strings.Contains(d.Flow.Nodes[1].Input, "{{ .date_info }}") {
		t.Errorf("date_str not reconciled to date_info: %q", d.Flow.Nodes[1].Input)
	}
	// And preflight should now be clean of dependency blockers.
	r := Preflight(d, PreflightInput{})
	if blockKinds(r)["dependency"] != 0 {
		t.Errorf("expected no dependency blocker after reconcile: %+v", r.Blockers)
	}
}

func TestReconcileVars_AmbiguousLeftAlone(t *testing.T) {
	// Two equally-plausible ancestors (both share no tokens with the ref) → no
	// guess; the reference is left for the user/LLM.
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Kind: "tool", Tool: "t", Output: "alpha", Input: `{}`},
			{ID: "b", Kind: "tool", Tool: "t", Output: "beta", Input: `{}`},
			{ID: "c", Kind: "tool", Tool: "t", Input: `{"x":"{{ .gamma }}"}`},
		},
		Edges: []sdkr.FlowEdge{{From: "a", To: "c"}, {From: "b", To: "c"}},
	}}
	if n := ReconcileVars(&d); n != 0 {
		t.Errorf("ambiguous/no-overlap ref should not be reconciled, got %d", n)
	}
}
