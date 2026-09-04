package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// A saved Studio agent must carry a well-defined system prompt (not the old
// placeholder): the trigger, the ordered steps, the output channel, and — when
// host execution is involved — a scope-of-action line.
func TestToAgentDefinition_WellDefinedPrompt(t *testing.T) {
	draft := Draft{
		Name:         "Daily Digest",
		SystemPrompt: "You are the mighty Daily Digest bot.",
		Trigger:      Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		Channels:     []string{"slack"},
		Flow: Flow{
			Entry: "curate",
			Nodes: []sdkr.FlowNode{
				{ID: "curate", Kind: sdkr.FlowNodeAgent, Agent: "curator", Output: "urls"},
				{ID: "pipeline", Kind: sdkr.FlowNodePython, Code: "import subprocess\n", Output: "audio"},
				{ID: "search", Kind: sdkr.FlowNodeTool, Tool: "mcp__github__search"},
				{ID: "notify", Kind: sdkr.FlowNodeTool, Tool: "channel.send"},
			},
			Edges: []sdkr.FlowEdge{
				{From: "curate", To: "pipeline"},
				{From: "pipeline", To: "search"},
				{From: "search", To: "notify"},
				{From: "notify", To: "end"},
			},
		},
	}
	def, err := ToAgentDefinition(draft, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	p := def.SystemPrompt
	if !strings.HasPrefix(p, "## Soulacy Agent Operating Contract\n\n") {
		t.Fatalf("prompt did not start with shared contract:\n%s", p)
	}
	if !strings.Contains(p, "## Agent Role\n\nYou are the mighty Daily Digest bot.\n\n") {
		t.Fatalf("prompt did not preserve draft.SystemPrompt under Agent Role:\n%s", p)
	}
	for _, want := range []string{"cron", "0 7 * * *", "curator", "Python", "channel.send", "slack", "Steps:"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q.\n---\n%s", want, p)
		}
	}
	// The python+subprocess step must trigger the host-execution scope line.
	if !strings.Contains(strings.ToLower(p), "system commands on the host") {
		t.Fatalf("prompt missing host-execution scope note:\n%s", p)
	}
	// Steps must be ordered from the entry (curate before notify).
	if strings.Index(p, "curator") > strings.Index(p, "channel.send") {
		t.Fatalf("steps not in execution order:\n%s", p)
	}
	if !strings.Contains(def.Description, "step") {
		t.Fatalf("Description should summarize steps, got %q", def.Description)
	}

	// Tool classification
	if def.Builtins == nil || len(*def.Builtins) != 1 || (*def.Builtins)[0] != "channel.send" {
		t.Errorf("expected Builtins to contain [channel.send], got %v", def.Builtins)
	}
	if def.MCPTools == nil || len(*def.MCPTools) != 1 || (*def.MCPTools)[0] != "mcp__github__search" {
		t.Errorf("expected MCPTools to contain [mcp__github__search], got %v", def.MCPTools)
	}
}
