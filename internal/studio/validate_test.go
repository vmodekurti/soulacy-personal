package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// validBranchingDraft is a multi-node branching draft that compiles cleanly.
func validBranchingDraft() Draft {
	return Draft{
		Name:    "Triage",
		Trigger: Trigger{Type: "schedule", Config: map[string]any{"cron": "0 8 * * 1-5"}},
		Flow: Flow{
			Entry: "fetch",
			Nodes: []sdkr.FlowNode{
				{ID: "fetch", Kind: "tool", Tool: "http_get", Input: "{}", Output: "stories"},
				{ID: "triage", Kind: "branch"},
				{ID: "summarize", Kind: "agent", Agent: "summarizer", Input: "go", Output: "summary"},
				{ID: "skip", Kind: "agent", Agent: "notifier", Input: "no", Output: "note"},
			},
			Edges: []sdkr.FlowEdge{
				{From: "fetch", To: "triage"},
				{From: "triage", To: "summarize", If: "{{ gt (len .stories) 0 }}"},
				{From: "triage", To: "skip"},
				{From: "summarize", To: "end"},
				{From: "skip", To: "end"},
			},
		},
	}
}

// TestValidate_ValidBranchingDraft (Story M3): a valid multi-node branching
// draft validates ok with no errors.
func TestValidate_ValidBranchingDraft(t *testing.T) {
	res := Validate(validBranchingDraft())
	if !res.Ok {
		t.Fatalf("expected ok=true, got ok=false with errors: %+v", res.Errors)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("expected no errors, got %+v", res.Errors)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("expected no warnings on a clean graph, got %+v", res.Warnings)
	}
}

// TestValidate_DanglingEdge (Story M3): a draft with an edge pointing at a
// non-existent target is ok=false with a precise, edge-attributed message.
func TestValidate_DanglingEdge(t *testing.T) {
	d := validBranchingDraft()
	// Re-point the first triage out-edge (edge index 1) at a missing node.
	d.Flow.Edges[1].To = "ghost"

	res := Validate(d)
	if res.Ok {
		t.Fatalf("expected ok=false for a dangling edge")
	}
	if len(res.Errors) == 0 {
		t.Fatalf("expected at least one error")
	}
	e := res.Errors[0]
	if e.EdgeIndex == nil || *e.EdgeIndex != 1 {
		t.Fatalf("expected error attributed to edge index 1, got edgeIndex=%v", e.EdgeIndex)
	}
	if !strings.Contains(e.Message, "ghost") || !strings.Contains(e.Message, "unknown node") {
		t.Fatalf("expected a precise dangling-edge message naming \"ghost\", got %q", e.Message)
	}
}

// TestValidate_BadEntry (Story M3): a draft whose entry names a missing node
// is ok=false with a precise message attributed to that entry id.
func TestValidate_BadEntry(t *testing.T) {
	d := validBranchingDraft()
	d.Flow.Entry = "nope"

	res := Validate(d)
	if res.Ok {
		t.Fatalf("expected ok=false for a bad entry")
	}
	if len(res.Errors) == 0 {
		t.Fatalf("expected at least one error")
	}
	e := res.Errors[0]
	if !strings.Contains(e.Message, "entry node") || !strings.Contains(e.Message, "nope") {
		t.Fatalf("expected a precise bad-entry message, got %q", e.Message)
	}
	if e.NodeID != "nope" {
		t.Fatalf("expected nodeId attributed to the missing entry, got %q", e.NodeID)
	}
}

// TestValidate_UndeclaredPortError (Story M3): an edge naming a port not
// declared on the node is a hard error attributed to the edge.
func TestValidate_UndeclaredPortError(t *testing.T) {
	d := Draft{
		Name:    "Ports",
		Trigger: Trigger{Type: "manual"},
		Flow: Flow{
			Entry: "a",
			Nodes: []sdkr.FlowNode{
				{ID: "a", Kind: "tool", Tool: "http_get", Input: "{}"},
				{ID: "b", Kind: "agent", Agent: "summarizer", Input: "go"},
			},
			Edges: []sdkr.FlowEdge{
				{From: "a", To: "b", FromPort: "missing"},
				{From: "b", To: "end"},
			},
		},
	}
	res := Validate(d)
	if res.Ok {
		t.Fatalf("expected ok=false for an undeclared port")
	}
	e := res.Errors[0]
	if e.EdgeIndex == nil || *e.EdgeIndex != 0 {
		t.Fatalf("expected edge index 0, got %v", e.EdgeIndex)
	}
	if !strings.Contains(e.Message, "from_port") {
		t.Fatalf("expected message to mention from_port, got %q", e.Message)
	}
}

// TestValidate_UnreachableNodeWarning (Story M3): a node with no incoming edge
// that isn't the entry compiles but earns a soft warning.
func TestValidate_UnreachableNodeWarning(t *testing.T) {
	d := Draft{
		Name:    "Lonely",
		Trigger: Trigger{Type: "manual"},
		Flow: Flow{
			Entry: "a",
			Nodes: []sdkr.FlowNode{
				{ID: "a", Kind: "agent", Agent: "x", Input: "go"},
				{ID: "orphan", Kind: "agent", Agent: "y", Input: "go"},
			},
			Edges: []sdkr.FlowEdge{
				{From: "a", To: "end"},
			},
		},
	}
	res := Validate(d)
	if !res.Ok {
		t.Fatalf("expected ok=true (graph compiles), got errors %+v", res.Errors)
	}
	found := false
	for _, w := range res.Warnings {
		if w.NodeID == "orphan" && strings.Contains(w.Message, "unreachable") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an unreachable warning for \"orphan\", got %+v", res.Warnings)
	}
}

func TestValidate_DuplicateOutputVarError(t *testing.T) {
	d := Draft{
		Name:    "Duplicate vars",
		Trigger: Trigger{Type: "manual"},
		Flow: Flow{
			Entry: "a",
			Nodes: []sdkr.FlowNode{
				{ID: "a", Kind: "tool", Tool: "http_get", Input: "{}", Output: "shared"},
				{ID: "b", Kind: "agent", Agent: "summarizer", Input: "go", Output: "shared"},
			},
			Edges: []sdkr.FlowEdge{{From: "a", To: "b"}},
		},
	}
	res := Validate(d)
	if res.Ok {
		t.Fatalf("expected ok=false for duplicate output vars")
	}
	found := false
	for _, e := range res.Errors {
		if e.NodeID == "b" && strings.Contains(e.Message, "produced by both") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected duplicate output-var error on b, got %+v", res.Errors)
	}
}

// TestValidate_UnknownTriggerWarning (Story M3): an unrecognized trigger type
// is a soft warning, not an error.
func TestValidate_UnknownTriggerWarning(t *testing.T) {
	d := validBranchingDraft()
	d.Trigger.Type = "carrier-pigeon"

	res := Validate(d)
	if !res.Ok {
		t.Fatalf("expected ok=true; unknown trigger is a warning, got errors %+v", res.Errors)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w.Message, "unknown trigger type") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an unknown-trigger warning, got %+v", res.Warnings)
	}
}
