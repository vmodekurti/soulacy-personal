package studio

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		err  string
		want RepairClass
	}{
		{"", RepairNone},
		{`template: t:1: function "fromJson" not defined`, RepairTemplateError},
		{`template: t:1: executing "t" at <.source_pack.text>: can't evaluate field text in type interface {}`, RepairShapeDrift},
		{`can't evaluate field results in type string`, RepairShapeDrift},
		{`Traceback ... KeyError: 'results'`, RepairShapeDrift},
		{`range can't iterate over abc`, RepairShapeDrift},
		{`web_search: 401 Unauthorized`, RepairToolFailure},
		{`dial tcp: no such host`, RepairToolFailure},
	}
	for _, c := range cases {
		if got := Classify(LiveNodeRun{Error: c.err}); got != c.want {
			t.Errorf("Classify(%q) = %s, want %s", c.err, got, c.want)
		}
	}
}

func TestTemplateFieldChains(t *testing.T) {
	got := templateFieldChains(`{{ range .search.results }}{{ .title }}{{ end }} {{ toJson .meta }}`)
	joined := map[string]bool{}
	for _, ch := range got {
		joined[strings.Join(ch, ".")] = true
	}
	if !joined["search.results"] {
		t.Errorf("expected search.results chain, got %v", got)
	}
	if !joined["meta"] {
		t.Errorf("expected meta chain, got %v", got)
	}
}

// Deterministic remap: node reads .search.results but the real output has the
// list under .items → propose a remap, no LLM.
func TestProposeAdapter_KeyRemap(t *testing.T) {
	producer := LiveNodeRun{
		NodeID: "search", OutputVar: "search",
		Output: json.RawMessage(`{"items":[{"title":"a"}],"meta":{"n":1}}`),
	}
	target := LiveNodeRun{
		NodeID: "fmt", Kind: "agent",
		Input: `{{ toJson .search.results }}`,
		Error: `can't evaluate field results`,
	}
	d := Diagnose([]LiveNodeRun{producer, target}, target)
	if d.ArrayKey != "items" {
		t.Fatalf("array key = %q, want items (observed %v)", d.ArrayKey, d.ObservedKeys)
	}
	p, ok := ProposeAdapter(target, d)
	if !ok {
		t.Fatal("expected a deterministic remap proposal")
	}
	if !strings.Contains(p.New, ".search.items") || strings.Contains(p.New, ".search.results") {
		t.Fatalf("remap not applied: %q", p.New)
	}
	if !p.Auto {
		t.Error("deterministic remap should be Auto")
	}
}

// Deterministic string-wrapped JSON: producer returned a JSON *string*; propose
// wrapping references in fromJson.
func TestProposeAdapter_StringWrapped(t *testing.T) {
	producer := LiveNodeRun{
		NodeID: "call", OutputVar: "resp",
		Output: json.RawMessage(`"{\"price\": 123}"`), // a JSON string that wraps JSON
	}
	target := LiveNodeRun{
		NodeID: "use", Kind: "agent",
		Input: `The price is {{ .resp.price }}`,
		Error: `can't evaluate field price in type string`,
	}
	d := Diagnose([]LiveNodeRun{producer, target}, target)
	if !d.StringWrapped {
		t.Fatal("expected StringWrapped diagnosis")
	}
	p, ok := ProposeAdapter(target, d)
	if !ok {
		t.Fatal("expected a fromJson adapter proposal")
	}
	if !strings.Contains(p.New, "fromJson .resp") {
		t.Fatalf("fromJson not injected: %q", p.New)
	}
}

// When no deterministic fix exists, the orchestrator falls back to the LLM and
// returns its rewrite as a non-auto proposal.
type repairFake struct{ reply string }

func (f repairFake) Complete(ctx context.Context, prompt string) (string, error) {
	return f.reply, nil
}

func TestProposeLiveRepairs_LLMFallback(t *testing.T) {
	// A shape drift with no producer output in the trace → deterministic layer
	// can't remap, so the LLM path is used.
	target := LiveNodeRun{
		NodeID: "x", Kind: "agent",
		Input: `{{ .weird.deeply.nested }}`,
		Error: `can't evaluate field nested`,
	}
	llm := repairFake{reply: `{"field":"input","value":"{{ toJson .weird }}"}`}
	props := ProposeLiveRepairs(context.Background(), llm, Draft{}, []LiveNodeRun{target})
	if len(props) != 1 {
		t.Fatalf("want 1 proposal, got %d", len(props))
	}
	if props[0].New != "{{ toJson .weird }}" || props[0].Auto {
		t.Fatalf("unexpected proposal: %+v", props[0])
	}
}

