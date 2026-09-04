package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// The recurring failure: a python step shells out to the `nlm` CLI while the
// workflow already drives the notebooklm MCP. With an MCP available this is a
// BLOCKER pointing at the right MCP.
func TestShellSmell_BlocksCliWhenMcpPresent(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "save_auth", Kind: "tool", Tool: "mcp__notebooklm__save_auth"},
		{ID: "make_podcast", Kind: "python", Code: "def run(inputs):\n    import subprocess\n    subprocess.run(['nlm', 'notebook', 'create', '--title', 't'])\n    return {}"},
	}}}
	blocks, warns := shellSmellIssues(d)
	if len(blocks) != 1 || len(warns) != 0 {
		t.Fatalf("expected 1 blocker, 0 warnings, got %d/%d", len(blocks), len(warns))
	}
	if blocks[0].NodeID != "make_podcast" {
		t.Errorf("blocker should point at make_podcast, got %q", blocks[0].NodeID)
	}
	if !strings.Contains(blocks[0].Message, "nlm") || !strings.Contains(blocks[0].Message, "notebooklm") {
		t.Errorf("blocker should name the binary and the connected MCP, got %q", blocks[0].Message)
	}
}

// Without any MCP in the workflow it's a WARNING, not a blocker.
func TestShellSmell_WarnsWhenNoMcp(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "p", Kind: "python", Code: `def run(inputs):` + "\n" + `    import os; os.system("ffmpeg -i a b")` + "\n" + `    return {}`},
	}}}
	blocks, warns := shellSmellIssues(d)
	if len(blocks) != 0 || len(warns) != 1 || warns[0].NodeID != "p" {
		t.Fatalf("expected 0 blockers, 1 warning on p, got %d/%d", len(blocks), len(warns))
	}
}

// A pure-data python node (no shell-out) is not flagged.
func TestShellSmell_IgnoresPureData(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "clean", Kind: "python", Code: "def run(inputs):\n    return [x for x in inputs.get('items', [])]"},
	}}}
	if b, w := shellSmellIssues(d); len(b) != 0 || len(w) != 0 {
		t.Errorf("pure-data python should not be flagged, got %d/%d", len(b), len(w))
	}
}

// Wired into Validate: a CLI-shelling python step with an MCP present fails the graph.
func TestValidate_BlocksShellWithMcp(t *testing.T) {
	d := Draft{
		Name:    "x",
		Trigger: Trigger{Type: "manual"},
		Flow: Flow{
			Nodes: []sdkr.FlowNode{
				{ID: "t", Kind: "tool", Tool: "mcp__notebooklm__create"},
				{ID: "a", Kind: "python", Output: "o", Code: "def run(inputs):\n    import subprocess\n    subprocess.run(['nlm','x'])\n    return {}"},
			},
			Edges: []sdkr.FlowEdge{{From: "t", To: "a"}, {From: "a", To: "end"}},
			Entry: "t",
		},
	}
	res := Validate(d)
	if res.Ok {
		t.Fatal("a CLI-shelling python step with an MCP present should block the graph")
	}
	found := false
	for _, e := range res.Errors {
		if e.NodeID == "a" && strings.Contains(e.Message, "MCP") {
			found = true
		}
	}
	if !found {
		t.Errorf("Validate should surface the shell-smell blocker, got %+v", res.Errors)
	}
}
