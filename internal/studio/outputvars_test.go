package studio

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// A producer with a blank output var would have its result dropped, so a wired
// consumer reads null. ensureOutputVars must assign every executable node a var.
func TestEnsureOutputVars_AssignsMissing(t *testing.T) {
	f := &Flow{Nodes: []sdkr.FlowNode{
		{ID: "filter_and_rank", Kind: "python", Output: ""}, // blank → must be filled
		{ID: "make_podcast", Kind: "python", Output: "podcast"},
		{ID: "trig", Kind: sdkr.FlowNodeTrigger},
		{ID: "out", Kind: sdkr.FlowNodeExit},
	}}
	n := ensureOutputVars(f)
	if n != 1 {
		t.Fatalf("expected 1 assignment, got %d", n)
	}
	if f.Nodes[0].Output != "filter_and_rank" {
		t.Errorf("blank output should default to the node id, got %q", f.Nodes[0].Output)
	}
	if f.Nodes[1].Output != "podcast" {
		t.Errorf("existing output must be preserved, got %q", f.Nodes[1].Output)
	}
	if f.Nodes[2].Output != "" || f.Nodes[3].Output != "" {
		t.Error("structural endpoint nodes should not get an output var")
	}
}

// Assigned names must be unique (no clobbering an existing var).
func TestEnsureOutputVars_Unique(t *testing.T) {
	f := &Flow{Nodes: []sdkr.FlowNode{
		{ID: "step", Kind: "tool", Tool: "a", Output: "step"}, // takes "step"
		{ID: "step", Kind: "tool", Tool: "b", Output: ""},     // collides → step_2
	}}
	ensureOutputVars(f)
	if f.Nodes[1].Output == "" || f.Nodes[1].Output == "step" {
		t.Errorf("expected a unique non-colliding var, got %q", f.Nodes[1].Output)
	}
}

// Non-identifier characters in an id are sanitized into a valid var.
func TestEnsureOutputVars_Sanitizes(t *testing.T) {
	f := &Flow{Nodes: []sdkr.FlowNode{
		{ID: "fetch-news!", Kind: "tool", Tool: "x", Output: ""},
	}}
	ensureOutputVars(f)
	if got := f.Nodes[0].Output; got != "fetch_news" {
		t.Errorf("expected sanitized var fetch_news, got %q", got)
	}
}
