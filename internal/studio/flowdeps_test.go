package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// The runtime seeds `trigger` (and `history`) for every flow, so an entry step
// reading the incoming message via {{ .trigger.text }} must NOT be flagged as a
// missing dependency. Regression for the false "no earlier step produces
// trigger" blocker that stopped weather-style workflows from saving.
func TestCheckDataFlow_AllowsBuiltinTrigger(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "input_msg", Kind: "python", Input: `{"message": "{{ .trigger.text }}"}`, Output: "message"},
			{ID: "resolve", Kind: "tool", Tool: "x", Input: `{"query": "{{ .message }}"}`, Output: "location"},
			{ID: "hist", Kind: "python", Input: `{"h": "{{ toJson .history }}"}`, Output: "h"},
		},
		Edges: []sdkr.FlowEdge{{From: "input_msg", To: "resolve"}, {From: "resolve", To: "hist"}},
		Entry: "input_msg",
	}}
	var blockers []string
	checkDataFlow(d, func(sev, kind, node, msg, fix string) {
		if sev == "block" {
			blockers = append(blockers, msg)
		}
	})
	if len(blockers) != 0 {
		t.Fatalf("expected no blockers for builtin trigger/history, got: %v", blockers)
	}
}

// A genuinely undefined variable must still be blocked.
func TestCheckDataFlow_StillBlocksUnknownVar(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Kind: "tool", Tool: "x", Input: `{"q": "{{ .nope }}"}`, Output: "out"},
		},
		Entry: "a",
	}}
	blockers := 0
	checkDataFlow(d, func(sev, kind, node, msg, fix string) {
		if sev == "block" {
			blockers++
		}
	})
	if blockers == 0 {
		t.Fatal("expected a blocker for the undefined var .nope")
	}
}

func TestCheckDataFlow_AllowsMappedNodeLocals(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "make", Kind: "tool", Tool: "x", Output: "items"},
			{
				ID:          "map",
				Kind:        "tool",
				Tool:        "y",
				ForEach:     `{{ toJson .items }}`,
				ItemVar:     "article",
				MaxParallel: 3,
				Input:       `{"text":{{ toJson .article.text }},"position":{{ .article_index }}}`,
				Output:      "mapped",
			},
		},
		Edges: []sdkr.FlowEdge{{From: "make", To: "map"}},
		Entry: "make",
	}}
	var blockers []string
	checkDataFlow(d, func(sev, kind, node, msg, fix string) {
		if sev == "block" {
			blockers = append(blockers, msg)
		}
	})
	if len(blockers) != 0 {
		t.Fatalf("expected mapped item locals to be satisfied, got: %v", blockers)
	}
}

func TestCheckDataFlow_BlocksMissingMappedCollection(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{
				ID:      "map",
				Kind:    "tool",
				Tool:    "y",
				ForEach: `{{ toJson .missing_items }}`,
				ItemVar: "article",
				Input:   `{"text":{{ toJson .article.text }}}`,
				Output:  "mapped",
			},
		},
		Entry: "map",
	}}
	var blockers []string
	checkDataFlow(d, func(sev, kind, node, msg, fix string) {
		if sev == "block" {
			blockers = append(blockers, msg)
		}
	})
	if len(blockers) != 1 || !strings.Contains(blockers[0], "missing_items") {
		t.Fatalf("expected missing mapped collection blocker, got: %v", blockers)
	}
}

func TestCheckDataFlow_BlocksMissingPythonInput(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "clean", Kind: "python", Code: `def run(inputs):
    rows = inputs.get("rows", [])
    return rows`, Output: "clean_rows"},
		},
		Entry: "clean",
	}}
	var blockers []string
	checkDataFlow(d, func(sev, kind, node, msg, fix string) {
		if sev == "block" {
			blockers = append(blockers, msg)
		}
	})
	if len(blockers) != 1 || !strings.Contains(blockers[0], `inputs["rows"]`) {
		t.Fatalf("expected missing python input blocker, got: %v", blockers)
	}
}

func TestCheckDataFlow_AllowsEntryPythonInboundAliasProbes(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "input_msg", Kind: "python", Code: `def run(inputs):
    trigger = inputs.get("trigger") or {}
    msg = inputs.get("message") or inputs.get("text") or inputs.get("input")
    if not msg and isinstance(trigger, dict):
        msg = trigger.get("text", "")
    return msg`, Output: "message"},
			{ID: "resolve", Kind: "tool", Tool: "x", Input: `{"query":"{{ .message }}"}`, Output: "location"},
		},
		Edges: []sdkr.FlowEdge{{From: "input_msg", To: "resolve"}},
		Entry: "input_msg",
	}}
	var blockers []string
	checkDataFlow(d, func(sev, kind, node, msg, fix string) {
		if sev == "block" {
			blockers = append(blockers, msg)
		}
	})
	if len(blockers) != 0 {
		t.Fatalf("expected no blockers for entry inbound alias probes, got: %v", blockers)
	}
}

