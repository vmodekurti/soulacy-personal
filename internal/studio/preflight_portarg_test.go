package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// A required tool argument supplied via a typed input PORT must NOT be flagged as
// "empty" by preflight — otherwise the build loop repairs it forever (the LLM
// re-adds the arg, the portizer re-wires it). Regression for the 5-problem loop.
func TestPreflight_RequiredArgViaPortNotFlagged(t *testing.T) {
	cat := Catalog{MCP: []CatalogMCPServer{{
		Server: "notebooklm",
		Tools: []CatalogMCPTool{
			{Name: "mcp__notebooklm__notebook_create", Params: "title*:string"},
			{Name: "mcp__notebooklm__research_start", Params: "notebook_id*:string, query*:string"},
		},
	}}}

	d := Draft{
		Name:    "nb",
		Trigger: Trigger{Type: "manual"},
		Flow: Flow{
			Nodes: []sdkr.FlowNode{
				{ID: "create", Kind: "tool", Tool: "mcp__notebooklm__notebook_create",
					Input: `{"title":"AI News"}`, Output: "notebook",
					Outputs: []sdkr.FlowPort{{Name: "id", Field: "id"}}},
				// notebook_id arrives via a typed input port (not in Input); query is in Input.
				{ID: "research", Kind: "tool", Tool: "mcp__notebooklm__research_start",
					Input:  `{"query":"latest AI"}`,
					Inputs: []sdkr.FlowPort{{Name: "notebook_id"}}},
			},
			Edges: []sdkr.FlowEdge{{From: "create", To: "research", FromPort: "id", ToPort: "notebook_id"}},
			Entry: "create",
		},
	}

	pf := Preflight(d, PreflightInput{Catalog: cat, ConnectedMCP: map[string]bool{"notebooklm": true}})
	for _, b := range pf.Blockers {
		if b.NodeID == "research" && strings.Contains(b.Message, "notebook_id") {
			t.Fatalf("port-supplied required arg must not be flagged empty; got blocker: %+v", b)
		}
	}
}
