package studio

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func repairDraft() Draft {
	return Draft{
		Name: "Podcast",
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "search", Kind: "tool", Tool: "web_search", Output: "res", Input: `{"query":"ai"}`},
			{ID: "summarize", Kind: "llm", Output: "sum", Input: `{{ .res.results }}`},
		}},
	}
}

var at = time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)

func TestClassifyFailure_SplitsTheToolFailureBucket(t *testing.T) {
	// The old classifier lumped auth, permission, network and rate limits into
	// one bucket — each of which warrants a completely different response.
	cases := map[string]RepairClass{
		"API returned 401 Unauthorized":            RepairAuth,
		"invalid api key":                          RepairAuth,
		"403 forbidden: missing scope":             RepairPermission,
		"429 Too Many Requests":                    RepairRateLimit,
		"dial tcp: no such host":                   RepairNetwork,
		"context deadline exceeded":                RepairNetwork,
		"the server exploded in an unusual manner": RepairToolFailure,
	}
	for errText, want := range cases {
		got := ClassifyFailure(LiveNodeRun{NodeID: "n", Error: errText})
		if got != want {
			t.Errorf("%q: class = %q, want %q", errText, got, want)
		}
	}
}

func TestSecurityFailuresAreNeverRepaired(t *testing.T) {
	for _, c := range []RepairClass{RepairAuth, RepairPermission} {
		if !IsSecurityClass(c) {
			t.Errorf("%s must be a security class", c)
		}
		a := AdviseRepair(c)
		if a.Repairable {
			t.Errorf("%s must never be marked repairable", c)
		}
		if !a.Security {
			t.Errorf("%s must be flagged as security", c)
		}
	}
	// And the transaction refuses before applying anything.
	draft := repairDraft()
	candidate, attempt := ApplyRepairTransactionally(draft, RepairProposal{
		NodeID: "search", Field: "input", Class: RepairAuth, New: `{"query":"x"}`,
	}, "input", nil, func(Draft, string) (map[string]string, error) {
		t.Fatal("a security-class failure must never reach the replay stage")
		return nil, nil
	}, at)
	if attempt.Promoted || attempt.Validated {
		t.Errorf("a security failure must be refused outright: %+v", attempt)
	}
	if !strings.Contains(attempt.Reason, "never repaired") {
		t.Errorf("reason should state the rule: %q", attempt.Reason)
	}
	if candidate.Flow.Nodes[0].Input != draft.Flow.Nodes[0].Input {
		t.Error("the draft must be returned untouched")
	}
}

func TestRetryableFailuresProposeNoChange(t *testing.T) {
	for _, c := range []RepairClass{RepairNetwork, RepairRateLimit} {
		a := AdviseRepair(c)
		if !a.Retryable || a.Repairable {
			t.Errorf("%s should be retryable and not repairable: %+v", c, a)
		}
	}
}

func TestApplyRepairTransactionally_PromotesOnlyWhenReplayProvesIt(t *testing.T) {
	draft := repairDraft()
	proposal := RepairProposal{
		NodeID: "summarize", Field: "input", Class: RepairShapeDrift,
		Old: `{{ .res.results }}`, New: `{{ toJson .res.items }}`,
		Rationale: "the API returns items, not results", ObservedKeys: []string{"items"},
	}

	// Replay succeeds and the contract is met → promoted.
	contract := &agent.OutcomeContract{Assertions: []agent.OutcomeAssertion{
		{Target: "summarize", Op: OpNotEmpty},
	}}
	good := func(c Draft, in string) (map[string]string, error) {
		if in != "the failing input" {
			t.Errorf("the ORIGINAL failing input must be replayed, got %q", in)
		}
		// The candidate must carry the patch.
		if c.Flow.Nodes[1].Input != proposal.New {
			t.Errorf("replay received an unpatched candidate: %q", c.Flow.Nodes[1].Input)
		}
		return map[string]string{"summarize": `{"items":[1,2]}`}, nil
	}
	candidate, attempt := ApplyRepairTransactionally(draft, proposal, "the failing input", contract, good, at)
	if !attempt.Promoted {
		t.Fatalf("a proven repair must be promoted: %+v", attempt)
	}
	if !attempt.Validated || !attempt.Replayed || !attempt.ReplayPassed {
		t.Errorf("all three gates should be recorded: %+v", attempt)
	}
	if candidate.Flow.Nodes[1].Input != proposal.New {
		t.Error("the promoted candidate must carry the patch")
	}
	// Isolation: the ORIGINAL draft is never mutated.
	if draft.Flow.Nodes[1].Input != proposal.Old {
		t.Error("the original draft must be untouched")
	}
	// Rollback is recorded.
	if attempt.Rollback.Value != proposal.Old || attempt.Rollback.NodeID != "summarize" {
		t.Errorf("rollback must restore the prior value: %+v", attempt.Rollback)
	}
	if len(attempt.Evidence) == 0 || attempt.Rationale == "" {
		t.Error("evidence and rationale must be recorded")
	}
}

