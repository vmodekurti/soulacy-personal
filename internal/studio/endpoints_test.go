package studio

import (
	"testing"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func triggerNode(id string, params map[string]any) sdkr.FlowNode {
	return sdkr.FlowNode{ID: id, Kind: sdkr.FlowNodeTrigger, Params: params}
}

func exitNode(id string, params map[string]any) sdkr.FlowNode {
	return sdkr.FlowNode{ID: id, Kind: sdkr.FlowNodeExit, Params: params}
}

// A cron trigger block + a channel exit block must project onto Trigger/Channels
// and set the flow entry to the trigger.
func TestDeriveEndpoints_CronAndChannel(t *testing.T) {
	d := &Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			triggerNode("trig", map[string]any{"kind": "cron", "config": map[string]any{"cron": "0 7 * * *"}}),
			{ID: "work", Kind: "tool", Tool: "web_search", Input: `{"query":"x"}`, Output: "r"},
			exitNode("out", map[string]any{"route": "channel", "config": map[string]any{"channel": "telegram"}}),
		},
		Entry: "work",
	}}
	DeriveEndpoints(d)

	if d.Trigger.Type != "schedule" {
		t.Errorf("expected schedule trigger, got %q", d.Trigger.Type)
	}
	if cron, _ := d.Trigger.Config["cron"].(string); cron != "0 7 * * *" {
		t.Errorf("expected cron projected, got %q", cron)
	}
	if d.Flow.Entry != "trig" {
		t.Errorf("entry should be the trigger block, got %q", d.Flow.Entry)
	}
	found := false
	for _, c := range d.Channels {
		if c == "telegram" {
			found = true
		}
	}
	if !found {
		t.Errorf("exit channel not projected onto Channels: %v", d.Channels)
	}
}

// No endpoint nodes => no changes (legacy drafts unaffected).
func TestDeriveEndpoints_NoopWithoutEndpoints(t *testing.T) {
	d := &Draft{
		Trigger: Trigger{Type: "manual"},
		Flow:    Flow{Nodes: []sdkr.FlowNode{{ID: "a", Kind: "tool", Tool: "x"}}, Entry: "a"},
	}
	DeriveEndpoints(d)
	if d.Trigger.Type != "manual" || d.Flow.Entry != "a" {
		t.Error("draft without endpoint nodes should be unchanged")
	}
}

func TestValidateEndpoints_MultipleTriggersBlocked(t *testing.T) {
	d := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		triggerNode("t1", map[string]any{"kind": "cron"}),
		triggerNode("t2", map[string]any{"kind": "http"}),
		exitNode("e", map[string]any{"route": "console"}),
	}}}
	issues := ValidateEndpoints(d)
	blocked := false
	for _, i := range issues {
		if i.Severity == "block" {
			blocked = true
		}
	}
	if !blocked {
		t.Errorf("two triggers should produce a blocker, got %v", issues)
	}
}

func TestValidateEndpoints_WarnsNoExit(t *testing.T) {
	d := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		triggerNode("t1", map[string]any{"kind": "cron"}),
		{ID: "work", Kind: "tool", Tool: "x"},
	}}}
	issues := ValidateEndpoints(d)
	if len(issues) == 0 {
		t.Error("a trigger with no exit should warn")
	}
}

func TestValidateEndpoints_NoopWithoutEndpoints(t *testing.T) {
	d := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{{ID: "a", Kind: "tool", Tool: "x"}}}}
	if issues := ValidateEndpoints(d); issues != nil {
		t.Errorf("legacy draft should have no endpoint issues, got %v", issues)
	}
}

// A trigger/exit graph must compile (kinds accepted by reasoning.CompileFlow).
func TestEndpointGraph_Compiles(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			triggerNode("trig", map[string]any{"kind": "cron"}),
			{ID: "work", Kind: "tool", Tool: "web_search", Input: `{"query":"x"}`, Output: "r"},
			exitNode("out", map[string]any{"route": "console"}),
		},
		Edges: []sdkr.FlowEdge{
			{From: "trig", To: "work"},
			{From: "work", To: "out"},
		},
		Entry: "trig",
	}}
	if _, err := reasoning.CompileFlow(d.spec()); err != nil {
		t.Fatalf("trigger/exit graph should compile: %v", err)
	}
}
