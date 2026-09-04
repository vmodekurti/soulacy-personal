package studio

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestPreflight_PythonNodeMissingRunBlocks(t *testing.T) {
	// Code without a top-level def run( → block (this is the scheduled-agent
	// "name 'run' is not defined" failure caught at save time).
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "p", Kind: "python", Code: "import datetime\nx = 1\n"},
	}}}
	r := Preflight(d, PreflightInput{})
	if r.OK {
		t.Fatal("python node without def run should block save")
	}
	// Valid top-level def run → no python block.
	d2 := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "p", Kind: "python", Code: "import datetime\ndef run(inputs):\n    return 1\n"},
	}}}
	for _, b := range Preflight(d2, PreflightInput{}).Blockers {
		if b.NodeID == "p" {
			t.Errorf("valid python node should not block: %+v", b)
		}
	}
}

func TestDefinesRunEntrypoint(t *testing.T) {
	if !definesRunEntrypoint("def run(inputs):\n    return 1") {
		t.Error("top-level def run should be detected")
	}
	if definesRunEntrypoint("    def run(inputs):\n        return 1") {
		t.Error("indented/nested def run must NOT count (harness can't see it)")
	}
	if definesRunEntrypoint("") {
		t.Error("empty code must not count")
	}
}
