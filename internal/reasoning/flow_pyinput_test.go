package reasoning

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// toJson must render a parsed flow var as valid JSON (not Go's map[...] syntax).
func TestRenderTemplate_ToJson(t *testing.T) {
	out, err := renderTemplate(`{{ toJson .x }}`, map[string]any{"x": map[string]any{"a": 1, "b": "hi"}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("toJson did not produce valid JSON: %q (%v)", out, err)
	}
	// Plain {{ .x }} would produce Go syntax — confirm we're not getting that.
	if strings.Contains(out, "map[") {
		t.Fatalf("got Go-formatted output, not JSON: %q", out)
	}
}

// A python node with no explicit Input must receive ALL flow vars as valid JSON
// on stdin (so run(inputs) sees upstream outputs without manual templating).
func TestRunFlow_PythonGetsVarsAsJSON(t *testing.T) {
	spec := sdkr.FlowSpec{
		Entry: "fetch",
		Nodes: []sdkr.FlowNode{
			{ID: "fetch", Kind: sdkr.FlowNodeTool, Tool: "t", Output: "articles"},
			{ID: "proc", Kind: sdkr.FlowNodePython, Code: "def run(i):\n    return i"},
		},
		Edges: []sdkr.FlowEdge{{From: "fetch", To: "proc"}, {From: "proc", To: "end"}},
	}
	g, err := CompileFlow(spec)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	var pyInput string
	run := func(ctx context.Context, node sdkr.FlowNode, rendered string) (json.RawMessage, error) {
		if node.ID == "fetch" {
			return json.RawMessage(`{"items":["a","b"]}`), nil
		}
		pyInput = rendered // capture what the python node receives
		return json.RawMessage(`{}`), nil
	}
	if _, err := RunFlow(context.Background(), g, map[string]any{}, run, FlowHooks{}); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(pyInput), &v); err != nil {
		t.Fatalf("python node got invalid JSON: %q (%v)", pyInput, err)
	}
	if _, ok := v["articles"]; !ok {
		t.Fatalf("python inputs missing upstream var 'articles': %q", pyInput)
	}
}
