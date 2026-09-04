package studio

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/reasoning"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// TestPortize_EndToEndRuntimeResolution is the load-bearing proof: after
// lowering a field-path handoff to a typed port, the REAL runtime
// (reasoning.RunFlow) assembles the consumer's input from the wire with NO
// templating, and the consumer receives the extracted field value.
func TestPortize_EndToEndRuntimeResolution(t *testing.T) {
	d := linearDraft(`{"notebook_id":"{{ .notebook.id }}"}`)
	if n := PortizeHandoffs(d, Catalog{}); n != 1 {
		t.Fatalf("want 1 handoff lowered, got %d", n)
	}

	spec := sdkr.FlowSpec{Nodes: d.Flow.Nodes, Edges: d.Flow.Edges, Entry: d.Flow.Entry}
	g, err := reasoning.CompileFlow(spec)
	if err != nil {
		t.Fatalf("compile portized flow: %v", err)
	}

	var useInput string
	run := func(ctx context.Context, node sdkr.FlowNode, rendered string) (json.RawMessage, error) {
		switch node.ID {
		case "create":
			return json.RawMessage(`{"id":"NB-123","title":"My Notebook"}`), nil
		case "use":
			useInput = rendered
			return json.RawMessage(`{"ok":true}`), nil
		}
		return json.RawMessage(`{}`), nil
	}
	if _, err := reasoning.RunFlow(context.Background(), g, map[string]any{}, run, reasoning.FlowHooks{}); err != nil {
		t.Fatalf("run portized flow: %v", err)
	}

	// The consumer must have received the extracted id, template-free.
	var got map[string]any
	if err := json.Unmarshal([]byte(useInput), &got); err != nil {
		t.Fatalf("consumer input not JSON: %q (%v)", useInput, err)
	}
	if got["notebook_id"] != "NB-123" {
		t.Errorf("wire did not deliver the field; consumer input = %q", useInput)
	}
	if strings.Contains(useInput, "{{") {
		t.Errorf("consumer input should be template-free; got %q", useInput)
	}
}

// linearDraft builds create→use with a field-path handoff template on the
// consumer, plus a direct control edge.
func linearDraft(consumerInput string) *Draft {
	return &Draft{Name: "L", Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "create", Kind: "tool", Tool: "mk", Output: "notebook", Input: `{"title":"t"}`},
			{ID: "use", Kind: "tool", Tool: "use", Output: "used", Input: consumerInput},
		},
		Edges: []sdkr.FlowEdge{{From: "create", To: "use"}},
		Entry: "create",
	}}
}

func TestPortize_FieldPathAnnotatesExistingEdge(t *testing.T) {
	d := linearDraft(`{"notebook_id":"{{ .notebook.id }}"}`)
	if n := PortizeHandoffs(d, Catalog{}); n != 1 {
		t.Fatalf("want 1 handoff lowered, got %d", n)
	}
	use := d.Flow.Nodes[1]
	if strings.Contains(use.Input, "{{") {
		t.Errorf("template should be gone from consumer input: %q", use.Input)
	}
	if !outputPortExists(use.Inputs, "notebook_id") {
		t.Errorf("consumer should declare input port notebook_id; got %+v", use.Inputs)
	}
	create := d.Flow.Nodes[0]
	var op *sdkr.FlowPort
	for i := range create.Outputs {
		if create.Outputs[i].Field == "id" {
			op = &create.Outputs[i]
		}
	}
	if op == nil {
		t.Fatalf("producer should declare an output port with Field=id; got %+v", create.Outputs)
	}
	// The existing direct edge is annotated in place (no new edge added).
	if len(d.Flow.Edges) != 1 {
		t.Fatalf("should annotate the existing edge, not add one; edges=%+v", d.Flow.Edges)
	}
	e := d.Flow.Edges[0]
	if e.FromPort != op.Name || e.ToPort != "notebook_id" || e.If != "" {
		t.Errorf("edge not wired correctly / predicate disturbed: %+v", e)
	}
}

func TestPortize_WholeOutputUsesImplicitFromPort(t *testing.T) {
	d := linearDraft(`{"whole":"{{ .notebook }}"}`)
	if n := PortizeHandoffs(d, Catalog{}); n != 1 {
		t.Fatalf("want 1, got %d", n)
	}
	e := d.Flow.Edges[0]
	if e.FromPort != "" || e.ToPort != "whole" {
		t.Errorf("whole-output wire should have empty from_port: %+v", e)
	}
	// No output port needed for a whole-output wire.
	if len(d.Flow.Nodes[0].Outputs) != 0 {
		t.Errorf("whole-output handoff should not declare an output port; got %+v", d.Flow.Nodes[0].Outputs)
	}
}

