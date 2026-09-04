package studio

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// Phase E parity: ensureNodeIntents seeds Intent from Description for real nodes
// (so a generated node is re-editable as a prompt) but leaves structural endpoint
// blocks and already-set intents alone.
func TestEnsureNodeIntents(t *testing.T) {
	f := &Flow{Nodes: []sdkr.FlowNode{
		{ID: "a", Kind: "tool", Tool: "web_search", Description: "Search the web for AI news"},
		{ID: "b", Kind: "agent", Agent: "summarizer", Description: "Summarize", Intent: "already set"},
		{ID: "t", Kind: sdkr.FlowNodeTrigger, Description: "Trigger"},
		{ID: "e", Kind: sdkr.FlowNodeExit, Description: "Exit"},
	}}
	ensureNodeIntents(f)

	if f.Nodes[0].Intent != "Search the web for AI news" {
		t.Errorf("node a should seed Intent from Description, got %q", f.Nodes[0].Intent)
	}
	if f.Nodes[1].Intent != "already set" {
		t.Errorf("an existing Intent must not be overwritten, got %q", f.Nodes[1].Intent)
	}
	if f.Nodes[2].Intent != "" || f.Nodes[3].Intent != "" {
		t.Error("structural endpoint blocks should not get an Intent")
	}
}
