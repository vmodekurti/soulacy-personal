package studio

import (
	"encoding/json"
	"strings"
	"testing"
)

func evalOne(t *testing.T, a Assertion, output string) AssertionResult {
	t.Helper()
	trace := []TraceEntry{{NodeID: a.Target, Output: json.RawMessage(output)}}
	return evalAssertion(a, trace, json.RawMessage(output))
}

func TestAssessAssertions_RejectsRunCompletedOnly(t *testing.T) {
	// P0-4: "an agent cannot be certified with only a run-completed assertion".
	// `exists` passes for ANY non-empty output, so a contract made only of them
	// cannot distinguish delivering the brief from producing some bytes.
	weak := AssessAssertions([]Assertion{
		{Target: "result", Op: OpExists},
		{Target: "send", Op: OpExists},
	})
	if weak.OK {
		t.Fatal("a contract of only exists assertions must not qualify")
	}
	if weak.Substantive != 0 {
		t.Errorf("exists is never substantive, got %d", weak.Substantive)
	}
	if weak.Fix == "" {
		t.Error("a rejection must come with a concrete fix")
	}

	// No assertions at all is the weaker case and must also be rejected.
	none := AssessAssertions(nil)
	if none.OK {
		t.Fatal("an empty contract must not qualify")
	}
	if !strings.Contains(strings.Join(none.Reasons, " "), "no outcome assertions") {
		t.Errorf("reason should say there are none: %v", none.Reasons)
	}

	// One substantive assertion is enough to qualify.
	ok := AssessAssertions([]Assertion{
		{Target: "result", Op: OpExists},
		{Target: "sources", Op: OpCountGTE, Value: "3"},
	})
	if !ok.OK || ok.Substantive != 1 {
		t.Fatalf("one substantive assertion should qualify: %+v", ok)
	}
}

func TestIsSubstantiveAssertion(t *testing.T) {
	substantive := []Assertion{
		{Op: OpCountGTE, Value: "3"},
		{Op: OpHasField, Value: "url"},
		{Op: OpDelivered},
		{Op: OpDestination, Value: "-100"},
		{Op: OpArtifact},
		{Op: OpNotEmpty},
		{Op: OpContains, Value: "podcast"},
	}
	for _, a := range substantive {
		if !IsSubstantiveAssertion(a) {
			t.Errorf("%+v should be substantive", a)
		}
	}
	weak := []Assertion{
		{Op: OpExists},
		{Op: OpContains, Value: ""}, // a contains with nothing expected claims nothing
		{Op: OpEquals, Value: "  "},
		{Op: "unknown_op"},
	}
	for _, a := range weak {
		if IsSubstantiveAssertion(a) {
			t.Errorf("%+v should NOT be substantive", a)
		}
	}
}