func TestApplyRepairTransactionally_RejectsUnprovenRepairs(t *testing.T) {
	draft := repairDraft()
	proposal := RepairProposal{
		NodeID: "summarize", Field: "input", Class: RepairShapeDrift,
		Old: `{{ .res.results }}`, New: `{{ toJson .res.items }}`,
	}
	contract := &agent.OutcomeContract{Assertions: []agent.OutcomeAssertion{
		{Target: "summarize", Op: OpCountGTE, Value: "2"},
	}}

	// This is the case the old path could not catch: the patch COMPILES, the
	// replay RUNS, and the outcome is still wrong.
	stillEmpty := func(Draft, string) (map[string]string, error) {
		return map[string]string{"summarize": `{"items":[]}`}, nil
	}
	candidate, attempt := ApplyRepairTransactionally(draft, proposal, "in", contract, stillEmpty, at)
	if attempt.Promoted {
		t.Fatal("a repair that compiles but does not fix the outcome must NOT be promoted")
	}
	if !attempt.Validated || !attempt.Replayed {
		t.Errorf("it should have validated and replayed before being rejected: %+v", attempt)
	}
	if attempt.ReplayPassed {
		t.Error("replay must not be marked passed")
	}
	if candidate.Flow.Nodes[1].Input != proposal.Old {
		t.Error("a rejected repair must leave the original draft in place")
	}

	// A replay that errors outright is likewise not promoted.
	broken := func(Draft, string) (map[string]string, error) {
		return nil, errors.New("boom")
	}
	if _, attempt := ApplyRepairTransactionally(draft, proposal, "in", contract, broken, at); attempt.Promoted {
		t.Error("a failed replay must not promote")
	}

	// With no replay available, a validated patch is still not promoted —
	// "it compiles" was exactly the insufficient standard this replaces.
	if _, attempt := ApplyRepairTransactionally(draft, proposal, "in", contract, nil, at); attempt.Promoted {
		t.Error("validation alone must never promote")
	}

	// A stale proposal naming a node that no longer exists fails cleanly.
	stale := RepairProposal{NodeID: "ghost", Field: "input", New: "x"}
	if _, attempt := ApplyRepairTransactionally(draft, stale, "in", nil, nil, at); attempt.Promoted || attempt.Validated {
		t.Error("a stale proposal must not apply")
	}
}

func TestApplyRepairTransactionally_RejectsPatchesThatBreakTheGraph(t *testing.T) {
	draft := repairDraft()
	// A template that cannot parse must fail validation before any replay.
	bad := RepairProposal{
		NodeID: "summarize", Field: "input", Class: RepairTemplateError,
		Old: `{{ .res.results }}`, New: `{{ unclosed `,
	}
	replayed := false
	_, attempt := ApplyRepairTransactionally(draft, bad, "in", nil, func(Draft, string) (map[string]string, error) {
		replayed = true
		return nil, nil
	}, at)
	if attempt.Promoted || attempt.Validated {
		t.Errorf("an invalid patch must fail validation: %+v", attempt)
	}
	if replayed {
		t.Error("a patch that does not validate must never be replayed")
	}
}

func TestClassifyEmptyResult(t *testing.T) {
	// The class the original classifier declared but never produced: the node
	// SUCCEEDED and returned nothing usable.
	if !ClassifyEmptyResult(LiveNodeRun{NodeID: "n", Output: []byte(`{"results":[]}`)}) {
		t.Error("an empty collection must be recognised")
	}
	if !ClassifyEmptyResult(LiveNodeRun{NodeID: "n", Output: []byte(`[]`)}) {
		t.Error("an empty array must be recognised")
	}
	if ClassifyEmptyResult(LiveNodeRun{NodeID: "n", Output: []byte(`{"results":[1]}`)}) {
		t.Error("a non-empty result is not the empty case")
	}
	// An errored node is a different class entirely.
	if ClassifyEmptyResult(LiveNodeRun{NodeID: "n", Error: "boom", Output: []byte(`[]`)}) {
		t.Error("an errored node is not the empty-result case")
	}
}
