package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// collect runs checkTemplateReferences and returns the warnings it produced.
func collectTemplateRefs(draft Draft) []PreflightIssue {
	var issues []PreflightIssue
	add := func(sev, kind, node, msg, fix string) {
		issues = append(issues, PreflightIssue{Severity: sev, Kind: kind, NodeID: node, Message: msg, Fix: fix})
	}
	checkTemplateReferences(draft, add)
	return issues
}

func toolNode(id, output, tool, input string) sdkr.FlowNode {
	return sdkr.FlowNode{ID: id, Kind: "tool", Tool: tool, Output: output, Input: input}
}

// SuggestTemplateFixes turns the {{ .notebook.notebook }} bug into a concrete
// find/replace the GUI's one-click Fix applies.
func TestSuggestTemplateFixes_RepeatedNestedPath(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		toolNode("create", "notebook", "mcp__notebooklm__create", `{"title":"AI news"}`),
		toolNode("add", "", "mcp__notebooklm__add_sources", `{"notebook_id":"{{ .notebook.notebook }}"}`),
	}}}
	fixes := SuggestTemplateFixes(draft)
	if len(fixes) != 1 {
		t.Fatalf("want 1 fix, got %d: %+v", len(fixes), fixes)
	}
	if fixes[0].Find != "{{ .notebook.notebook }}" || fixes[0].Replace != "{{ .notebook.notebook.id }}" {
		t.Errorf("unexpected fix: %+v", fixes[0])
	}
}

// ApplyTemplateFixes rewrites the clear bugs in place (the generation self-heal)
// and is idempotent.
func TestApplyTemplateFixes_HealsAndIsIdempotent(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		toolNode("create", "notebook", "mcp__notebooklm__create", `{"title":"x"}`),
		toolNode("add", "", "mcp__notebooklm__add_sources", `{"notebook_id":"{{ .notebook }}"}`),
		toolNode("status", "", "mcp__notebooklm__studio_status", `{"notebook_id":"{{ .notebook.notebook }}"}`),
	}}}
	if n := ApplyTemplateFixes(&draft); n != 2 {
		t.Fatalf("want 2 nodes changed, got %d", n)
	}
	if got := draft.Flow.Nodes[1].Input; got != `{"notebook_id":"{{ .notebook.id }}"}` {
		t.Errorf("add not healed: %s", got)
	}
	if got := draft.Flow.Nodes[2].Input; got != `{"notebook_id":"{{ .notebook.notebook.id }}"}` {
		t.Errorf("status not healed: %s", got)
	}
	if n := ApplyTemplateFixes(&draft); n != 0 {
		t.Errorf("expected idempotent (0 changes), got %d", n)
	}
}

func TestApplyTemplateFixes_HealsBrokenEntryCapture(t *testing.T) {
	draft := Draft{Flow: Flow{
		Entry: "get_message",
		Nodes: []sdkr.FlowNode{{
			ID:          "get_message",
			Kind:        sdkr.FlowNodePython,
			Description: "Capture the initial user message",
			Code:        "def run(inputs):\n    return inputs.get('final_msg', '')",
			Input:       "{}",
			Output:      "initial_user_message",
		}},
	}}
	if n := ApplyTemplateFixes(&draft); n != 1 {
		t.Fatalf("want 1 node changed, got %d", n)
	}
	node := draft.Flow.Nodes[0]
	if !strings.Contains(node.Code, "trigger_text") || !strings.Contains(node.Input, ".trigger.text") {
		t.Fatalf("entry capture not healed: code=%q input=%q", node.Code, node.Input)
	}
	if n := ApplyTemplateFixes(&draft); n != 0 {
		t.Errorf("expected idempotent (0 changes), got %d", n)
	}
}

func TestApplyTemplateFixes_MapsPythonUpstreamInputsAsJSON(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{
			ID:     "store_in_kb",
			Kind:   sdkr.FlowNodePython,
			Output: "storage_results",
			Code:   "def run(inputs):\n    return [{'status': 'stored'}]",
		},
		{
			ID:   "send_confirmation",
			Kind: sdkr.FlowNodePython,
			Code: "def run(inputs):\n    results = inputs.get('storage_results') or []\n    return str(results)",
			Input: `Create a confirmation.

Storage Results:
{{ toJson .storage_results }}`,
		},
	}}}
	if n := ApplyTemplateFixes(&draft); n != 1 {
		t.Fatalf("want 1 node changed, got %d", n)
	}
	want := `{"storage_results": {{ toJson .storage_results }}}`
	if got := draft.Flow.Nodes[1].Input; got != want {
		t.Fatalf("python input not mapped as JSON:\nwant %s\ngot  %s", want, got)
	}
	if n := ApplyTemplateFixes(&draft); n != 0 {
		t.Errorf("expected idempotent (0 changes), got %d", n)
	}
}

