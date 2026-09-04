package reasoning

import (
	"strings"
	"testing"
	"text/template"
)

func TestTmplFromJSON(t *testing.T) {
	// A JSON string is parsed and re-emitted as valid JSON.
	if got, err := tmplFromJSON(`{"a":1,"b":[2,3]}`); err != nil || got != `{"a":1,"b":[2,3]}` {
		t.Fatalf("json string: got %q err %v", got, err)
	}
	// An already-structured value is emitted as JSON.
	if got, err := tmplFromJSON(map[string]any{"x": "y"}); err != nil || got != `{"x":"y"}` {
		t.Fatalf("structured: got %q err %v", got, err)
	}
	// A non-JSON string becomes a JSON-quoted string (still valid JSON).
	if got, err := tmplFromJSON("hello"); err != nil || got != `"hello"` {
		t.Fatalf("plain string: got %q err %v", got, err)
	}
}

// The exact blocker from the field: a generated template using fromJson must now
// parse and render instead of failing with "function fromJson not defined".
func TestFlowTemplate_FromJsonRenders(t *testing.T) {
	tmpl, err := template.New("").Funcs(flowTemplateFuncs).Parse(`{"data": {{ fromJson .raw }}}`)
	if err != nil {
		t.Fatalf("template with fromJson should parse, got: %v", err)
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, map[string]any{"raw": map[string]any{"price": 123}}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if sb.String() != `{"data": {"price":123}}` {
		t.Fatalf("rendered = %q", sb.String())
	}
}
