package reasoning

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// wire builds a two-node spec joined by one typed wire.
func wire(out, in sdkr.FlowPort, mutate ...func(*sdkr.FlowSpec)) sdkr.FlowSpec {
	spec := sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "prod", Tool: "t", Output: "res", Outputs: []sdkr.FlowPort{out}},
			{ID: "cons", Tool: "t", Inputs: []sdkr.FlowPort{in}},
		},
		Edges: []sdkr.FlowEdge{{From: "prod", To: "cons", FromPort: out.Name, ToPort: in.Name}},
	}
	for _, m := range mutate {
		m(&spec)
	}
	return spec
}

func TestPortContracts_BackwardCompatible(t *testing.T) {
	// The whole existing corpus declares ports with, at most, a free-text type.
	// None of it may start failing.
	specs := []sdkr.FlowSpec{
		// No declared ports at all — the overwhelmingly common case.
		{Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t", Output: "x"}, {ID: "b", Tool: "t"}},
			Edges: []sdkr.FlowEdge{{From: "a", To: "b"}}},
		// Ports declared but untyped.
		wire(sdkr.FlowPort{Name: "result"}, sdkr.FlowPort{Name: "result"}),
		// One end typed, the other silent → no guarantee to violate.
		wire(sdkr.FlowPort{Name: "r", Type: "array"}, sdkr.FlowPort{Name: "r"}),
		wire(sdkr.FlowPort{Name: "r"}, sdkr.FlowPort{Name: "r", Type: "string"}),
		// Unrecognised type labels must never fail a build.
		wire(sdkr.FlowPort{Name: "r", Type: "NotebookRef"}, sdkr.FlowPort{Name: "r", Type: "string"}),
		// "json"/"any" explicitly mean "shape varies".
		wire(sdkr.FlowPort{Name: "r", Type: "json"}, sdkr.FlowPort{Name: "r", Type: "object"}),
	}
	for i, spec := range specs {
		if _, err := CompileFlow(spec); err != nil {
			t.Errorf("spec %d must still compile: %v", i, err)
		}
	}
}

