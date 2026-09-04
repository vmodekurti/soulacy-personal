package studio

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// M5 (Stories S5.2/S5.3): per-node mock overrides + assertions evaluated
// against the dry run. The canonical draft is fetch (tool) → summarize (agent),
// whose summarize output becomes the final result.

// ── (a) mock override flows into the trace and the result ────────────────────

func TestTestRun_Mock_OverridesNodeOutputAndResult(t *testing.T) {
	draft := canonicalDraft(t)
	opts := &TestOptions{
		Mocks: map[string]json.RawMessage{
			// Override the final (agent) node so it also drives the result.
			"summarize": json.RawMessage(`"PINNED SUMMARY"`),
		},
	}
	res, err := TestRun(context.Background(), draft, "go", opts)
	if err != nil {
		t.Fatalf("TestRun: %v", err)
	}

	// summarize trace entry reflects the mock and is flagged.
	var summ *TraceEntry
	for i := range res.Trace {
		if res.Trace[i].NodeID == "summarize" {
			summ = &res.Trace[i]
		}
	}
	if summ == nil {
		t.Fatalf("no summarize trace entry: %+v", res.Trace)
	}
	if !summ.Mocked {
		t.Errorf("summarize entry should be marked Mocked: %+v", summ)
	}
	if string(summ.Output) != `"PINNED SUMMARY"` {
		t.Errorf("summarize output = %s, want pinned mock", summ.Output)
	}

	// The downstream result (last node's output) reflects the mock.
	if string(res.Result) != `"PINNED SUMMARY"` {
		t.Errorf("result = %s, want pinned mock", res.Result)
	}

	// The un-mocked node is NOT flagged.
	for _, e := range res.Trace {
		if e.NodeID == "fetch" && e.Mocked {
			t.Errorf("fetch should not be mocked: %+v", e)
		}
	}
}

func TestTestRun_Mock_UnknownNodeID_Warns(t *testing.T) {
	draft := canonicalDraft(t)
	opts := &TestOptions{
		Mocks: map[string]json.RawMessage{"ghost": json.RawMessage(`"x"`)},
	}
	res, err := TestRun(context.Background(), draft, "go", opts)
	if err != nil {
		t.Fatalf("TestRun: %v", err)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "ghost") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a warning for unknown mock node id, got %v", res.Warnings)
	}
}

// ── (b) "contains" assertion on a node: pass and fail ────────────────────────

func TestTestRun_Assertion_Contains_PassAndFail(t *testing.T) {
	draft := canonicalDraft(t)

	// Pass: the fetch tool stub contains the tool name "http_get".
	pass := &TestOptions{Assertions: []Assertion{
		{Target: "fetch", Op: "contains", Value: "http_get"},
	}}
	res, err := TestRun(context.Background(), draft, "go", pass)
	if err != nil {
		t.Fatalf("TestRun: %v", err)
	}
	if len(res.Assertions) != 1 || !res.Assertions[0].Pass {
		t.Fatalf("expected contains pass, got %+v", res.Assertions)
	}
	if !res.Passed {
		t.Errorf("Passed should be true when the only assertion passes")
	}

	// Fail: the fetch output does not contain "nope" — Detail must be useful.
	fail := &TestOptions{Assertions: []Assertion{
		{Target: "fetch", Op: "contains", Value: "nope"},
	}}
	res, err = TestRun(context.Background(), draft, "go", fail)
	if err != nil {
		t.Fatalf("TestRun: %v", err)
	}
	if len(res.Assertions) != 1 || res.Assertions[0].Pass {
		t.Fatalf("expected contains fail, got %+v", res.Assertions)
	}
	d := res.Assertions[0].Detail
	if !strings.Contains(d, "nope") || !strings.Contains(d, "does not contain") {
		t.Errorf("Detail not useful: %q", d)
	}
	if res.Passed {
		t.Errorf("Passed should be false when an assertion fails")
	}
}

// ── (c) "result" assertion targets the final flow output ─────────────────────

func TestTestRun_Assertion_ResultTarget(t *testing.T) {
	draft := canonicalDraft(t)
	opts := &TestOptions{
		Mocks: map[string]json.RawMessage{
			"summarize": json.RawMessage(`"digest ready"`),
		},
		Assertions: []Assertion{
			{Target: "result", Op: "equals", Value: "digest ready"},
			{Target: "result", Op: "exists"},
		},
	}
	res, err := TestRun(context.Background(), draft, "go", opts)
	if err != nil {
		t.Fatalf("TestRun: %v", err)
	}
	if !res.Passed {
		t.Fatalf("expected result assertions to pass: %+v", res.Assertions)
	}
	for _, a := range res.Assertions {
		if !a.Pass {
			t.Errorf("assertion %+v should pass", a)
		}
	}
}

// ── (d) Passed aggregates across assertions ──────────────────────────────────

func TestTestRun_Passed_AggregatesAcrossAssertions(t *testing.T) {
	draft := canonicalDraft(t)
	opts := &TestOptions{Assertions: []Assertion{
		{Target: "fetch", Op: "exists"},                 // pass
		{Target: "fetch", Op: "contains", Value: "zzz"}, // fail
		{Target: "summarize", Op: "exists"},             // pass
	}}
	res, err := TestRun(context.Background(), draft, "go", opts)
	if err != nil {
		t.Fatalf("TestRun: %v", err)
	}
	if res.Passed {
		t.Errorf("one failing assertion must make Passed=false: %+v", res.Assertions)
	}
	if len(res.Assertions) != 3 {
		t.Fatalf("expected 3 assertion results, got %d", len(res.Assertions))
	}
}

