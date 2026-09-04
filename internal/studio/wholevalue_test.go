package studio

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// The exact live bug: a python step receives a whole list via "{{ .urls }}",
// which renders as Go's map[...] dump. The framework must rewrite it to real
// JSON automatically.
func TestFixWholeValueInterpolations_UrlsList(t *testing.T) {
	d := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "filter", Kind: "python", Output: "urls"},
		{ID: "podcast", Kind: "python", Input: `{"urls": "{{ .urls }}"}`, Output: "podcast"},
	}}}
	n := fixWholeValueInterpolations(d)
	if n != 1 {
		t.Fatalf("expected 1 rewrite, got %d", n)
	}
	got := d.Flow.Nodes[1].Input
	if got != `{"urls": {{ toJson .urls }}}` {
		t.Fatalf("expected toJson unquoted, got %q", got)
	}
}

// A scalar field path (.notebook.id) already renders fine quoted, and a scalar
// destination key (id) means "that object's id" — a field-level fix, not a JSON
// dump. Both are left alone.
func TestFixWholeValueInterpolations_LeavesScalars(t *testing.T) {
	d := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "t1", Kind: "tool", Tool: "x", Input: `{"notebook_id": "{{ .notebook.id }}"}`}, // field path, already fine
		{ID: "t2", Kind: "tool", Tool: "y", Input: `{"id": "{{ .notebook }}"}`},             // scalar key -> field repair, not us
	}}}
	if n := fixWholeValueInterpolations(d); n != 0 {
		t.Errorf("scalar field path and scalar key should be left alone, rewrote %d", n)
	}
}

// An already-correct unquoted toJson, and a quoted toJson, both normalize to one
// unquoted toJson (idempotent / no double-wrap).
func TestFixWholeValueInterpolations_Idempotent(t *testing.T) {
	d := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "a", Kind: "python", Input: `{"x": {{ toJson .v }}}`},   // already good (unquoted) — not matched
		{ID: "b", Kind: "python", Input: `{"y": "{{ toJson .v }}"}`}, // quoted toJson — must unwrap
	}}}
	fixWholeValueInterpolations(d)
	if d.Flow.Nodes[0].Input != `{"x": {{ toJson .v }}}` {
		t.Errorf("unquoted toJson should be untouched, got %q", d.Flow.Nodes[0].Input)
	}
	if d.Flow.Nodes[1].Input != `{"y": {{ toJson .v }}}` {
		t.Errorf("quoted toJson should unwrap to one toJson, got %q", d.Flow.Nodes[1].Input)
	}
	// Second pass changes nothing.
	before := d.Flow.Nodes[1].Input
	fixWholeValueInterpolations(d)
	if d.Flow.Nodes[1].Input != before {
		t.Error("second pass should be a no-op")
	}
}

// Prose interpolation (surrounding text) and agent inputs are left alone.
func TestFixWholeValueInterpolations_LeavesProseAndAgents(t *testing.T) {
	d := &Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "tool", Kind: "tool", Tool: "x", Input: `{"q": "AI news about {{ .topic }}"}`}, // surrounding text
		{ID: "agent", Kind: "agent", Agent: "summarizer", Input: `{{ .articles }}`},         // agent prose
	}}}
	if n := fixWholeValueInterpolations(d); n != 0 {
		t.Errorf("prose and agent inputs should be untouched, rewrote %d", n)
	}
}