// Soft failure: the node ran green (no Go error) but its OUTPUT reports a json
// decode error — exactly the parse_stock_data case where the upstream tool
// returned an HTTP response with headers before the JSON body. Classify must
// catch it, and the python node gets an LLM code-repair carrying the real input.
func TestSoftFailure_PythonOutputError(t *testing.T) {
	realInput := `{"stock_data": "Status: 200 OK\nContent-Type: application/json\n\n{\"chart\":{\"result\":[{\"meta\":{\"regularMarketPrice\":314.48}}]}}"}`
	py := LiveNodeRun{
		NodeID: "parse_stock_data", Kind: "python",
		Input:  realInput,
		Output: json.RawMessage(`{"current_price":"N/A","chart_data":[],"ticker":"","error":"Expecting value: line 1 column 1 (char 0)"}`),
		// note: NO run.Error — it "succeeded"
	}
	if got := Classify(py); got != RepairShapeDrift {
		t.Fatalf("soft failure should classify as shape_drift, got %s", got)
	}
	draft := Draft{}
	draft.Flow.Nodes = []sdkr.FlowNode{{
		ID: "parse_stock_data", Kind: sdkr.FlowNodePython,
		Code: "import json\ndef run(inputs):\n    data = json.loads(inputs['stock_data'])\n    return data",
	}}
	llm := repairFake{reply: `{"field":"code","value":"import json\ndef run(inputs):\n    raw = inputs.get('stock_data','')\n    i = raw.find('{')\n    data = json.loads(raw[i:]) if i>=0 else {}\n    return data"}`}
	props := ProposeLiveRepairs(context.Background(), llm, draft, []LiveNodeRun{py})
	if len(props) != 1 {
		t.Fatalf("want 1 proposal, got %d", len(props))
	}
	if props[0].Field != "code" || !strings.Contains(props[0].New, "find('{')") {
		t.Fatalf("expected a defensive code rewrite, got %+v", props[0])
	}
	if !strings.Contains(props[0].Old, "json.loads(inputs['stock_data'])") {
		t.Fatalf("proposal should carry the OLD code for the diff, got old=%q", props[0].Old)
	}
}

func TestOutputErrorText(t *testing.T) {
	if got := outputErrorText(json.RawMessage(`{"error":"boom"}`)); got != "boom" {
		t.Fatalf("got %q", got)
	}
	if got := outputErrorText(json.RawMessage(`{"error":""}`)); got != "" {
		t.Fatalf("empty error should not count, got %q", got)
	}
	if got := outputErrorText(json.RawMessage(`{"ok":true}`)); got != "" {
		t.Fatalf("no error field, got %q", got)
	}
}

// tool_failure is advisory-only: reported, but never a code rewrite.
func TestProposeLiveRepairs_ToolFailureAdvisory(t *testing.T) {
	target := LiveNodeRun{NodeID: "s", Kind: "tool", Input: `{{ .q }}`, Error: "web_search: 403 forbidden"}
	props := ProposeLiveRepairs(context.Background(), repairFake{}, Draft{}, []LiveNodeRun{target})
	if len(props) != 1 || props[0].Class != RepairToolFailure || props[0].New != "" {
		t.Fatalf("expected advisory tool_failure with no rewrite, got %+v", props)
	}
}

// ApplyProposal patches the node and clears python consent on code changes.
func TestApplyProposal(t *testing.T) {
	d := Draft{}
	d.Flow.Nodes = []sdkr.FlowNode{
		{ID: "a", Kind: sdkr.FlowNodeAgent, Input: "old"},
		{ID: "p", Kind: sdkr.FlowNodePython, Code: "def run(inputs):\n    return 1", Consent: &sdkr.FlowConsent{}},
	}
	if !ApplyProposal(&d, RepairProposal{NodeID: "a", Field: "input", New: "new"}) {
		t.Fatal("apply input failed")
	}
	if d.Flow.Nodes[0].Input != "new" {
		t.Fatalf("input not patched: %q", d.Flow.Nodes[0].Input)
	}
	if !ApplyProposal(&d, RepairProposal{NodeID: "p", Field: "code", New: "def run(inputs):\n    return 2"}) {
		t.Fatal("apply code failed")
	}
	if d.Flow.Nodes[1].Consent != nil {
		t.Error("python consent should be cleared after a code change")
	}
	// Re-validate: a patched draft still normalizes+compiles.
	if !ApplyProposal(&d, RepairProposal{NodeID: "missing", Field: "input", New: "x"}) == false {
		t.Error("apply to missing node should return false")
	}
}

// redactSample caps arrays and long strings while preserving shape.
func TestRedactSample(t *testing.T) {
	raw := json.RawMessage(`{"results":[1,2,3,4,5,6,7,8],"note":"` + strings.Repeat("x", 500) + `"}`)
	out := redactSample(raw, 3, 50)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("redacted not valid json: %v", err)
	}
	arr := m["results"].([]any)
	if len(arr) != 4 { // 3 kept + 1 summary marker
		t.Fatalf("array not capped: len=%d", len(arr))
	}
	if s := m["note"].(string); len([]rune(s)) > 51 {
		t.Fatalf("string not capped: len=%d", len([]rune(s)))
	}
}