// Assertion against a node that never executed fails with a clear detail.
func TestTestRun_Assertion_MissingTarget(t *testing.T) {
	draft := canonicalDraft(t)
	opts := &TestOptions{Assertions: []Assertion{
		{Target: "ghost", Op: "exists"},
	}}
	res, err := TestRun(context.Background(), draft, "go", opts)
	if err != nil {
		t.Fatalf("TestRun: %v", err)
	}
	if res.Assertions[0].Pass {
		t.Errorf("missing-target assertion should fail")
	}
	if !strings.Contains(res.Assertions[0].Detail, "did not execute") {
		t.Errorf("expected did-not-execute detail, got %q", res.Assertions[0].Detail)
	}
}

// ── (e) Mode=="live" runs nothing real and returns the not-supported note ────

func TestTestRun_LiveMode_NotSupported_NoExecution(t *testing.T) {
	draft := canonicalDraft(t)
	opts := &TestOptions{
		Mode: "live",
		// Even with assertions, nothing real runs and the trace is empty.
		Assertions: []Assertion{{Target: "result", Op: "exists"}},
	}
	res, err := TestRun(context.Background(), draft, "go", opts)
	if err != nil {
		t.Fatalf("TestRun(live): %v", err)
	}
	if res.Mode != "live" {
		t.Errorf("mode echo = %q, want live", res.Mode)
	}
	if len(res.Trace) != 0 {
		t.Errorf("live mode must not execute any node, got trace %+v", res.Trace)
	}
	if len(res.Result) != 0 {
		t.Errorf("live mode must produce no result, got %s", res.Result)
	}
	noted := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "not supported") && strings.Contains(w, "save") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("expected a clear not-supported note, got %v", res.Warnings)
	}
}

// An invalid flow is an error even in live mode (the draft is malformed).
func TestTestRun_LiveMode_InvalidFlowStillErrors(t *testing.T) {
	bad := Draft{Name: "Bad", Flow: Flow{
		Nodes: canonicalDraft(t).Flow.Nodes[:1],
		Entry: "ghost",
	}}
	if _, err := TestRun(context.Background(), bad, "x", &TestOptions{Mode: "live"}); err == nil {
		t.Errorf("expected an error for an invalid flow in live mode")
	}
}

// ── (f) backward compatibility: no options & dry defaults ────────────────────

func TestTestRun_NoOptions_BackwardCompatible(t *testing.T) {
	draft := canonicalDraft(t)

	// nil opts behaves like the original dry run.
	res, err := TestRun(context.Background(), draft, "go", nil)
	if err != nil {
		t.Fatalf("TestRun(nil opts): %v", err)
	}
	if len(res.Trace) != 2 {
		t.Errorf("expected 2 trace entries, got %d", len(res.Trace))
	}
	if res.Mode != "dry" {
		t.Errorf("default mode = %q, want dry", res.Mode)
	}
	if !res.Passed {
		t.Errorf("no assertions should aggregate to Passed=true")
	}
	if len(res.Assertions) != 0 {
		t.Errorf("expected no assertion results, got %+v", res.Assertions)
	}
	for _, e := range res.Trace {
		if e.Mocked {
			t.Errorf("no mocks: entry should not be flagged: %+v", e)
		}
	}

	// Empty opts with empty Mode also defaults to dry.
	res2, err := TestRun(context.Background(), draft, "go", &TestOptions{})
	if err != nil {
		t.Fatalf("TestRun(empty opts): %v", err)
	}
	if res2.Mode != "dry" {
		t.Errorf("empty-mode default = %q, want dry", res2.Mode)
	}
}

// ── stringifyOutput is deterministic over JSON shapes ────────────────────────

func TestStringifyOutput_Deterministic(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`"hello"`, "hello"},                    // string unquoted
		{`{ "b": 1, "a": 2 }`, `{"b":1,"a":2}`}, // compacted, key order preserved
		{`[1,  2,3]`, `[1,2,3]`},                // whitespace stripped
		{``, ""},                                // empty
		{`42`, "42"},                            // number
	}
	for _, c := range cases {
		if got := stringifyOutput(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("stringifyOutput(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// EvaluateAssertions is pure: same inputs → same outputs, no run needed.
func TestEvaluateAssertions_Pure(t *testing.T) {
	trace := []TraceEntry{
		{NodeID: "n1", Output: json.RawMessage(`"abc"`)},
	}
	result := json.RawMessage(`{"ok":true}`)
	asserts := []Assertion{
		{Target: "n1", Op: "contains", Value: "b"},
		{Target: "result", Op: "contains", Value: `"ok":true`},
		{Target: "n1", Op: "equals", Value: "abc"},
	}
	got := EvaluateAssertions(asserts, trace, result)
	if len(got) != 3 {
		t.Fatalf("want 3 results, got %d", len(got))
	}
	for _, a := range got {
		if !a.Pass {
			t.Errorf("assertion %+v should pass", a)
		}
	}
}
