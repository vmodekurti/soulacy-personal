package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestParseToolParams(t *testing.T) {
	got := parseToolParams("title*:string, summary:string, count:integer, tags:array")
	if len(got) != 4 {
		t.Fatalf("want 4 params, got %d: %+v", len(got), got)
	}
	if !got[0].Required || got[0].Name != "title" || got[0].Type != "string" {
		t.Errorf("param[0] = %+v, want required title:string", got[0])
	}
	if got[1].Required {
		t.Errorf("summary should be optional: %+v", got[1])
	}
	if got[2].Type != "integer" {
		t.Errorf("count type = %q, want integer", got[2].Type)
	}
}

func catWithTool(name, params string) Catalog {
	return Catalog{MCP: []CatalogMCPServer{{
		Server: "nb",
		Tools:  []CatalogMCPTool{{Name: name, Params: params}},
	}}}
}

func collect(draft Draft, cat Catalog) []PreflightIssue {
	var out []PreflightIssue
	checkToolArgs(draft, cat, func(sev, kind, node, msg, fix string) {
		out = append(out, PreflightIssue{Severity: sev, Kind: kind, NodeID: node, Message: msg, Fix: fix})
	})
	return out
}

func TestCheckToolArgs_UnknownArgument(t *testing.T) {
	cat := catWithTool("mcp__nb__create_notebook", "title*:string, summary:string")
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "make", Tool: "mcp__nb__create_notebook", Input: `{"name": "My Notebook"}`},
	}}}
	issues := collect(draft, cat)
	// "name" is not accepted; "title" (required) absence is Preflight's job, not here.
	var foundUnknown bool
	for _, is := range issues {
		if strings.Contains(is.Message, `"name" is not accepted`) {
			foundUnknown = true
		}
	}
	if !foundUnknown {
		t.Fatalf("expected unknown-argument issue for \"name\"; got %+v", issues)
	}
}

func TestCheckToolArgs_TypeMismatchAndTemplateSkipped(t *testing.T) {
	cat := catWithTool("mcp__nb__add", "notebook_id*:string, count:integer")
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		// notebook_id wired from upstream (templated → not type-checked);
		// count given a string literal → type mismatch.
		{ID: "add", Tool: "mcp__nb__add", Input: `{"notebook_id": "{{ .nb }}", "count": "five"}`},
	}}}
	issues := collect(draft, cat)
	var mismatch bool
	for _, is := range issues {
		if strings.Contains(is.Message, `"count"`) && strings.Contains(is.Message, "integer") {
			mismatch = true
		}
		if strings.Contains(is.Message, `"notebook_id"`) {
			t.Errorf("templated notebook_id should not be flagged: %+v", is)
		}
	}
	if !mismatch {
		t.Fatalf("expected count type-mismatch issue; got %+v", issues)
	}
}

func TestCheckToolArgs_CleanCallNoIssues(t *testing.T) {
	cat := catWithTool("mcp__nb__create_notebook", "title*:string, summary:string")
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "make", Tool: "mcp__nb__create_notebook", Input: `{"title": "X", "summary": "Y"}`},
	}}}
	if issues := collect(draft, cat); len(issues) != 0 {
		t.Fatalf("clean call should yield no issues, got %+v", issues)
	}
}

func TestCheckToolArgs_BuiltinSignatureUnknownArg(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{{
		ID:    "fetch",
		Kind:  "tool",
		Tool:  "fetch_url",
		Input: `{"query":"https://example.com"}`,
	}}}}
	var out []PreflightIssue
	checkToolArgs(d, Catalog{}, func(sev, kind, node, msg, fix string) {
		out = append(out, PreflightIssue{Severity: sev, Kind: kind, NodeID: node, Message: msg, Fix: fix})
	})
	if len(out) == 0 {
		t.Fatal("expected builtin signature warning")
	}
	if !strings.Contains(out[0].Message, `Argument "query"`) || !strings.Contains(out[0].Message, "fetch_url") {
		t.Fatalf("unexpected warning: %+v", out[0])
	}
}
