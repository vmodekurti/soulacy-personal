package runtime

import (
	"testing"
	"time"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestNodeExecTimeout(t *testing.T) {
	cases := []struct {
		name     string
		node     sdkr.FlowNode
		rendered string
		want     time.Duration
	}{
		{
			name:     "explicit node timeout wins",
			node:     sdkr.FlowNode{Timeout: "5m"},
			rendered: `{"max_wait":1200}`,
			want:     5 * time.Minute,
		},
		{
			name:     "derived from max_wait in rendered input (+headroom)",
			node:     sdkr.FlowNode{},
			rendered: `{"notebook_id":"abc","max_wait":1200}`,
			want:     1200*time.Second + nodeWaitHeadroom,
		},
		{
			name: "derived from timeout_s in params",
			node: sdkr.FlowNode{Params: map[string]any{"timeout_s": float64(900)}},
			want: 900*time.Second + nodeWaitHeadroom,
		},
		{
			name:     "duration-string wait",
			node:     sdkr.FlowNode{},
			rendered: `{"wait":"20m"}`,
			want:     1200*time.Second + nodeWaitHeadroom,
		},
		{
			name:     "no wait arg, non-MCP → zero (use global)",
			node:     sdkr.FlowNode{},
			rendered: `{"query":"x"}`,
			want:     0,
		},
		{
			name:     "external MCP tool with no declared wait → generous default",
			node:     sdkr.FlowNode{Tool: "mcp__notebooklm__research_import"},
			rendered: `{"notebook_id":"abc","task_id":"xyz"}`,
			want:     defaultMCPFlowTimeout,
		},
		{
			name:     "kb_write with no declared wait gets bounded knowledge default",
			node:     sdkr.FlowNode{Tool: "kb_write"},
			rendered: `{"kb":"AI-Docs","content":"hello"}`,
			want:     defaultKBWriteFlowTimeout,
		},
		{
			name:     "declared wait still wins for kb_write",
			node:     sdkr.FlowNode{Tool: "kb_write"},
			rendered: `{"kb":"AI-Docs","content":"hello","timeout_s":300}`,
			want:     300*time.Second + nodeWaitHeadroom,
		},
		{
			name:     "explicit timeout still wins for an MCP tool",
			node:     sdkr.FlowNode{Tool: "mcp__x__y", Timeout: "30s"},
			rendered: `{}`,
			want:     30 * time.Second,
		},
		{
			name:     "invalid explicit timeout falls back to derive",
			node:     sdkr.FlowNode{Timeout: "10 minutes"},
			rendered: `{"max_wait":300}`,
			want:     300*time.Second + nodeWaitHeadroom,
		},
	}
	for _, c := range cases {
		if got := nodeExecTimeout(c.node, c.rendered); got != c.want {
			t.Errorf("%s: nodeExecTimeout=%v want %v", c.name, got, c.want)
		}
	}
}