func TestStructuralOperators(t *testing.T) {
	cases := []struct {
		name    string
		a       Assertion
		output  string
		pass    bool
		outcome Outcome
	}{
		{"count_gte met", Assertion{Target: "n", Op: OpCountGTE, Value: "3"}, `{"results":[1,2,3]}`, true, OutcomeComplete},
		{"count_gte short is partial", Assertion{Target: "n", Op: OpCountGTE, Value: "3"}, `{"results":[1]}`, false, OutcomePartial},
		{"count_gte zero is empty", Assertion{Target: "n", Op: OpCountGTE, Value: "3"}, `{"results":[]}`, false, OutcomeEmpty},
		{"count_eq", Assertion{Target: "n", Op: OpCountEQ, Value: "2"}, `{"items":[1,2]}`, true, OutcomeComplete},
		{"not_empty on empty list", Assertion{Target: "n", Op: OpNotEmpty}, `[]`, false, OutcomeEmpty},
		{"not_empty on list", Assertion{Target: "n", Op: OpNotEmpty}, `[1]`, true, OutcomeComplete},
		{"has_field", Assertion{Target: "n", Op: OpHasField, Value: "notebook.id"}, `{"notebook":{"id":"x"}}`, true, OutcomeComplete},
		{"has_field missing", Assertion{Target: "n", Op: OpHasField, Value: "notebook.id"}, `{"notebook":{}}`, false, OutcomeFailed},
		{"field_equals", Assertion{Target: "n", Op: OpFieldEquals, Value: "status=success"}, `{"status":"success"}`, true, OutcomeComplete},
		{"field_equals wrong", Assertion{Target: "n", Op: OpFieldEquals, Value: "status=success"}, `{"status":"error"}`, false, OutcomeFailed},
		{"delivered", Assertion{Target: "n", Op: OpDelivered, Value: "telegram"}, `{"ok":true,"channel":"telegram","to":"-100"}`, true, OutcomeComplete},
		{"delivered wrong channel", Assertion{Target: "n", Op: OpDelivered, Value: "telegram"}, `{"ok":true,"channel":"slack"}`, false, OutcomeFailed},
		{"delivered not ok", Assertion{Target: "n", Op: OpDelivered}, `{"ok":false,"channel":"telegram"}`, false, OutcomeFailed},
		{"destination match", Assertion{Target: "n", Op: OpDestination, Value: "-100"}, `{"ok":true,"to":"-100"}`, true, OutcomeComplete},
		{"destination mismatch", Assertion{Target: "n", Op: OpDestination, Value: "-100"}, `{"ok":true,"to":"-999"}`, false, OutcomeFailed},
		{"artifact ready", Assertion{Target: "n", Op: OpArtifact}, `{"artifact_id":"a1","status":"success"}`, true, OutcomeComplete},
		{"artifact still processing is partial", Assertion{Target: "n", Op: OpArtifact}, `{"artifact_id":"a1","artifact_status":"processing"}`, false, OutcomePartial},
		{"artifact absent is empty", Assertion{Target: "n", Op: OpArtifact}, `{"status":"success"}`, false, OutcomeEmpty},
	}
	for _, tc := range cases {
		got := evalOne(t, tc.a, tc.output)
		if got.Pass != tc.pass {
			t.Errorf("%s: pass=%v want %v (%s)", tc.name, got.Pass, tc.pass, got.Detail)
		}
		if got.Outcome != tc.outcome {
			t.Errorf("%s: outcome=%q want %q (%s)", tc.name, got.Outcome, tc.outcome, got.Detail)
		}
	}
}

func TestLegacyOperatorsUnchanged(t *testing.T) {
	// The original three must behave exactly as before; only Outcome is added.
	if r := evalOne(t, Assertion{Target: "n", Op: "contains", Value: "hello"}, `"hello world"`); !r.Pass {
		t.Errorf("contains regressed: %s", r.Detail)
	}
	if r := evalOne(t, Assertion{Target: "n", Op: "equals", Value: "hi"}, `"hi"`); !r.Pass {
		t.Errorf("equals regressed: %s", r.Detail)
	}
	if r := evalOne(t, Assertion{Target: "n", Op: "exists"}, `"x"`); !r.Pass || r.Outcome != OutcomeComplete {
		t.Errorf("exists regressed: %+v", r)
	}
	// A failing exists is specifically the EMPTY case, which is what makes it
	// distinguishable from a wrong-value failure.
	if r := evalOne(t, Assertion{Target: "n", Op: "exists"}, `""`); r.Pass || r.Outcome != OutcomeEmpty {
		t.Errorf("a failing exists should classify as empty: %+v", r)
	}
	// An unknown op names the valid set rather than the original three.
	r := evalOne(t, Assertion{Target: "n", Op: "made_up"}, `"x"`)
	if r.Pass || !strings.Contains(r.Detail, OpCountGTE) {
		t.Errorf("unknown op should list supported ops: %s", r.Detail)
	}
}

func TestClassifyOutcome(t *testing.T) {
	cases := []struct {
		name string
		in   []AssertionResult
		want Outcome
	}{
		{"all pass", []AssertionResult{{Pass: true, Outcome: OutcomeComplete}}, OutcomeComplete},
		{"no assertions is empty, not complete", nil, OutcomeEmpty},
		{"empty dominates a pass", []AssertionResult{
			{Pass: true, Outcome: OutcomeComplete}, {Pass: false, Outcome: OutcomeEmpty},
		}, OutcomeEmpty},
		{"failed dominates empty", []AssertionResult{
			{Pass: false, Outcome: OutcomeEmpty}, {Pass: false, Outcome: OutcomeFailed},
		}, OutcomeFailed},
		{"mixed pass and fail is partial", []AssertionResult{
			{Pass: true, Outcome: OutcomeComplete}, {Pass: false, Outcome: OutcomeFailed},
		}, OutcomePartial},
	}
	for _, tc := range cases {
		if got := ClassifyOutcome(tc.in); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}
