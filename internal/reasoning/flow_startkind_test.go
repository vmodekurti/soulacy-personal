package reasoning

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// A builder model sometimes emits a "start"/"end" node kind. The compiler must
// tolerate these as structural passthroughs instead of hard-failing the flow.
func TestCompileFlow_ToleratesStartEndKinds(t *testing.T) {
	spec := sdkr.FlowSpec{
		Entry: "begin",
		Nodes: []sdkr.FlowNode{
			{ID: "begin", Kind: "start"},
			{ID: "work", Kind: "agent", Agent: "helper"},
			{ID: "done", Kind: "end"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "begin", To: "work"},
			{From: "work", To: "done"},
		},
	}
	if _, err := CompileFlow(spec); err != nil {
		t.Fatalf("expected start/end kinds to compile, got: %v", err)
	}
}

func TestCompileFlow_StillRejectsUnknownKind(t *testing.T) {
	spec := sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{{ID: "a", Kind: "sqlquery"}},
	}
	if _, err := CompileFlow(spec); err == nil {
		t.Fatalf("expected a truly unknown kind to still error")
	}
}
