package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/pkg/agent"
)

func nodeRun(id, output string) reasoning.FlowNodeRun {
	return reasoning.FlowNodeRun{NodeID: id, Output: json.RawMessage(output)}
}

func contract(enforce string, as ...agent.OutcomeAssertion) *agent.OutcomeContract {
	return &agent.OutcomeContract{Assertions: as, Enforce: enforce}
}

func TestEvaluateOutcome_NoContractIsAlwaysMet(t *testing.T) {
	for _, c := range []*agent.OutcomeContract{nil, {}, {Assertions: nil}} {
		r := EvaluateOutcome(c, json.RawMessage(`{}`), nil)
		if !r.Met || r.Outcome != OutcomeComplete {
			t.Errorf("an agent with no contract must be unaffected: %+v", r)
		}
	}
}

// The case the whole epic exists for: every node ran, nothing errored, and the
// run achieved nothing. That is Empty, and it must NOT be reported as success.
func TestEvaluateOutcome_CleanRunThatAchievedNothingIsEmpty(t *testing.T) {
	c := contract("", agent.OutcomeAssertion{
		Target: "search_article_sources", Op: "count_gte", Value: "3",
		Describe: "at least three articles were found",
	})
	trace := []reasoning.FlowNodeRun{nodeRun("search_article_sources", `{"results":[]}`)}

	r := EvaluateOutcome(c, json.RawMessage(`{"ok":true}`), trace)
	if r.Met {
		t.Fatal("a run that collected zero articles must not meet the contract")
	}
	if r.Outcome != OutcomeEmpty {
		t.Errorf("outcome = %q, want empty (not failed — nothing errored)", r.Outcome)
	}
	// The summary must speak the author's language, not the operator's.
	if !strings.Contains(r.Summary, "at least three articles were found") {
		t.Errorf("summary should carry the Describe text: %q", r.Summary)
	}
	if !strings.Contains(r.Summary, "0 items") {
		t.Errorf("summary should say what actually happened: %q", r.Summary)
	}
}

func TestEvaluateOutcome_PodcastWorkflow(t *testing.T) {
	// P0-4's worked example: sources added, audio completed, link delivered.
	c := contract("",
		agent.OutcomeAssertion{Target: "add_source_pack", Op: "count_gte", Value: "3", Describe: "three sources were added"},
		agent.OutcomeAssertion{Target: "poll_audio_status", Op: "artifact", Describe: "the audio finished rendering"},
		agent.OutcomeAssertion{Target: "deliver_audio_status", Op: "delivered", Value: "telegram", Describe: "the link reached Telegram"},
	)

	good := []reasoning.FlowNodeRun{
		nodeRun("add_source_pack", `{"sources":[{"id":"a"},{"id":"b"},{"id":"c"}]}`),
		nodeRun("poll_audio_status", `{"artifacts":[{"id":"x"}],"status":"success"}`),
		nodeRun("deliver_audio_status", `{"ok":true,"channel":"telegram","to":"-100123"}`),
	}
	if r := EvaluateOutcome(c, nil, good); !r.Met || r.Outcome != OutcomeComplete {
		t.Fatalf("a fully successful podcast run must be complete: %+v", r)
	}

	// Audio still rendering is PARTIAL: the run did its part, the provider
	// hasn't finished. Reporting that as success is the bug.
	pending := append([]reasoning.FlowNodeRun(nil), good...)
	pending[1] = nodeRun("poll_audio_status", `{"artifact_id":"da70","artifact_status":"processing"}`)
	r := EvaluateOutcome(c, nil, pending)
	if r.Met {
		t.Error("an unfinished artifact must not meet the contract")
	}
	if r.Outcome != OutcomePartial {
		t.Errorf("outcome = %q, want partial", r.Outcome)
	}

	// Delivered successfully to the WRONG destination is a failure, and the one
	// a node-level check can never catch.
	wrong := append([]reasoning.FlowNodeRun(nil), good...)
	wrong[2] = nodeRun("deliver_audio_status", `{"ok":true,"channel":"slack","to":"-100123"}`)
	if r := EvaluateOutcome(c, nil, wrong); r.Met {
		t.Error("delivery via the wrong channel must fail the contract")
	}
}

func TestEvaluateOutcome_DestinationMismatch(t *testing.T) {
	c := contract("", agent.OutcomeAssertion{Target: "send", Op: "destination", Value: "-100123"})
	trace := []reasoning.FlowNodeRun{nodeRun("send", `{"ok":true,"channel":"telegram","to":"-100999"}`)}
	r := EvaluateOutcome(c, nil, trace)
	if r.Met {
		t.Fatal("sending to the wrong chat must fail")
	}
	if !strings.Contains(r.Summary, "-100999") || !strings.Contains(r.Summary, "-100123") {
		t.Errorf("summary should show got and wanted: %q", r.Summary)
	}
}