func TestPortContracts_TypeMismatch(t *testing.T) {
	// The exact bug class this exists for: an array wired into a string, which
	// used to compile and then stringify as "[map[...] map[...]]" at run time.
	_, err := CompileFlow(wire(
		sdkr.FlowPort{Name: "articles", Type: "array"},
		sdkr.FlowPort{Name: "articles", Type: "string"},
	))
	if err == nil {
		t.Fatal("array→string must be refused")
	}
	for _, want := range []string{"type mismatch", "array", "string", "adapter"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
	// The error must name BOTH ends, so the author knows which wire is wrong.
	if !strings.Contains(err.Error(), "prod") || !strings.Contains(err.Error(), "cons") {
		t.Errorf("error should name both nodes: %v", err)
	}

	// object→array and string→object are equally real reshapes.
	for _, pair := range [][2]string{{"object", "array"}, {"string", "object"}} {
		if _, err := CompileFlow(wire(
			sdkr.FlowPort{Name: "v", Type: pair[0]},
			sdkr.FlowPort{Name: "v", Type: pair[1]},
		)); err == nil {
			t.Errorf("%s→%s must be refused", pair[0], pair[1])
		}
	}

	// number→string is the one widening every JSON encoder does identically.
	if _, err := CompileFlow(wire(
		sdkr.FlowPort{Name: "v", Type: "number"},
		sdkr.FlowPort{Name: "v", Type: "string"},
	)); err != nil {
		t.Errorf("number→string is a safe widening: %v", err)
	}
}

func TestPortContracts_AdapterIsTheEscapeHatch(t *testing.T) {
	// Conversions are not forbidden — implicit ones are. Declaring the consumer
	// an adapter is the author taking responsibility for the reshape.
	if _, err := CompileFlow(wire(
		sdkr.FlowPort{Name: "v", Type: "array"},
		sdkr.FlowPort{Name: "v", Type: "string", Adapter: true},
	)); err != nil {
		t.Fatalf("an adapter consumer must accept any producer: %v", err)
	}
}

func TestPortContracts_Cardinality(t *testing.T) {
	// many→one without an aggregation step: the fan-out contract violation.
	_, err := CompileFlow(wire(
		sdkr.FlowPort{Name: "urls", Type: "string", Cardinality: sdkr.CardinalityMany},
		sdkr.FlowPort{Name: "urls", Type: "string", Cardinality: sdkr.CardinalityOne},
	))
	if err == nil {
		t.Fatal("many→one must be refused")
	}
	if !strings.Contains(err.Error(), "cardinality") {
		t.Errorf("should be reported as a cardinality problem, not a type one: %v", err)
	}
	// The remedy must mention for_each, which is the idiomatic fix here.
	if !strings.Contains(err.Error(), "for_each") {
		t.Errorf("fix should point at for_each: %v", err)
	}

	// An array-ish TYPE implies many, so the two notations agree rather than
	// contradict: "string[]" producer into a "one" consumer is still many→one.
	if _, err := CompileFlow(wire(
		sdkr.FlowPort{Name: "urls", Type: "string[]"},
		sdkr.FlowPort{Name: "urls", Type: "string", Cardinality: sdkr.CardinalityOne},
	)); err == nil {
		t.Error("string[] producer implies many; many→one must be refused")
	}

	// A for_each consumer legitimately receives a many producer — the loop IS
	// the aggregation step, so this must compile.
	spec := wire(
		sdkr.FlowPort{Name: "urls", Type: "string", Cardinality: sdkr.CardinalityMany},
		sdkr.FlowPort{Name: "urls", Type: "string", Cardinality: sdkr.CardinalityOne},
		func(s *sdkr.FlowSpec) {
			s.Nodes[1].ForEach = "{{ toJson .res }}"
			s.Nodes[1].ItemVar = "item"
		},
	)
	if _, err := CompileFlow(spec); err != nil {
		t.Errorf("a for_each consumer must accept a many producer: %v", err)
	}
}

func TestPortContracts_Nullability(t *testing.T) {
	_, err := CompileFlow(wire(
		sdkr.FlowPort{Name: "id", Type: "string", Nullable: true},
		sdkr.FlowPort{Name: "id", Type: "string"},
	))
	if err == nil {
		t.Fatal("nullable→non-nullable must be refused")
	}
	if !strings.Contains(err.Error(), "nullability") {
		t.Errorf("should be reported as a nullability problem: %v", err)
	}
	// Marking the consumer nullable accepts it.
	if _, err := CompileFlow(wire(
		sdkr.FlowPort{Name: "id", Type: "string", Nullable: true},
		sdkr.FlowPort{Name: "id", Type: "string", Nullable: true},
	)); err != nil {
		t.Errorf("nullable→nullable is fine: %v", err)
	}
	// An undeclared consumer makes no promise, so there is nothing to violate.
	if _, err := CompileFlow(wire(
		sdkr.FlowPort{Name: "id", Nullable: true},
		sdkr.FlowPort{Name: "id"},
	)); err != nil {
		t.Errorf("an undeclared consumer must stay permissive: %v", err)
	}
}

func TestPortContracts_RequiredInputs(t *testing.T) {
	// A required port with nothing feeding it fails at COMPILE time rather than
	// binding a zero value and failing downstream.
	spec := sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Tool: "t", Output: "res"},
			{ID: "b", Tool: "t", Inputs: []sdkr.FlowPort{{Name: "notebook_id", Type: "string", Required: true}}},
		},
		Edges: []sdkr.FlowEdge{{From: "a", To: "b"}}, // untyped handoff — port unwired
	}
	_, err := CompileFlow(spec)
	if err == nil {
		t.Fatal("an unwired required input must be refused")
	}
	for _, want := range []string{"notebook_id", "requires input port"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}

	// Wiring it satisfies the requirement.
	spec.Nodes[0].Outputs = []sdkr.FlowPort{{Name: "notebook_id", Type: "string"}}
	spec.Edges = []sdkr.FlowEdge{{From: "a", To: "b", FromPort: "notebook_id", ToPort: "notebook_id"}}
	if _, err := CompileFlow(spec); err != nil {
		t.Errorf("a wired required input must compile: %v", err)
	}

	// So does supplying it as a constant in the node's own input.
	spec.Edges = []sdkr.FlowEdge{{From: "a", To: "b"}}
	spec.Nodes[1].Input = `{"notebook_id": "nb-123"}`
	if _, err := CompileFlow(spec); err != nil {
		t.Errorf("a constant in the node input must satisfy a required port: %v", err)
	}
}