func TestPortize_TwoWiresSameProducerAddsDataEdge(t *testing.T) {
	d := linearDraft(`{"notebook_id":"{{ .notebook.id }}","name":"{{ .notebook.title }}"}`)
	if n := PortizeHandoffs(d, Catalog{}); n != 2 {
		t.Fatalf("want 2 wires, got %d", n)
	}
	// One edge annotated in place, one data-only edge added (if:"false").
	if len(d.Flow.Edges) != 2 {
		t.Fatalf("expected 2 edges (1 annotated + 1 data-only), got %+v", d.Flow.Edges)
	}
	var control, data int
	for _, e := range d.Flow.Edges {
		if e.If == "false" {
			data++
		} else {
			control++
		}
	}
	if control != 1 || data != 1 {
		t.Errorf("expected 1 control + 1 data-only edge; got control=%d data=%d (%+v)", control, data, d.Flow.Edges)
	}
}

func TestPortize_IgnoresProseAndConstantsAndUnknownVars(t *testing.T) {
	// Prose-with-template (agent-style), a constant, and a ref to a non-producer
	// must all be left untouched.
	d := &Draft{Name: "X", Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Kind: "tool", Tool: "mk", Output: "nb", Input: `{"title":"t"}`},
			{ID: "b", Kind: "tool", Tool: "use", Output: "u",
				Input: `{"q":"summarize {{ .nb.title }}","k":"const","z":"{{ .ghost }}"}`},
		},
		Edges: []sdkr.FlowEdge{{From: "a", To: "b"}},
		Entry: "a",
	}}
	if n := PortizeHandoffs(d, Catalog{}); n != 0 {
		t.Fatalf("nothing is a clean whole-value handoff; want 0, got %d", n)
	}
	if !strings.Contains(d.Flow.Nodes[1].Input, "summarize {{ .nb.title }}") {
		t.Errorf("prose template must be preserved: %q", d.Flow.Nodes[1].Input)
	}
}

func TestPortize_Idempotent(t *testing.T) {
	d := linearDraft(`{"notebook_id":"{{ .notebook.id }}"}`)
	first := PortizeHandoffs(d, Catalog{})
	edgesAfterFirst := len(d.Flow.Edges)
	second := PortizeHandoffs(d, Catalog{})
	if first != 1 || second != 0 {
		t.Fatalf("portize not idempotent: first=%d second=%d", first, second)
	}
	if len(d.Flow.Edges) != edgesAfterFirst {
		t.Errorf("second pass should add nothing; edges %d→%d", edgesAfterFirst, len(d.Flow.Edges))
	}
}

func TestPortize_StampsInputPortTypeFromSchema(t *testing.T) {
	d := linearDraft(`{"notebook_id":"{{ .notebook.id }}"}`)
	d.Flow.Nodes[1].Tool = "use" // consumer tool
	cat := Catalog{MCP: []CatalogMCPServer{{Tools: []CatalogMCPTool{
		{Name: "use", Params: "notebook_id*:string, count:number"},
	}}}}
	if n := PortizeHandoffs(d, cat); n != 1 {
		t.Fatalf("want 1, got %d", n)
	}
	var typ string
	for _, p := range d.Flow.Nodes[1].Inputs {
		if p.Name == "notebook_id" {
			typ = p.Type
		}
	}
	if typ != "string" {
		t.Errorf("input port should carry the schema type string; got %q (ports=%+v)", typ, d.Flow.Nodes[1].Inputs)
	}
}

func TestValidateToolArgs_FlagsBogusPortBinding(t *testing.T) {
	// A typed input port that names an argument the tool doesn't accept must be
	// flagged (the port wire is validated against the real schema, not trusted).
	d := Draft{Name: "X", Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "b", Kind: "tool", Tool: "use", Inputs: []sdkr.FlowPort{{Name: "bogus_arg"}}},
	}}}
	cat := Catalog{MCP: []CatalogMCPServer{{Tools: []CatalogMCPTool{
		{Name: "use", Params: "notebook_id*:string"},
	}}}}
	warns := ValidateToolArgs(d, cat)
	var found bool
	for _, w := range warns {
		if strings.Contains(w.Message, "bogus_arg") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a warning for the bogus port binding; got %+v", warns)
	}
}

func TestPortize_OnlyWiresFromAncestors(t *testing.T) {
	// "use" references "later" which is produced by a node that runs AFTER it —
	// not an ancestor — so it must NOT be wired (no reordering, no false data dep).
	d := &Draft{Name: "X", Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "use", Kind: "tool", Tool: "use", Output: "u", Input: `{"x":"{{ .later }}"}`},
			{ID: "after", Kind: "tool", Tool: "mk", Output: "later", Input: `{"t":"t"}`},
		},
		Edges: []sdkr.FlowEdge{{From: "use", To: "after"}},
		Entry: "use",
	}}
	if n := PortizeHandoffs(d, Catalog{}); n != 0 {
		t.Fatalf("must not wire from a non-ancestor; got %d", n)
	}
}