func TestEvaluateOutcome_PartialWhenSomePass(t *testing.T) {
	c := contract("",
		agent.OutcomeAssertion{Target: "a", Op: "not_empty"},
		agent.OutcomeAssertion{Target: "b", Op: "has_field", Value: "url"},
	)
	trace := []reasoning.FlowNodeRun{
		nodeRun("a", `{"items":[1,2]}`),
		nodeRun("b", `{"title":"no url here"}`),
	}
	r := EvaluateOutcome(c, nil, trace)
	if r.Met {
		t.Fatal("a missing required field must fail the contract")
	}
	if r.Outcome != OutcomePartial {
		t.Errorf("outcome = %q, want partial (one passed, one failed)", r.Outcome)
	}
}

func TestEvaluateOutcome_MissingStepIsFailure(t *testing.T) {
	c := contract("", agent.OutcomeAssertion{Target: "never_ran", Op: "not_empty"})
	r := EvaluateOutcome(c, nil, []reasoning.FlowNodeRun{nodeRun("other", `{}`)})
	if r.Met || r.Outcome != OutcomeFailed {
		t.Fatalf("an assertion on a step that never ran must fail: %+v", r)
	}
	if !strings.Contains(r.Summary, "did not run") {
		t.Errorf("summary should say the step didn't run: %q", r.Summary)
	}
}

func TestEvaluateOutcome_FieldEqualsAndPaths(t *testing.T) {
	trace := []reasoning.FlowNodeRun{
		nodeRun("nb", `{"notebook":{"id":"1df0","state":"ready"},"results":[{"url":"https://a"}]}`),
	}
	pass := []agent.OutcomeAssertion{
		{Target: "nb", Op: "has_field", Value: "notebook.id"},
		{Target: "nb", Op: "field_equals", Value: "notebook.state=ready"},
		{Target: "nb", Op: "has_field", Value: "results.0.url"},
		{Target: "nb", Op: "count_gte", Value: "1"},
	}
	for _, a := range pass {
		if r := EvaluateOutcome(contract("", a), nil, trace); !r.Met {
			t.Errorf("assertion %+v should pass: %s", a, r.Summary)
		}
	}
	fail := []agent.OutcomeAssertion{
		{Target: "nb", Op: "has_field", Value: "notebook.missing"},
		{Target: "nb", Op: "field_equals", Value: "notebook.state=pending"},
		{Target: "nb", Op: "count_gte", Value: "5"},
	}
	for _, a := range fail {
		if r := EvaluateOutcome(contract("", a), nil, trace); r.Met {
			t.Errorf("assertion %+v should fail", a)
		}
	}
}

func TestEvaluateOutcome_TargetsRunResultByDefault(t *testing.T) {
	c := contract("", agent.OutcomeAssertion{Target: "result", Op: "not_empty"})
	if r := EvaluateOutcome(c, json.RawMessage(`{"items":[1]}`), nil); !r.Met {
		t.Error("target 'result' should read the run's final output")
	}
	if r := EvaluateOutcome(c, json.RawMessage(`[]`), nil); r.Met {
		t.Error("an empty final result must not satisfy not_empty")
	}
}

func TestEvaluateOutcome_DecodesStringWrappedJSON(t *testing.T) {
	// Tool output routinely arrives as a JSON document inside a JSON string.
	// An assertion must see through that, or every count would read as 1.
	c := contract("", agent.OutcomeAssertion{Target: "n", Op: "count_gte", Value: "2"})
	trace := []reasoning.FlowNodeRun{nodeRun("n", `"{\"results\":[1,2,3]}"`)}
	if r := EvaluateOutcome(c, nil, trace); !r.Met {
		t.Errorf("string-wrapped JSON should decode: %s", r.Summary)
	}
}

func TestEnforcementModeDefaultsToReport(t *testing.T) {
	// Adding a contract to an existing agent must not silently start dropping
	// its output — enforcement is opt-in.
	if (&agent.OutcomeContract{}).EnforcementMode() != agent.EnforceReport {
		t.Error("default enforcement must be report")
	}
	if (&agent.OutcomeContract{Enforce: "fail"}).EnforcementMode() != agent.EnforceFail {
		t.Error("explicit fail must be honoured")
	}
	var nilContract *agent.OutcomeContract
	if nilContract.EnforcementMode() != agent.EnforceReport {
		t.Error("a nil contract must be nil-safe")
	}
}
