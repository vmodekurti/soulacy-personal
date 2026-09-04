package studio

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestSuggestMissing_ConnectedMCPToolNotFlagged(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "n1", Kind: "tool", Tool: "mcp__notebooklm__notebook_create", Input: `{"title":"x"}`},
		{ID: "n2", Kind: "tool", Tool: "mcp__notebooklm__not_real", Input: `{}`},
	}}}
	cat := Catalog{
		Tools: []string{"web_search"}, // builtins present so guard passes
		MCP: []CatalogMCPServer{{
			Server: "notebooklm",
			Tools:  []CatalogMCPTool{{Name: "mcp__notebooklm__notebook_create"}},
		}},
	}
	missing := suggestMissing(d, cat)
	for _, m := range missing {
		if m.Name == "mcp__notebooklm__notebook_create" {
			t.Errorf("connected MCP tool should NOT be flagged missing: %+v", missing)
		}
	}
	// The genuinely-absent MCP tool SHOULD still be flagged.
	found := false
	for _, m := range missing {
		if m.Name == "mcp__notebooklm__not_real" {
			found = true
		}
	}
	if !found {
		t.Errorf("an MCP tool the server does NOT expose should be flagged: %+v", missing)
	}
}
