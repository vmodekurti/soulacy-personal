package studio

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestCaptureShape_ObjectKeepsFieldNames(t *testing.T) {
	raw := json.RawMessage(`{"id":"nb-123","title":"AI","extra":{"a":1}}`)
	shape := CaptureShape(raw)
	if !strings.Contains(shape, "id") || !strings.Contains(shape, "title") {
		t.Errorf("shape should keep field names, got %q", shape)
	}
}

func TestCaptureShape_TruncatesLongStrings(t *testing.T) {
	long := strings.Repeat("x", 500)
	raw := json.RawMessage(`{"blob":"` + long + `"}`)
	shape := CaptureShape(raw)
	if len(shape) > maxShapeLen+1 {
		t.Errorf("shape should be bounded, got len %d", len(shape))
	}
	if strings.Contains(shape, long) {
		t.Error("long string leaf should be truncated, not echoed in full")
	}
}

func TestCaptureShape_ArraySample(t *testing.T) {
	raw := json.RawMessage(`{"results":[{"url":"a"},{"url":"b"},{"url":"c"}]}`)
	shape := CaptureShape(raw)
	// Only the first element is kept as a representative sample.
	if strings.Count(shape, "url") != 1 {
		t.Errorf("array should be sampled to one element, got %q", shape)
	}
}

func TestCaptureShape_Empty(t *testing.T) {
	if CaptureShape(json.RawMessage(`null`)) != "" {
		t.Error("null output should yield empty shape")
	}
	if CaptureShape(json.RawMessage(``)) != "" {
		t.Error("empty output should yield empty shape")
	}
}

func TestShapesFromTrace_MapsVars(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "create", Output: "notebook"},
		{ID: "noout", Output: ""},
	}}}
	trace := []TraceEntry{
		{NodeID: "create", Output: json.RawMessage(`{"id":"nb-1"}`)},
		{NodeID: "noout", Output: json.RawMessage(`{"x":1}`)},
	}
	shapes := ShapesFromTrace(d, trace)
	if len(shapes) != 1 || shapes[0].Name != "notebook" {
		t.Fatalf("expected one shape for 'notebook', got %+v", shapes)
	}
	if !strings.Contains(shapes[0].Shape, "id") {
		t.Errorf("shape should sample the real output, got %q", shapes[0].Shape)
	}
}

// End-to-end: a dry run returns captured shapes that can ground CompileNode.
func TestTestRun_ReturnsShapes(t *testing.T) {
	d := Draft{
		Name:    "shape demo",
		Trigger: Trigger{Type: "manual"},
		Flow: Flow{
			Nodes: []sdkr.FlowNode{
				{ID: "a", Kind: "tool", Tool: "web_search", Input: `{"query":"x"}`, Output: "results"},
			},
			Entry: "a",
		},
	}
	res, err := TestRun(context.Background(), d, "", &TestOptions{
		Mocks: map[string]json.RawMessage{"a": json.RawMessage(`{"results":[{"url":"u","title":"t"}]}`)},
	})
	if err != nil {
		t.Fatalf("test run: %v", err)
	}
	if len(res.Shapes) == 0 {
		t.Fatal("expected captured shapes from the run")
	}
	if res.Shapes[0].Name != "results" || !strings.Contains(res.Shapes[0].Shape, "url") {
		t.Errorf("captured shape should reflect the mocked real output, got %+v", res.Shapes)
	}
}
