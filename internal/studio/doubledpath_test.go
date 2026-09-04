package studio

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// The exact live crash: {{ .notebook_id.notebook_id.id }} over-reaches a doubled
// segment. The framework must truncate to {{ .notebook_id.notebook_id }}.
func TestFixDoubledSegmentPaths_NotebookId(t *testing.T) {
	d := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "import_sources", Kind: "tool", Tool: "mcp__nlm__source_add",
			Input: `{"notebook_id": "{{ .notebook_id.notebook_id.id }}", "url": "x"}`},
	}}}
	n := fixDoubledSegmentPaths(d)
	if n != 1 {
		t.Fatalf("expected 1 rewrite, got %d", n)
	}
	got := d.Flow.Nodes[0].Input
	if got != `{"notebook_id": "{{ .notebook_id.notebook_id }}", "url": "x"}` {
		t.Fatalf("trailing over-reach not stripped, got %q", got)
	}
}

// A correct doubled-but-leaf path (no trailing segment) is left alone, and a
// normal non-duplicated path is untouched.
func TestFixDoubledSegmentPaths_LeavesValidPaths(t *testing.T) {
	d := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "a", Kind: "tool", Tool: "t", Input: `{"x": "{{ .notebook_id.notebook_id }}"}`}, // already correct
		{ID: "b", Kind: "tool", Tool: "t", Input: `{"y": "{{ .notebook.id }}"}`},             // normal nested
		{ID: "c", Kind: "tool", Tool: "t", Input: `{"z": "{{ toJson .articles }}"}`},         // no dup
	}}}
	if n := fixDoubledSegmentPaths(d); n != 0 {
		t.Errorf("valid paths should be left alone, rewrote %d", n)
	}
}

// Also fixes edge predicates.
func TestFixDoubledSegmentPaths_Edges(t *testing.T) {
	d := &Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{{ID: "a", Kind: "tool", Tool: "t"}},
		Edges: []sdkr.FlowEdge{{From: "a", To: "end", If: `{{ eq .res.res.status "ok" }}`}},
	}}
	if n := fixDoubledSegmentPaths(d); n != 1 {
		t.Fatalf("expected the edge predicate fixed, got %d", n)
	}
	if d.Flow.Edges[0].If != `{{ eq .res.res "ok" }}` {
		t.Errorf("edge predicate over-reach not stripped, got %q", d.Flow.Edges[0].If)
	}
}
