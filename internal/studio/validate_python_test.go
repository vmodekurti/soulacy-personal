package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestValidate_PythonNodeWarnings(t *testing.T) {
	warnMsgs := func(d Draft) string {
		var b strings.Builder
		for _, w := range Validate(d).Warnings {
			b.WriteString(w.Message)
			b.WriteString("\n")
		}
		return b.String()
	}

	// Code without run() -> warning.
	d := Draft{Name: "x", Trigger: Trigger{Type: "manual"}, Flow: Flow{Entry: "p", Nodes: []sdkr.FlowNode{
		{ID: "p", Kind: sdkr.FlowNodePython, Code: "x = 1"},
	}}}
	if !strings.Contains(warnMsgs(d), "run(inputs)") {
		t.Fatalf("expected run() warning, got: %s", warnMsgs(d))
	}

	// Proper run() -> no python warning.
	d2 := Draft{Name: "x", Trigger: Trigger{Type: "manual"}, Flow: Flow{Entry: "p", Nodes: []sdkr.FlowNode{
		{ID: "p", Kind: sdkr.FlowNodePython, Code: "def run(inputs):\n    return inputs"},
	}}}
	if strings.Contains(warnMsgs(d2), "python node") {
		t.Fatalf("did not expect a python warning, got: %s", warnMsgs(d2))
	}

	// Empty code + no tool -> hard validate ERROR (CompileFlow rejects it).
	d3 := Draft{Name: "x", Trigger: Trigger{Type: "manual"}, Flow: Flow{Entry: "p", Nodes: []sdkr.FlowNode{
		{ID: "p", Kind: sdkr.FlowNodePython},
	}}}
	var errs strings.Builder
	for _, e := range Validate(d3).Errors {
		errs.WriteString(e.Message)
		errs.WriteString("\n")
	}
	if !strings.Contains(errs.String(), "neither inline code nor a tool") {
		t.Fatalf("expected a compile error for code+tool-less python node, got: %s", errs.String())
	}
}