// A path that already reaches a field (.notebook.notebook.id) must NOT be
// flagged or fixed again — this is what prevents the .id.id.id compounding.
func TestSuggestTemplateFixes_DoesNotCompound(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		toolNode("create", "notebook", "mcp__notebooklm__create", `{"title":"AI news"}`),
		toolNode("add", "", "mcp__notebooklm__add_sources", `{"notebook_id":"{{ .notebook.notebook.id }}"}`),
	}}}
	if fixes := SuggestTemplateFixes(draft); len(fixes) != 0 {
		t.Errorf("an already-fixed .id path must not be fixed again, got: %+v", fixes)
	}
	if issues := collectTemplateRefs(draft); len(issues) != 0 {
		t.Errorf("an already-fixed .id path must not warn, got: %+v", issues)
	}
}

// A python step that outputs a plain string (e.g. a date) must NOT be flagged or
// generate a fix when interpolated bare — the false-positive case to suppress.
func TestSuggestTemplateFixes_IgnoresBarePythonScalar(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "get_date", Kind: "python", Output: "current_date", Code: "def run(inputs):\n  return '2026-06-22'"},
		toolNode("create", "notebook", "mcp__x__create", `{"date":"{{ .current_date }}"}`),
	}}}
	if fixes := SuggestTemplateFixes(draft); len(fixes) != 0 {
		t.Errorf("a bare python scalar should not be fixed, got: %+v", fixes)
	}
	if issues := collectTemplateRefs(draft); len(issues) != 0 {
		t.Errorf("a bare python scalar should not warn, got: %+v", issues)
	}
}

// The reported bug: a wrong nested path {{ .notebook.notebook }} that resolves
// to a map instead of the id string.
func TestCheckTemplateRefs_FlagsRepeatedNestedPath(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		toolNode("create", "notebook", "mcp__notebooklm__create", `{"title":"AI news"}`),
		toolNode("add", "", "mcp__notebooklm__add_sources", `{"notebook_id":"{{ .notebook.notebook }}"}`),
	}}}
	issues := collectTemplateRefs(draft)
	if len(issues) == 0 {
		t.Fatal("expected a warning for {{ .notebook.notebook }}")
	}
	got := issues[0]
	if got.Severity != "warn" || got.NodeID != "add" {
		t.Errorf("unexpected issue: %+v", got)
	}
	if !strings.Contains(got.Message, "nested object") {
		t.Errorf("message should explain the nested-object problem: %q", got.Message)
	}
	if !strings.Contains(got.Fix, ".notebook.id") {
		t.Errorf("fix should suggest a scalar field: %q", got.Fix)
	}
}

// A bare whole-object interpolation of a structured (MCP) output.
func TestCheckTemplateRefs_FlagsWholeObjectInterpolation(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		toolNode("create", "notebook", "mcp__notebooklm__create", `{"title":"AI news"}`),
		toolNode("add", "", "mcp__notebooklm__add_sources", `{"notebook_id":"{{ .notebook }}"}`),
	}}}
	issues := collectTemplateRefs(draft)
	if len(issues) != 1 {
		t.Fatalf("expected exactly 1 warning, got %d: %+v", len(issues), issues)
	}
	if !strings.Contains(issues[0].Message, "whole") {
		t.Errorf("message should flag the whole-object pass: %q", issues[0].Message)
	}
}

// toJson legitimately serializes the whole object — must NOT warn.
func TestCheckTemplateRefs_AllowsToJson(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		toolNode("create", "notebook", "mcp__notebooklm__create", `{"title":"AI news"}`),
		toolNode("dump", "", "mcp__x__y", `{"payload":"{{ toJson .notebook }}"}`),
	}}}
	if issues := collectTemplateRefs(draft); len(issues) != 0 {
		t.Errorf("toJson should be allowed, got: %+v", issues)
	}
}

// A correct scalar field reference must NOT warn.
func TestCheckTemplateRefs_AllowsScalarField(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		toolNode("create", "notebook", "mcp__notebooklm__create", `{"title":"AI news"}`),
		toolNode("add", "", "mcp__notebooklm__add_sources", `{"notebook_id":"{{ .notebook.id }}"}`),
	}}}
	if issues := collectTemplateRefs(draft); len(issues) != 0 {
		t.Errorf("a scalar field ref should be clean, got: %+v", issues)
	}
}

// An agent step returns text (scalar), so a bare interpolation is fine.
func TestCheckTemplateRefs_AgentOutputIsScalar(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "writer", Kind: "agent", Agent: "summarizer", Output: "summary"},
		toolNode("post", "", "mcp__telegram__send", `{"text":"{{ .summary }}"}`),
	}}}
	if issues := collectTemplateRefs(draft); len(issues) != 0 {
		t.Errorf("agent text output should not warn, got: %+v", issues)
	}
}

// If a var is accessed with a field elsewhere, that proves it's an object, so a
// bare interpolation of it (even from a non-MCP producer) should warn.
func TestCheckTemplateRefs_InfersObjectFromFieldAccess(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "py", Kind: "python", Output: "data", Code: "def run(inputs):\n  return {}"},
		toolNode("a", "", "builtin_x", `{"v":"{{ .data.id }}"}`),
		toolNode("b", "", "builtin_y", `{"v":"{{ .data }}"}`),
	}}}
	issues := collectTemplateRefs(draft)
	var flaggedB bool
	for _, is := range issues {
		if is.NodeID == "b" {
			flaggedB = true
		}
	}
	if !flaggedB {
		t.Errorf("bare {{ .data }} should warn once .data.id proves it's an object; got: %+v", issues)
	}
}
