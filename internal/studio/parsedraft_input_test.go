package studio

import (
	"encoding/json"
	"strings"
	"testing"
)

// Reproduces the live failure: a model emits a node "input" as a JSON OBJECT
// instead of the schema's stringified JSON. ParseDraft must coerce it to a
// string rather than rejecting the whole draft.
func TestParseDraft_CoercesObjectInputToString(t *testing.T) {
	raw := `{
	  "name": "Daily AI Podcast",
	  "trigger": {"type":"schedule","config":{"cron":"0 7 * * *"}},
	  "flow": {
	    "nodes": [
	      {"id":"search","kind":"tool","tool":"web_search",
	       "input": {"query":"latest AI news","num_results": 10},
	       "output":"results"},
	      {"id":"note","kind":"agent","agent":"notifier",
	       "input": "Send the digest",
	       "output":"sent"}
	    ],
	    "edges": [{"from":"search","to":"note"},{"from":"note","to":"end"}],
	    "entry":"search"
	  }
	}`

	d, err := ParseDraft(raw)
	if err != nil {
		t.Fatalf("ParseDraft should tolerate an object-typed input, got: %v", err)
	}
	if len(d.Flow.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(d.Flow.Nodes))
	}

	// The object input is now a JSON string carrying the same data.
	got := d.Flow.Nodes[0].Input
	if !strings.HasPrefix(strings.TrimSpace(got), "{") {
		t.Fatalf("node input should be stringified JSON, got %q", got)
	}
	var obj map[string]any
	if uerr := json.Unmarshal([]byte(got), &obj); uerr != nil {
		t.Fatalf("coerced input is not valid JSON: %v (%q)", uerr, got)
	}
	if obj["query"] != "latest AI news" {
		t.Errorf("coerced input lost data: %v", obj)
	}
	if n, _ := obj["num_results"].(float64); int(n) != 10 {
		t.Errorf("coerced input lost num_results: %v", obj)
	}

	// A string input is untouched.
	if d.Flow.Nodes[1].Input != "Send the digest" {
		t.Errorf("string input should be preserved, got %q", d.Flow.Nodes[1].Input)
	}
}

// A node "input" emitted as an array is also coerced (some models wrap args).
func TestParseDraft_CoercesArrayInput(t *testing.T) {
	raw := `{"name":"x","trigger":{"type":"manual"},"flow":{"nodes":[
	  {"id":"a","kind":"python","input":["one","two"],"output":"o"}
	],"entry":"a"}}`
	d, err := ParseDraft(raw)
	if err != nil {
		t.Fatalf("array input should be coerced, got: %v", err)
	}
	if d.Flow.Nodes[0].Input != `["one","two"]` {
		t.Errorf("expected stringified array, got %q", d.Flow.Nodes[0].Input)
	}
}

// coerceNodeInputs must be a safe no-op for already-correct drafts and for junk.
func TestCoerceNodeInputs_NoopPaths(t *testing.T) {
	good := `{"flow":{"nodes":[{"id":"a","input":"already a string"}]}}`
	if coerceNodeInputs(good) != good {
		t.Error("a string input should be left byte-for-byte unchanged")
	}
	if coerceNodeInputs("not json") != "not json" {
		t.Error("non-JSON should pass through untouched")
	}
	noFlow := `{"name":"x"}`
	if coerceNodeInputs(noFlow) != noFlow {
		t.Error("missing flow should pass through untouched")
	}
}
