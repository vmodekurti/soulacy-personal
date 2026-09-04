package studio

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestDeriveNodeTimeouts_FromInputMaxWait(t *testing.T) {
	// Mirrors the real NotebookLM poll: max_wait:1200 in the tool input.
	d := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "poll", Kind: "tool", Tool: "mcp__notebooklm__research_status",
			Input: `{"notebook_id":"{{ .nb.id }}","max_wait":1200}`},
	}}}
	if got := deriveNodeTimeouts(d); got != 1 {
		t.Fatalf("want 1 node updated, got %d", got)
	}
	// 1200s wait + 60s headroom.
	if d.Flow.Nodes[0].Timeout != "1260s" {
		t.Errorf("want Timeout 1260s, got %q", d.Flow.Nodes[0].Timeout)
	}
}

func TestDeriveNodeTimeouts_FromParamsTimeoutS(t *testing.T) {
	d := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "poll", Kind: "tool", Tool: "t", Params: map[string]any{"timeout_s": float64(900)}},
	}}}
	deriveNodeTimeouts(d)
	if d.Flow.Nodes[0].Timeout != "960s" {
		t.Errorf("want 960s from params timeout_s, got %q", d.Flow.Nodes[0].Timeout)
	}
}

func TestDeriveNodeTimeouts_DurationStringWait(t *testing.T) {
	d := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "poll", Kind: "tool", Tool: "t", Input: `{"wait":"20m"}`},
	}}}
	deriveNodeTimeouts(d)
	if d.Flow.Nodes[0].Timeout != "1260s" { // 20m = 1200s + 60s
		t.Errorf("want 1260s from \"20m\", got %q", d.Flow.Nodes[0].Timeout)
	}
}

func TestDeriveNodeTimeouts_NeverOverridesExplicit(t *testing.T) {
	d := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "poll", Kind: "tool", Tool: "t", Timeout: "5m", Input: `{"max_wait":1200}`},
	}}}
	if got := deriveNodeTimeouts(d); got != 0 {
		t.Fatalf("explicit Timeout must not be overridden; got %d updates", got)
	}
	if d.Flow.Nodes[0].Timeout != "5m" {
		t.Errorf("explicit Timeout changed to %q", d.Flow.Nodes[0].Timeout)
	}
}

func TestDeriveNodeTimeouts_NoWaitNoChange_AndIdempotent(t *testing.T) {
	d := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "a", Kind: "tool", Tool: "t", Input: `{"query":"x"}`},
	}}}
	if got := deriveNodeTimeouts(d); got != 0 {
		t.Errorf("a node with no wait arg must not be touched; got %d", got)
	}
	// A node WITH a wait is set once, then stable.
	d2 := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "poll", Kind: "tool", Tool: "t", Input: `{"max_wait":300}`},
	}}}
	first := deriveNodeTimeouts(d2)
	second := deriveNodeTimeouts(d2)
	if first != 1 || second != 0 {
		t.Errorf("derive not idempotent: first=%d second=%d", first, second)
	}
}
