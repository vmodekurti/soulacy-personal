package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestSystemToolWarnings(t *testing.T) {
	f := Flow{Nodes: []sdkr.FlowNode{
		{ID: "glue", Kind: "tool", Tool: "python_eval", Input: `{"code":"x"}`},
		{ID: "ok", Kind: "tool", Tool: "web_search", Input: `{"query":"x"}`},
		{ID: "py", Kind: "python", Code: "def run(inputs):\n    return inputs"},
	}}
	warns := systemToolWarnings(f)
	if len(warns) != 1 || warns[0].NodeID != "glue" {
		t.Fatalf("only the python_eval tool node should warn; got %+v", warns)
	}
	if !strings.Contains(warns[0].Message, "Custom Python") {
		t.Errorf("warning should point at the Custom Python alternative: %q", warns[0].Message)
	}
}

// The warning flows through the full Validate result (and never as a hard error,
// since a system-tooled agent could still run it).
func TestValidate_SurfacesSystemToolWarning(t *testing.T) {
	d := Draft{Name: "X", Trigger: Trigger{Type: "manual"}, Flow: Flow{
		Nodes: []sdkr.FlowNode{{ID: "glue", Kind: "tool", Tool: "shell_exec", Input: `{"command":"ls"}`, Output: "r"}},
		Entry: "glue",
	}}
	res := Validate(d)
	var found bool
	for _, w := range res.Warnings {
		if w.NodeID == "glue" && strings.Contains(w.Message, "system tool") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a system-tool warning in Validate result; got %+v", res.Warnings)
	}
}
