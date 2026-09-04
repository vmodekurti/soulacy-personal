package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestNodeTimeoutWarnings(t *testing.T) {
	f := Flow{Nodes: []sdkr.FlowNode{
		{ID: "ok", Kind: "tool", Tool: "t", Timeout: "10m"},         // valid → no warning
		{ID: "blank", Kind: "tool", Tool: "t"},                      // empty → no warning
		{ID: "bad", Kind: "tool", Tool: "t", Timeout: "10 minutes"}, // invalid
		{ID: "zero", Kind: "tool", Tool: "t", Timeout: "0s"},        // non-positive
	}}
	warns := nodeTimeoutWarnings(f)
	flagged := map[string]bool{}
	for _, w := range warns {
		flagged[w.NodeID] = true
		if !strings.Contains(w.Message, "duration") {
			t.Errorf("warning should explain the duration format: %q", w.Message)
		}
	}
	if flagged["ok"] || flagged["blank"] {
		t.Errorf("valid/empty timeouts must not be flagged; got %+v", warns)
	}
	if !flagged["bad"] || !flagged["zero"] {
		t.Errorf("invalid/non-positive timeouts must be flagged; got %+v", warns)
	}
}

// A valid per-node timeout round-trips through the whole Validate path without
// becoming an error (it's a successful, compilable graph).
func TestValidate_AcceptsValidNodeTimeout(t *testing.T) {
	d := Draft{Name: "X", Trigger: Trigger{Type: "manual"}, Flow: Flow{
		Nodes: []sdkr.FlowNode{{ID: "a", Kind: "tool", Tool: "web_search", Input: `{"query":"x"}`, Output: "r", Timeout: "5m"}},
		Entry: "a",
	}}
	res := Validate(d)
	for _, w := range res.Warnings {
		if w.NodeID == "a" && strings.Contains(w.Message, "duration") {
			t.Errorf("a valid timeout should not warn: %q", w.Message)
		}
	}
}
