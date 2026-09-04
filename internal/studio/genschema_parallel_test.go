package studio

import (
	"strings"
	"testing"
)

// The generation schema must allow every kind the engine can execute. Providers
// that enforce enums natively (Gemini responseSchema, Anthropic forced-tool
// input_schema) make an omission structural: the builder cannot emit the node
// at all, so every generated graph came out serial regardless of how
// independent the steps were.
func TestSchemaAllowsEveryExecutableNodeKind(t *testing.T) {
	var kinds []string
	for _, k := range ValidNodeKinds {
		kinds = append(kinds, k)
	}
	joined := strings.Join(kinds, ",")
	for _, want := range []string{"tool", "agent", "python", "llm", "branch", "parallel"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ValidNodeKinds is missing %q (have %v)", want, kinds)
		}
	}

	// And the enum the model actually receives must match.
	schema := DraftSchema()
	found := findKindEnum(schema)
	if found == nil {
		t.Fatal("could not locate node.kind enum in DraftSchema()")
	}
	var got []string
	for _, v := range found {
		if s, ok := v.(string); ok {
			got = append(got, s)
		}
	}
	if len(got) != len(ValidNodeKinds) {
		t.Errorf("schema enum %v does not match ValidNodeKinds %v", got, ValidNodeKinds)
	}
	if !strings.Contains(strings.Join(got, ","), "parallel") {
		t.Error("schema enum does not permit a parallel node")
	}
}

// findKindEnum walks the schema for the node "kind" property's enum, without
// assuming the exact nesting — the schema shape is allowed to evolve.
func findKindEnum(node any) []any {
	m, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	if props, ok := m["properties"].(map[string]any); ok {
		if kind, ok := props["kind"].(map[string]any); ok {
			if e, ok := kind["enum"].([]any); ok {
				return e
			}
			if e, ok := kind["enum"].([]string); ok {
				out := make([]any, 0, len(e))
				for _, s := range e {
					out = append(out, s)
				}
				return out
			}
		}
	}
	for _, v := range m {
		if e := findKindEnum(v); e != nil {
			return e
		}
		if arr, ok := v.([]any); ok {
			for _, item := range arr {
				if e := findKindEnum(item); e != nil {
					return e
				}
			}
		}
	}
	return nil
}

// The model also has to be TOLD the kind exists. Allowing it in the schema only
// stops a rejection; a builder that has never seen "parallel" keeps writing
// independent work as a serial chain.
func TestCompilePromptTeachesParallel(t *testing.T) {
	prompt := BuildPrompt("fetch three sources and combine them", Catalog{
		Tools: []string{"web_search", "fetch_url"},
	}, nil)
	low := strings.ToLower(prompt)
	if !strings.Contains(low, "parallel") {
		t.Fatal("the compile prompt never mentions parallel, so the model cannot use it")
	}
	// The join policies are the part that is impossible to guess.
	for _, want := range []string{"\"all\"", "\"any\"", "quorum", "best_effort"} {
		if !strings.Contains(low, strings.ToLower(want)) {
			t.Errorf("prompt does not explain the %s join policy", want)
		}
	}
}
