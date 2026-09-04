package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func nlmCatalog() Catalog {
	return Catalog{MCP: []CatalogMCPServer{{
		Server: "notebooklm",
		Tools: []CatalogMCPTool{
			{Name: "mcp__notebooklm__research_start", Params: "topic*:string"},
			{Name: "mcp__notebooklm__notebook_create", Params: "title*:string"},
			{Name: "mcp__notebooklm__noparams"}, // publishes no params -> never checked
		},
	}}}
}

// The live failure: research_start was called with num_results, which it doesn't
// accept. It must be flagged, naming the bad arg and the accepted ones.
func TestValidateToolArgs_FlagsUnknownArg(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "research", Kind: "tool", Tool: "mcp__notebooklm__research_start",
			Input: `{"topic": "AI news", "num_results": 10}`},
	}}}
	ws := ValidateToolArgs(d, nlmCatalog())
	if len(ws) != 1 {
		t.Fatalf("expected 1 warning, got %d: %+v", len(ws), ws)
	}
	if ws[0].NodeID != "research" || !strings.Contains(ws[0].Message, "num_results") {
		t.Errorf("warning should name the bad arg on the right node, got %+v", ws[0])
	}
	if !strings.Contains(ws[0].Message, "topic") {
		t.Errorf("warning should list accepted args, got %q", ws[0].Message)
	}
}

// A valid call (and a templated arg) produces no warning.
func TestValidateToolArgs_AcceptsValid(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "create", Kind: "tool", Tool: "mcp__notebooklm__notebook_create",
			Input: `{"title": "{{ .topic }}"}`},
	}}}
	if ws := ValidateToolArgs(d, nlmCatalog()); len(ws) != 0 {
		t.Errorf("a valid templated call should not warn, got %+v", ws)
	}
}

// Tools that publish no params, builtins not in the catalog, and empty catalogs
// are never flagged (no false positives where we can't see the schema).
func TestValidateToolArgs_NoSchemaNoWarn(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "a", Kind: "tool", Tool: "mcp__notebooklm__noparams", Input: `{"whatever": 1}`},
		{ID: "b", Kind: "tool", Tool: "web_search", Input: `{"query": "x", "num_results": 10}`},
	}}}
	if ws := ValidateToolArgs(d, nlmCatalog()); len(ws) != 0 {
		t.Errorf("tools with no published schema must not be flagged, got %+v", ws)
	}
	if ws := ValidateToolArgs(d, Catalog{}); ws != nil {
		t.Errorf("empty catalog should yield nil, got %+v", ws)
	}
}

// A REAL tool arg supplied via a declared typed input port is allowed (the port
// binds to a valid argument, so neither the template-key nor the port-binding
// check flags it). A port that binds to a NON-arg is a different case, covered by
// TestValidateToolArgs_FlagsBogusPortBinding.
func TestValidateToolArgs_AllowsPortArg(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "create", Kind: "tool", Tool: "mcp__notebooklm__notebook_create",
			Input:  `{"title": "x"}`,
			Inputs: []sdkr.FlowPort{{Name: "title"}},
		},
	}}}
	if ws := ValidateToolArgs(d, nlmCatalog()); len(ws) != 0 {
		t.Errorf("a port-supplied real arg should be allowed, got %+v", ws)
	}
}
