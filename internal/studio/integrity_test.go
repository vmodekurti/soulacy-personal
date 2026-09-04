package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestPreflight_RequiredMCPArgNeedsActualPortWire(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{
				ID:     "generate",
				Kind:   "tool",
				Tool:   "mcp__browser__generate",
				Inputs: []sdkr.FlowPort{{Name: "url"}},
			},
		},
		Entry: "generate",
	}}
	r := Preflight(d, PreflightInput{Catalog: Catalog{MCP: []CatalogMCPServer{{
		Server: "browser",
		Tools:  []CatalogMCPTool{{Name: "mcp__browser__generate", Params: "url*:string"}},
	}}}})
	if r.OK {
		t.Fatal("expected unwired required input port to block preflight")
	}
	var msg string
	for _, b := range r.Blockers {
		if b.NodeID == "generate" {
			msg = b.Message
		}
	}
	if !strings.Contains(msg, `Required argument "url"`) {
		t.Fatalf("expected required arg blocker, got blockers=%+v", r.Blockers)
	}
}

func TestPreflight_RequiredMCPArgSatisfiedByActualPortWire(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{
				ID:      "resolve",
				Kind:    "tool",
				Tool:    "lookup",
				Output:  "lookup_result",
				Outputs: []sdkr.FlowPort{{Name: "url"}},
			},
			{
				ID:     "generate",
				Kind:   "tool",
				Tool:   "mcp__browser__generate",
				Inputs: []sdkr.FlowPort{{Name: "url"}},
			},
		},
		Edges: []sdkr.FlowEdge{{From: "resolve", To: "generate", FromPort: "url", ToPort: "url"}},
		Entry: "resolve",
	}}
	r := Preflight(d, PreflightInput{Catalog: Catalog{MCP: []CatalogMCPServer{{
		Server: "browser",
		Tools:  []CatalogMCPTool{{Name: "mcp__browser__generate", Params: "url*:string"}},
	}}}})
	for _, b := range r.Blockers {
		if b.NodeID == "generate" && strings.Contains(b.Message, `Required argument "url"`) {
			t.Fatalf("actual typed wire should satisfy required arg, got blockers=%+v", r.Blockers)
		}
	}
}
