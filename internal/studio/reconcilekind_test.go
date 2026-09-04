package studio

import (
	"testing"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// normalizeDraft mirrors what Compile does to a freshly-parsed whole draft.
func normalizeDraft(d *Draft) {
	normalizeFlow(d)
	reconcileNodeKinds(d)
}

// The exact failure a local model produced: a node declared kind=python that
// carries neither inline code nor a tool. CompileFlow rejects that outright
// ("is kind=python but has neither inline code nor a tool"), throwing away an
// otherwise-good workflow. It must be reconciled to an llm transform instead.
func TestReconcile_PythonWithoutCodeBecomesLLM(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "format_response", Kind: "python", Description: "Format the stock price output"},
	}}}
	normalizeDraft(&d)
	if got := d.Flow.Nodes[0].Kind; got != sdkr.FlowNodeLLM {
		t.Fatalf("kind=python with no code/tool should become llm, got %q", got)
	}
}

// A python node that DOES carry code must be left alone.
func TestReconcile_PythonWithCodeUntouched(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "calc", Kind: "python", Code: "def run(inputs):\n    return inputs"},
	}}}
	normalizeDraft(&d)
	if got := d.Flow.Nodes[0].Kind; got != sdkr.FlowNodePython {
		t.Fatalf("python node with code must stay python, got %q", got)
	}
}

// python + a deployed python tool (no inline code) is legal — leave it.
func TestReconcile_PythonWithToolUntouched(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "run_it", Kind: "python", Tool: "my_py_tool"},
	}}}
	normalizeDraft(&d)
	if got := d.Flow.Nodes[0].Kind; got != sdkr.FlowNodePython {
		t.Fatalf("python node with a tool must stay python, got %q", got)
	}
}

func TestReconcile_ContradictoryKinds(t *testing.T) {
	cases := []struct {
		name string
		node sdkr.FlowNode
		want string
	}{
		{"tool with code → python", sdkr.FlowNode{ID: "a", Kind: "tool", Code: "def run(i): return i"}, sdkr.FlowNodePython},
		{"tool with agent → agent", sdkr.FlowNode{ID: "b", Kind: "tool", Agent: "helper"}, sdkr.FlowNodeAgent},
		{"tool with nothing → llm", sdkr.FlowNode{ID: "c", Kind: "tool"}, sdkr.FlowNodeLLM},
		{"agent with no agent → llm", sdkr.FlowNode{ID: "d", Kind: "agent"}, sdkr.FlowNodeLLM},
		{"agent with tool → tool", sdkr.FlowNode{ID: "e", Kind: "agent", Tool: "web_search"}, sdkr.FlowNodeTool},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{tc.node}}}
			normalizeDraft(&d)
			if got := d.Flow.Nodes[0].Kind; got != tc.want {
				t.Errorf("got kind %q, want %q", got, tc.want)
			}
		})
	}
}

// Valid nodes must never be rewritten.
func TestReconcile_ValidKindsUntouched(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "t", Kind: "tool", Tool: "fetch_url"},
		{ID: "a", Kind: "agent", Agent: "researcher"},
		{ID: "b", Kind: "branch"},
		{ID: "l", Kind: "llm"},
	}}}
	normalizeDraft(&d)
	want := []string{sdkr.FlowNodeTool, sdkr.FlowNodeAgent, sdkr.FlowNodeBranch, sdkr.FlowNodeLLM}
	for i, w := range want {
		if got := d.Flow.Nodes[i].Kind; got != w {
			t.Errorf("node %d: got %q, want %q", i, got, w)
		}
	}
}

// End-to-end guard: the reconciled draft must actually satisfy the strict
// compiler that rejected it before.
func TestReconcile_ReconciledDraftPassesStrictCompile(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "fetch", Kind: "tool", Tool: "fetch_url"},
			{ID: "format_response", Kind: "python", Description: "Format the output"},
		},
		Edges: []sdkr.FlowEdge{{From: "fetch", To: "format_response"}},
		Entry: "fetch",
	}}
	// Before reconciliation this exact draft fails the strict compiler with
	// `node "format_response" is kind=python but has neither inline code nor a tool`.
	normalizeDraft(&d)
	if _, err := reasoning.CompileFlow(d.spec()); err != nil {
		t.Fatalf("reconciled draft must compile, got: %v", err)
	}
}
