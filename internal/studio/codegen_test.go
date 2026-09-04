package studio

import (
	"context"
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestGenerateNodeCode(t *testing.T) {
	// The framework model returns fenced code; we must strip the fences.
	fake := fakeLLM{out: "```python\ndef run(inputs):\n    return inputs.get('articles')\n```"}
	req := CodegenRequest{
		NodeID:      "extract",
		Description: "Pull the URLs out of the articles",
		Workflow: Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "search", Kind: sdkr.FlowNodeTool, Tool: "web_search", Output: "articles", Description: "search the web"},
			{ID: "extract", Kind: sdkr.FlowNodePython},
		}}},
	}
	code, err := GenerateNodeCode(context.Background(), fake, req)
	if err != nil {
		t.Fatalf("GenerateNodeCode: %v", err)
	}
	if strings.Contains(code, "```") {
		t.Fatalf("fences not stripped: %q", code)
	}
	if !strings.Contains(code, "def run(inputs):") {
		t.Fatalf("missing run(): %q", code)
	}

	// The prompt must ground the model in available upstream inputs.
	p := buildCodegenPrompt(req)
	if !strings.Contains(p, `inputs["articles"]`) {
		t.Fatalf("prompt should list the upstream 'articles' input:\n%s", p)
	}
	if !strings.Contains(p, "def run(inputs):") {
		t.Fatal("prompt must pin the run(inputs) contract")
	}

	// No model configured → clear error.
	if _, err := GenerateNodeCode(context.Background(), nil, req); err == nil {
		t.Fatal("nil llm should error")
	}
}
