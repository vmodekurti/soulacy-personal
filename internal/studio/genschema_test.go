package studio

import (
	"context"
	"testing"
)

// schemaFake records whether the schema-constrained path was taken and captures
// the schema it was handed.
type schemaFake struct {
	plainCalled  bool
	schemaCalled bool
	gotSchema    map[string]any
	reply        string
}

func (f *schemaFake) Complete(ctx context.Context, prompt string) (string, error) {
	f.plainCalled = true
	return f.reply, nil
}

func (f *schemaFake) CompleteSchema(ctx context.Context, prompt string, schema map[string]any) (string, error) {
	f.schemaCalled = true
	f.gotSchema = schema
	return f.reply, nil
}

// plainFake supports only Complete (no schema constraint).
type plainFake struct {
	called bool
	reply  string
}

func (f *plainFake) Complete(ctx context.Context, prompt string) (string, error) {
	f.called = true
	return f.reply, nil
}

func TestCompleteDraft_PrefersSchemaWhenSupported(t *testing.T) {
	f := &schemaFake{reply: "{}"}
	if _, err := completeDraft(context.Background(), f, "hi"); err != nil {
		t.Fatal(err)
	}
	if !f.schemaCalled || f.plainCalled {
		t.Fatalf("expected schema path, got schemaCalled=%v plainCalled=%v", f.schemaCalled, f.plainCalled)
	}
	if f.gotSchema == nil {
		t.Fatal("schema was not passed through")
	}
}

func TestCompleteDraft_FallsBackToComplete(t *testing.T) {
	f := &plainFake{reply: "{}"}
	if _, err := completeDraft(context.Background(), f, "hi"); err != nil {
		t.Fatal(err)
	}
	if !f.called {
		t.Fatal("expected plain Complete to be called")
	}
}

// The schema must pin node.kind to exactly the engine's valid set — this is the
// constraint that stops the model inventing "start"/"end"/"sqlquery" nodes.
func TestDraftSchema_NodeKindEnum(t *testing.T) {
	s := DraftSchema()
	flow := s["properties"].(map[string]any)["flow"].(map[string]any)
	nodes := flow["properties"].(map[string]any)["nodes"].(map[string]any)
	item := nodes["items"].(map[string]any)
	kind := item["properties"].(map[string]any)["kind"].(map[string]any)
	enum, ok := kind["enum"].([]any)
	if !ok {
		t.Fatalf("kind has no enum: %#v", kind)
	}
	got := map[string]bool{}
	for _, v := range enum {
		got[v.(string)] = true
	}
	if len(got) != len(ValidNodeKinds) {
		t.Fatalf("enum size = %d, want %d (%v)", len(got), len(ValidNodeKinds), enum)
	}
	for _, k := range ValidNodeKinds {
		if !got[k] {
			t.Errorf("kind enum missing %q", k)
		}
	}
	// A hallucinated kind must NOT be in the allowed set.
	if got["start"] {
		t.Error("kind enum must not allow 'start'")
	}
}

// End-to-end: a schema-generated draft flows through the normal pipeline. This
// proves the schema path integrates with ParseDraft/normalize/validate.
func TestCompile_SchemaPathProducesValidDraft(t *testing.T) {
	raw := `{"name":"Echo","trigger":{"type":"channel"},"flow":{
		"nodes":[{"id":"a","kind":"agent","agent":"helper","input":"{{ .trigger.text }}","output":"reply"}],
		"edges":[{"from":"a","to":"end"}],"entry":"a"}}`
	f := &schemaFake{reply: raw}
	res, err := Compile(context.Background(), f, "echo the user", Catalog{}, nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !f.schemaCalled {
		t.Fatal("expected schema-constrained generation")
	}
	if got := NormalizeAndCheck(raw); !got.Valid {
		t.Fatalf("schema-generated draft should validate, errors: %v", got.Errors)
	}
	if res.Workflow.Name != "Echo" {
		t.Fatalf("draft name = %q", res.Workflow.Name)
	}
}
