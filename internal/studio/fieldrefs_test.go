package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// The create→add→generate→poll dance: every later step references {{ .id }} with
// no producer. reconcileFieldRefs must rewrite each to the create step's object
// output (.notebook.id), the deterministic fix for the live NotebookLM blockers.
func TestReconcileFieldRefs_NotebookLMDance(t *testing.T) {
	d := &Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "create_notebook", Kind: "tool", Tool: "mcp__nlm__create",
				Input: `{"title":"AI"}`, Output: "notebook"},
			{ID: "add_sources", Kind: "tool", Tool: "mcp__nlm__add",
				Input: `{"notebook_id":"{{ .id }}"}`, Output: "added"},
			{ID: "generate_audio", Kind: "tool", Tool: "mcp__nlm__gen",
				Input: `{"notebook_id":"{{ .id }}"}`, Output: "audio"},
			{ID: "poll_audio_status", Kind: "tool", Tool: "mcp__nlm__status",
				Input: `{"notebook_id":"{{.id}}"}`, Output: "status"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "create_notebook", To: "add_sources"},
			{From: "add_sources", To: "generate_audio"},
			{From: "generate_audio", To: "poll_audio_status"},
		},
	}}

	n := reconcileFieldRefs(d)
	if n != 3 {
		t.Fatalf("expected 3 field refs rewritten, got %d", n)
	}
	for _, id := range []string{"add_sources", "generate_audio", "poll_audio_status"} {
		node := nodeByID(d, id)
		if !strings.Contains(node.Input, ".notebook.id") {
			t.Errorf("%s should reference .notebook.id, got %q", id, node.Input)
		}
		if strings.Contains(node.Input, `."{{ .id }}"`) {
			t.Errorf("%s still has a dangling .id", id)
		}
	}
}

// A dangling field with two equally-early producers must NOT be guessed.
func TestReconcileFieldRefs_AmbiguousLeftAlone(t *testing.T) {
	d := &Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Output: "alpha", Input: `{}`},
			{ID: "b", Output: "beta", Input: `{}`},
			{ID: "c", Input: `{"x":"{{ .id }}"}`},
		},
		Edges: []sdkr.FlowEdge{
			{From: "a", To: "c"},
			{From: "b", To: "c"},
		},
	}}
	if n := reconcileFieldRefs(d); n != 0 {
		t.Errorf("ambiguous earliest producer should be left alone, rewrote %d", n)
	}
	if !strings.Contains(nodeByID(d, "c").Input, "{{ .id }}") {
		t.Error("ambiguous ref should be untouched")
	}
}

// A real var ref and an unknown field (Format) are both left alone.
func TestReconcileFieldRefs_LeavesVarsAndUnknownFields(t *testing.T) {
	d := &Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Output: "notebook", Input: `{}`},
			{ID: "b", Input: `{"x":"{{ .notebook }}","y":"{{ .Format }}"}`},
		},
		Edges: []sdkr.FlowEdge{{From: "a", To: "b"}},
	}}
	if n := reconcileFieldRefs(d); n != 0 {
		t.Errorf("a real var and an unknown field should be left alone, rewrote %d", n)
	}
}

// prefixFieldRef must never touch a trailing .id inside an existing chain.
func TestPrefixFieldRef_DoesNotDoublePrefix(t *testing.T) {
	in := `{"x":"{{ .notebook.id }}"}`
	if got := prefixFieldRef(in, "id", "notebook"); got != in {
		t.Errorf("existing chain should be untouched, got %q", got)
	}
}

func nodeByID(d *Draft, id string) *sdkr.FlowNode {
	for i := range d.Flow.Nodes {
		if d.Flow.Nodes[i].ID == id {
			return &d.Flow.Nodes[i]
		}
	}
	return nil
}
