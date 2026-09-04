package reasoning

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestFlowPorts_JSONRoundTrip verifies the Story S0.3 typed ports/params
// fields survive a JSON marshal/unmarshal cycle. (The SDK module is
// dependency-free by contract, so JSON stands in for serialization here;
// the YAML round-trip is covered in internal/reasoning.)
func TestFlowPorts_JSONRoundTrip(t *testing.T) {
	in := FlowSpec{
		Nodes: []FlowNode{{
			ID:   "a",
			Tool: "t",
			Inputs: []FlowPort{
				{Name: "in1", Type: "string", Label: "Input One"},
			},
			Outputs: []FlowPort{
				{Name: "out1", Type: "json", Label: "Output One"},
			},
			Params: map[string]any{"limit": float64(5), "mode": "fast"},
		}},
		Edges: []FlowEdge{{From: "a", To: "end", FromPort: "out1"}},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got FlowSpec
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	n := got.Nodes[0]
	if len(n.Inputs) != 1 || n.Inputs[0].Name != "in1" || n.Inputs[0].Type != "string" || n.Inputs[0].Label != "Input One" {
		t.Errorf("inputs round-trip wrong: %+v", n.Inputs)
	}
	if len(n.Outputs) != 1 || n.Outputs[0].Name != "out1" {
		t.Errorf("outputs round-trip wrong: %+v", n.Outputs)
	}
	if n.Params["limit"] != float64(5) || n.Params["mode"] != "fast" {
		t.Errorf("params round-trip wrong: %+v", n.Params)
	}
	if got.Edges[0].FromPort != "out1" {
		t.Errorf("from_port round-trip wrong: %q", got.Edges[0].FromPort)
	}
}

// TestFlowSpec_BackwardCompatZeroValues confirms a portless/paramless spec
// marshals without any of the new keys (append-only, zero-value clean), so
// existing flows serialize byte-identically to before Story S0.3.
func TestFlowSpec_BackwardCompatZeroValues(t *testing.T) {
	in := FlowSpec{
		Nodes: []FlowNode{{ID: "a", Tool: "t"}},
		Edges: []FlowEdge{{From: "a", To: "end"}},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"inputs", "outputs", "params", "from_port", "to_port"} {
		if strings.Contains(string(b), `"`+key+`"`) {
			t.Errorf("zero-value spec leaked %q key:\n%s", key, b)
		}
	}
}