func TestCheckDataFlow_BlocksStrictEntryPythonInboundAliasRead(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "input_msg", Kind: "python", Code: `def run(inputs):
    return inputs["message"]`, Output: "message"},
		},
		Entry: "input_msg",
	}}
	var blockers []string
	checkDataFlow(d, func(sev, kind, node, msg, fix string) {
		if sev == "block" {
			blockers = append(blockers, msg)
		}
	})
	if len(blockers) != 1 || !strings.Contains(blockers[0], `inputs["message"]`) {
		t.Fatalf("expected strict entry alias read to remain blocked, got: %v", blockers)
	}
}

func TestCheckDataFlow_AllowsPythonInputFromAncestorOutput(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "fetch", Kind: "tool", Tool: "x", Output: "rows"},
			{ID: "clean", Kind: "python", Code: `def run(inputs):
    return inputs["rows"]`, Output: "clean_rows"},
		},
		Edges: []sdkr.FlowEdge{{From: "fetch", To: "clean"}},
		Entry: "fetch",
	}}
	var blockers []string
	checkDataFlow(d, func(sev, kind, node, msg, fix string) {
		if sev == "block" {
			blockers = append(blockers, msg)
		}
	})
	if len(blockers) != 0 {
		t.Fatalf("expected ancestor output to satisfy python input, got: %v", blockers)
	}
}

func TestCheckDataFlow_BlocksEdgeConditionMissingVar(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Kind: "tool", Tool: "x", Output: "out"},
			{ID: "b", Kind: "tool", Tool: "y"},
		},
		Edges: []sdkr.FlowEdge{{From: "a", To: "b", If: `{{ .missing_flag }}`}},
		Entry: "a",
	}}
	var blockers []string
	checkDataFlow(d, func(sev, kind, node, msg, fix string) {
		if sev == "block" {
			blockers = append(blockers, node+": "+msg)
		}
	})
	if len(blockers) != 1 || !strings.Contains(blockers[0], "missing_flag") {
		t.Fatalf("expected missing edge predicate var blocker, got: %v", blockers)
	}
}

func TestCheckDataFlow_BlocksTypedPortFromNodeWithoutOutput(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "make", Kind: "tool", Tool: "x", Outputs: []sdkr.FlowPort{{Name: "id"}}},
			{ID: "use", Kind: "tool", Tool: "y", Inputs: []sdkr.FlowPort{{Name: "item_id"}}},
		},
		Edges: []sdkr.FlowEdge{{From: "make", To: "use", FromPort: "id", ToPort: "item_id"}},
		Entry: "make",
	}}
	var blockers []string
	checkDataFlow(d, func(sev, kind, node, msg, fix string) {
		if sev == "block" {
			blockers = append(blockers, msg)
		}
	})
	if len(blockers) != 1 || !strings.Contains(blockers[0], "has no output variable") {
		t.Fatalf("expected no-output typed port blocker, got: %v", blockers)
	}
}

func TestCheckOutputContracts_BlocksDuplicateInvalidAndReservedVars(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "search", Kind: "tool", Tool: "x", Output: "result"},
			{ID: "summarize", Kind: "agent", Agent: "a", Output: "result"},
			{ID: "tag", Kind: "python", Code: "def run(inputs):\n  return {}", Output: "bad-name!"},
			{ID: "capture", Kind: "tool", Tool: "x", Output: "trigger"},
			{ID: "branch", Kind: "branch", Output: "branch_result"},
		},
		Entry: "search",
	}}
	var blockers, warnings []string
	checkOutputContracts(d, func(sev, kind, node, msg, fix string) {
		if sev == "block" {
			blockers = append(blockers, node+": "+msg)
		} else {
			warnings = append(warnings, node+": "+msg)
		}
	})
	joined := strings.Join(blockers, "\n")
	for _, want := range []string{"produced by both", "not a valid workflow identifier", "reserved by the workflow runtime"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected blocker containing %q, got %v", want, blockers)
		}
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "Structural node") {
		t.Fatalf("expected one structural-node warning, got %v", warnings)
	}
}
