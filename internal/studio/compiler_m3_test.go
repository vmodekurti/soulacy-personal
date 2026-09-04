package studio

import (
	"context"
	"strings"
	"testing"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// TestCompile_MultiStepBranchGraph (Story M3, test a): a multi-step intent
// compiles to >=3 nodes including a branch node with 2 conditional out-edges,
// and the result passes reasoning.CompileFlow.
func TestCompile_MultiStepBranchGraph(t *testing.T) {
	canned := `{
  "name": "Triage Pipeline",
  "trigger": { "type": "schedule", "config": { "cron": "0 8 * * 1-5" } },
  "channels": ["telegram"],
  "flow": {
    "nodes": [
      { "id": "fetch", "kind": "tool", "tool": "http_get", "input": "{}", "output": "stories" },
      { "id": "triage", "kind": "branch" },
      { "id": "summarize", "kind": "agent", "agent": "summarizer", "input": "do it", "output": "summary" },
      { "id": "skip", "kind": "agent", "agent": "notifier", "input": "nothing", "output": "note" }
    ],
    "edges": [
      { "from": "fetch", "to": "triage" },
      { "from": "triage", "to": "summarize", "if": "{{ gt (len .stories) 0 }}" },
      { "from": "triage", "to": "skip" },
      { "from": "summarize", "to": "end" },
      { "from": "skip", "to": "end" }
    ],
    "entry": "fetch"
  }
}`
	res, err := Compile(context.Background(), fakeLLM{out: canned}, "fetch then branch", Catalog{}, nil)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	nodes := res.Workflow.Flow.Nodes
	if len(nodes) < 3 {
		t.Fatalf("expected >=3 nodes, got %d", len(nodes))
	}

	// Locate the branch node and confirm it has 2 conditional-capable out-edges.
	var branchID string
	for _, n := range nodes {
		if n.Kind == "branch" {
			branchID = n.ID
		}
	}
	if branchID == "" {
		t.Fatalf("expected a branch node in the compiled graph")
	}
	outEdges := 0
	conditional := 0
	for _, e := range res.Workflow.Flow.Edges {
		if e.From == branchID {
			outEdges++
			if strings.TrimSpace(e.If) != "" {
				conditional++
			}
		}
	}
	if outEdges < 2 {
		t.Fatalf("branch %q should fan out to >=2 edges, got %d", branchID, outEdges)
	}
	if conditional < 1 {
		t.Fatalf("branch %q should have >=1 conditional out-edge, got %d", branchID, conditional)
	}

	// Hard contract: the compiled graph passes CompileFlow.
	if _, err := reasoning.CompileFlow(res.Workflow.spec()); err != nil {
		t.Fatalf("compiled branch graph failed CompileFlow: %v", err)
	}
}

// TestCompile_PeerAgentHandoff (Story M3, test b): a kind=agent node
// referencing a catalog agent compiles, and multiple agent nodes are peers.
func TestCompile_PeerAgentHandoff(t *testing.T) {
	canned := `{
  "name": "Research Handoff",
  "trigger": { "type": "manual" },
  "channels": ["slack"],
  "flow": {
    "nodes": [
      { "id": "research", "kind": "agent", "agent": "researcher", "input": "find sources", "output": "sources" },
      { "id": "write", "kind": "agent", "agent": "writer", "input": "draft from {{.sources}}", "output": "draft" }
    ],
    "edges": [
      { "from": "research", "to": "write" },
      { "from": "write", "to": "end" }
    ],
    "entry": "research"
  }
}`
	cat := Catalog{Agents: []string{"researcher", "writer"}}
	res, err := Compile(context.Background(), fakeLLM{out: canned}, "research then write", cat, nil)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	peers := flowPeers(res.Workflow.Flow)
	if len(peers) != 2 {
		t.Fatalf("expected 2 peer agents, got %d (%v)", len(peers), peers)
	}
	if _, err := reasoning.CompileFlow(res.Workflow.spec()); err != nil {
		t.Fatalf("peer-agent graph failed CompileFlow: %v", err)
	}
}

// TestCompile_TypedPortsPass (Story M3, test c happy path): a graph using
// from_port/to_port with DECLARED node ports compiles.
func TestCompile_TypedPortsPass(t *testing.T) {
	canned := `{
  "name": "Ported Flow",
  "trigger": { "type": "manual" },
  "channels": ["email"],
  "flow": {
    "nodes": [
      { "id": "fetch", "kind": "tool", "tool": "http_get", "input": "{}", "output": "data",
        "outputs": [ { "name": "ok" } ] },
      { "id": "summarize", "kind": "agent", "agent": "summarizer", "input": "go",
        "inputs": [ { "name": "in" } ] }
    ],
    "edges": [
      { "from": "fetch", "to": "summarize", "from_port": "ok", "to_port": "in" },
      { "from": "summarize", "to": "end" }
    ],
    "entry": "fetch"
  }
}`
	res, err := Compile(context.Background(), fakeLLM{out: canned}, "ported", Catalog{}, nil)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	if _, err := reasoning.CompileFlow(res.Workflow.spec()); err != nil {
		t.Fatalf("typed-port graph failed CompileFlow: %v", err)
	}
}

// TestCompile_UndeclaredPortReconciled: an edge that names a port NOT declared
// on the referenced node no longer fails the compile. reconcilePorts
// auto-declares the referenced port on the node (preserving the model's
// intended named handle) so an otherwise-valid draft is not thrown away over a
// single cosmetic wiring slip — the historical failure mode that rejected real
// workflows. The reconciled draft must declare the port and compile cleanly.
func TestCompile_UndeclaredPortReconciled(t *testing.T) {
	canned := `{
  "name": "Bad Ports",
  "trigger": { "type": "manual" },
  "flow": {
    "nodes": [
      { "id": "fetch", "kind": "tool", "tool": "http_get", "input": "{}", "output": "data" },
      { "id": "summarize", "kind": "agent", "agent": "summarizer", "input": "go" }
    ],
    "edges": [
      { "from": "fetch", "to": "summarize", "from_port": "nope", "to_port": "in" },
      { "from": "summarize", "to": "end" }
    ],
    "entry": "fetch"
  }
}`
	res, err := Compile(context.Background(), fakeLLM{out: canned}, "bad ports", Catalog{}, nil)
	if err != nil {
		t.Fatalf("Compile should reconcile undeclared ports, got error: %v", err)
	}
	// The undeclared from_port "nope" must now be declared on "fetch" Outputs,
	// and the undeclared to_port "in" on "summarize" Inputs.
	var fetch, summarize *sdkr.FlowNode
	for i := range res.Workflow.Flow.Nodes {
		switch res.Workflow.Flow.Nodes[i].ID {
		case "fetch":
			fetch = &res.Workflow.Flow.Nodes[i]
		case "summarize":
			summarize = &res.Workflow.Flow.Nodes[i]
		}
	}
	if fetch == nil || !hasPortNamed(fetch.Outputs, "nope") {
		t.Fatalf("expected fetch to declare output port %q, got %+v", "nope", fetch)
	}
	if summarize == nil || !hasPortNamed(summarize.Inputs, "in") {
		t.Fatalf("expected summarize to declare input port %q, got %+v", "in", summarize)
	}
	if _, err := reasoning.CompileFlow(res.Workflow.spec()); err != nil {
		t.Fatalf("reconciled graph failed CompileFlow: %v", err)
	}
}

func hasPortNamed(ports []sdkr.FlowPort, name string) bool {
	for _, p := range ports {
		if p.Name == name {
			return true
		}
	}
	return false
}

// TestNormalizeFlow_FillsKinds confirms post-parse normalization makes implied
// kinds explicit (tool/agent/branch) without altering graph shape.
func TestNormalizeFlow_FillsKinds(t *testing.T) {
	dd := Draft{Flow: Flow{Entry: "a", Nodes: []sdkr.FlowNode{
		{ID: "a", Tool: "http_get"},    // -> tool
		{ID: "b", Agent: "summarizer"}, // -> agent
		{ID: "c"},                      // -> branch
	}}}
	normalizeFlow(&dd)
	want := []string{"tool", "agent", "branch"}
	for i, n := range dd.Flow.Nodes {
		if n.Kind != want[i] {
			t.Fatalf("node %d: kind = %q, want %q", i, n.Kind, want[i])
		}
	}
}
