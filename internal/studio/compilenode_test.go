package studio

import (
	"context"
	"strings"
	"testing"
)

type fakeNodeLLM struct{ out string }

func (f fakeNodeLLM) Complete(_ context.Context, _ string) (string, error) { return f.out, nil }

// A tool step compiles into a tool node, keeps the requested id, and remembers
// its intent (parity).
func TestCompileNode_ToolStep(t *testing.T) {
	llm := fakeNodeLLM{out: `{"flow":{"nodes":[{"id":"x","kind":"tool","tool":"web_search","input":"{\"query\":\"AI news\"}","output":"results"}]}}`}
	node, err := CompileNode(context.Background(), llm, CompileNodeRequest{
		Intent: "search the web for AI news",
		NodeID: "search_step",
	})
	if err != nil {
		t.Fatalf("compile tool node: %v", err)
	}
	if node.Kind != "tool" || node.Tool != "web_search" {
		t.Errorf("expected web_search tool node, got kind=%q tool=%q", node.Kind, node.Tool)
	}
	if node.ID != "search_step" {
		t.Errorf("requested node id should be kept, got %q", node.ID)
	}
	if node.Intent != "search the web for AI news" {
		t.Errorf("node should remember its intent, got %q", node.Intent)
	}
}

// The per-node compiler inherits ParseDraft's object-input coercion: a model that
// emits "input" as an object still yields a valid node.
func TestCompileNode_CoercesObjectInput(t *testing.T) {
	llm := fakeNodeLLM{out: "```json\n{\"flow\":{\"nodes\":[{\"id\":\"s\",\"kind\":\"tool\",\"tool\":\"web_search\",\"input\":{\"query\":\"x\",\"n\":5},\"output\":\"r\"}]}}\n```"}
	node, err := CompileNode(context.Background(), llm, CompileNodeRequest{Intent: "search"})
	if err != nil {
		t.Fatalf("object input should be coerced: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(node.Input), "{") {
		t.Errorf("input should be stringified JSON, got %q", node.Input)
	}
}

// A python step compiles into a python node with capabilities classified.
func TestCompileNode_PythonStepClassified(t *testing.T) {
	code := "def run(inputs):\\n    import subprocess\\n    return subprocess.run(['echo','hi']).returncode"
	llm := fakeNodeLLM{out: `{"flow":{"nodes":[{"id":"p","kind":"python","code":"` + code + `","output":"o"}]}}`}
	node, err := CompileNode(context.Background(), llm, CompileNodeRequest{Intent: "run echo", Kind: "python"})
	if err != nil {
		t.Fatalf("compile python node: %v", err)
	}
	if node.Kind != "python" {
		t.Fatalf("expected python node, got %q", node.Kind)
	}
	found := false
	for _, r := range node.Requires {
		if r == "system" {
			found = true
		}
	}
	if !found {
		t.Errorf("a subprocess python node should require system, got %v", node.Requires)
	}
}

func TestCompileNode_EmptyIntent(t *testing.T) {
	if _, err := CompileNode(context.Background(), fakeNodeLLM{}, CompileNodeRequest{}); err == nil {
		t.Error("empty intent should error")
	}
}

// A model that returns a node missing required fields (tool kind, no tool) is
// rejected by the single-node compile check.
func TestCompileNode_InvalidNodeRejected(t *testing.T) {
	llm := fakeNodeLLM{out: `{"flow":{"nodes":[{"id":"x","kind":"tool","output":"r"}]}}`}
	if _, err := CompileNode(context.Background(), llm, CompileNodeRequest{Intent: "do a thing"}); err == nil {
		t.Error("a tool node with no tool should be rejected")
	}
}

// The prompt grounds the model in upstream shapes and the catalog.
func TestBuildNodePrompt_GroundsUpstreamAndCatalog(t *testing.T) {
	p := BuildNodePrompt(CompileNodeRequest{
		Intent:   "add the notebook id",
		Upstream: []UpstreamVar{{Name: "notebook", Shape: `{"id":"nb-1"}`}},
		Catalog:  Catalog{Tools: []string{"web_search"}},
	})
	if !strings.Contains(p, "notebook") || !strings.Contains(p, "nb-1") {
		t.Error("prompt should include the upstream var name and shape")
	}
	if !strings.Contains(p, "web_search") {
		t.Error("prompt should include available tools")
	}
}
