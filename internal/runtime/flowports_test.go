package runtime

import (
	"encoding/json"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestFlowPortTypeMismatch(t *testing.T) {
	node := func(ports ...sdkr.FlowPort) sdkr.FlowNode {
		return sdkr.FlowNode{ID: "n", Tool: "t", Outputs: ports}
	}
	cases := []struct {
		name     string
		node     sdkr.FlowNode
		out      string
		mismatch bool
	}{
		{"no declared ports never mismatch", sdkr.FlowNode{ID: "n"}, `{"a":1}`, false},
		{"untyped port never mismatches", node(sdkr.FlowPort{Name: "r"}), `{"r":1}`, false},
		{"json/any hints are unchecked", node(sdkr.FlowPort{Name: "r", Type: "json"}), `"whatever"`, false},
		{"unknown type spellings are unchecked", node(sdkr.FlowPort{Name: "r", Type: "NotebookRef"}), `"x"`, false},
		{"named field matches", node(sdkr.FlowPort{Name: "url", Type: "string"}), `{"url":"https://x"}`, false},
		{"named field wrong type", node(sdkr.FlowPort{Name: "url", Type: "string"}), `{"url":42}`, true},
		{"generic port name carries whole output", node(sdkr.FlowPort{Name: "result", Type: "object"}), `{"ok":true}`, false},
		{"whole output wrong type", node(sdkr.FlowPort{Name: "result", Type: "array"}), `{"ok":true}`, true},
		{"dotted field path matches", node(sdkr.FlowPort{Name: "id", Type: "string", Field: "notebook.id"}), `{"notebook":{"id":"abc"}}`, false},
		{"dotted field absent is drift", node(sdkr.FlowPort{Name: "id", Type: "string", Field: "notebook.id"}), `{"nb":{"id":"abc"}}`, true},
		{"array spellings normalize", node(sdkr.FlowPort{Name: "urls", Type: "string[]"}), `{"urls":["a","b"]}`, false},
		{"number for int hint", node(sdkr.FlowPort{Name: "count", Type: "int"}), `{"count":3}`, false},
		{"string number is still drift", node(sdkr.FlowPort{Name: "count", Type: "int"}), `{"count":"3"}`, true},
		{"non-JSON output is unchecked", node(sdkr.FlowPort{Name: "r", Type: "string"}), `not json`, false},
	}
	for _, tc := range cases {
		got := flowPortTypeMismatch(tc.node, json.RawMessage(tc.out))
		if (got != "") != tc.mismatch {
			t.Errorf("%s: mismatch=%q, want mismatch=%v", tc.name, got, tc.mismatch)
		}
	}
}

func TestIsEffectfulFlowNode(t *testing.T) {
	effectful := []sdkr.FlowNode{
		{Kind: sdkr.FlowNodeAgent, Agent: "editor"},
		{Kind: sdkr.FlowNodeTool, Tool: "channel.send"},
		{Kind: sdkr.FlowNodeTool, Tool: "kb.delete"},
		{Kind: sdkr.FlowNodeTool, Tool: "queue.push"},
		{Kind: sdkr.FlowNodeTool, Tool: "file.write"},
		{Kind: sdkr.FlowNodePython, Code: "import subprocess", Requires: []string{"system"}},
		{Kind: sdkr.FlowNodePython, Code: "import requests", Requires: []string{"network"}},
		{Kind: sdkr.FlowNodePython, Tool: "notebooklm.create_notebook"}, // deployed tool by name
	}
	for _, n := range effectful {
		if !isEffectfulFlowNode(n) {
			t.Errorf("expected effectful: %+v", n)
		}
	}
	pure := []sdkr.FlowNode{
		{Kind: sdkr.FlowNodeLLM},
		{Kind: sdkr.FlowNodeTool, Tool: "kb.search"},
		{Kind: sdkr.FlowNodeTool, Tool: "http.get"},
		{Kind: sdkr.FlowNodePython, Code: "import json\ndef run(i): return i"}, // ReadOnly inline code
		{Kind: sdkr.FlowNodeBranch},
	}
	for _, n := range pure {
		if isEffectfulFlowNode(n) {
			t.Errorf("expected NOT effectful: %+v", n)
		}
	}
}

func TestParseFlowPredicateVerdict(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"take": true}`:                        true,
		`{"take": false}`:                       false,
		"```json\n{\"take\": true}\n```":        true,
		`The answer is {"take": false} because`: false,
	} {
		got, ok := parseFlowPredicateVerdict(raw)
		if !ok || got != want {
			t.Errorf("%q: got (%v,%v), want (%v,true)", raw, got, ok, want)
		}
	}
	// A hedging or malformed reply must never steer routing.
	for _, raw := range []string{"", "yes", `{"take": "true"}`, `{"decision": true}`, `{}`} {
		if _, ok := parseFlowPredicateVerdict(raw); ok {
			t.Errorf("%q: expected invalid verdict", raw)
		}
	}
}
