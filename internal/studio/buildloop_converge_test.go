package studio

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// handoffVerifier fails verification with the template-field-on-untyped error a
// fixed flow throws when it tries to read a field out of an upstream agent's
// free-form reply — the real "flow fighting a reasoning task" failure in Studio.
type handoffVerifier struct{ n int }

func (v *handoffVerifier) Verify(ctx context.Context, d Draft, tc TestCase) VerifyOutcome {
	v.n++
	return VerifyOutcome{
		OK:    false,
		Real:  true,
		Error: `flow: node "fetch_data": render input: execute template: template: :1:26: executing "" at <.parsed.skill>: can't evaluate field skill in type interface {}`,
	}
}

// A runtime handoff/parse error the loop can't repair must end with a verdict
// that recommends authoring this as an Auto agent, not endless flow edits or
// implicit ReAct.
func TestBuildLoop_HandoffErrorRecommendsAgentMode(t *testing.T) {
	d := Draft{Name: "finance", Trigger: Trigger{Type: "manual"}}
	v := &handoffVerifier{}
	// fakeLLM returning an error makes RepairWithProblems a no-op, so the loop hits
	// the "no automated fix" branch on the first verify failure.
	rep := BuildUntilWorks(context.Background(), fakeLLM{err: context.Canceled}, d, Catalog{}, BuildOptions{
		MaxAttempts: 6,
		Verifier:    v,
		Tests:       []TestCase{{Input: "How is AAPL doing?"}},
	})

	if rep.OK || rep.Verified {
		t.Fatalf("a never-passing flow must not report OK/Verified; got %+v", rep)
	}
	if rep.SuggestMode != "auto" {
		t.Errorf("a handoff/template failure should suggest auto mode; got %q (diagnosis: %q)", rep.SuggestMode, rep.Diagnosis)
	}
	if !strings.Contains(strings.ToLower(rep.Diagnosis), "agent") {
		t.Errorf("diagnosis should point the user at agent mode; got %q", rep.Diagnosis)
	}
	if rep.Summary != rep.Diagnosis {
		t.Errorf("the diagnosis should headline the summary; summary=%q", rep.Summary)
	}
}

// Preflight blockers that say a step references a value nothing upstream
// produces (the user's message / context / channel) are agent-fit: a fixed flow
// can't satisfy them. The loop must classify them as such and recommend Auto
// rather than reporting them as ordinary "still unresolved" problems.
func TestProblemsAreReasoningFit_PreflightHandoffBlockers(t *testing.T) {
	problems := []string{
		`step "produce": Step references {{ .message }} but no earlier step produces "message".`,
		`step "produce": Step references {{ .context }} but no earlier step produces "context".`,
		`step "produce": Step references {{ .channel }} but no earlier step produces "channel".`,
		`step "deliver": Step references {{ .channel }} but no earlier step produces "channel".`,
	}
	if !problemsAreReasoningFit(problems) {
		t.Fatalf("a flow referencing values nothing produces should be reasoning-fit")
	}
	d, mode := reasoningFitDiagnosis()
	if mode != "auto" || !strings.Contains(strings.ToLower(d), "agent") {
		t.Errorf("reasoning-fit verdict should recommend the auto agent; got mode=%q diag=%q", mode, d)
	}
	// A normal blocker set (e.g. a missing required arg) must NOT be reasoning-fit.
	ordinary := []string{`step "fetch": required argument "ticker" is empty.`}
	if problemsAreReasoningFit(ordinary) {
		t.Errorf("ordinary blockers must not be misclassified as reasoning-fit")
	}
}

// The PREFLIGHT-phase convergence guard must stop when the same blocker set
// recurs after a repair (the LLM keeps "fixing" without resolving it), instead
// of burning the whole attempt budget — and recommend Auto when the residual is
// agent-fit. Regression for the seenProblemSets guard.
func TestBuildLoop_PreflightNonConvergenceStops(t *testing.T) {
	// A flow node references a value nothing upstream produces — an unresolvable
	// handoff blocker. The fake LLM "repairs" by returning the SAME draft, so the
	// blocker recurs every attempt.
	bad := Draft{
		Name: "x", Trigger: Trigger{Type: "manual"},
		Flow: Flow{Entry: "a", Nodes: []sdkr.FlowNode{
			{ID: "a", Kind: sdkr.FlowNodeTool, Tool: "web_search",
				Input: `{"query":"{{ .missing }}"}`, Output: "r"},
		}},
	}
	raw, _ := json.Marshal(bad)
	rep := BuildUntilWorks(context.Background(), fakeLLM{out: string(raw)}, bad, Catalog{}, BuildOptions{
		MaxAttempts: 6,
		// No verifier → validation-only; the preflight phase drives the loop.
	})
	if rep.OK {
		t.Fatalf("an unresolvable draft must not report OK; got %+v", rep)
	}
	if len(rep.Residual) == 0 {
		t.Errorf("expected residual problems recorded")
	}
	// Must stop early, not run all 6 attempts on the same recurring blocker.
	if len(rep.Attempts) > 3 {
		t.Errorf("non-convergence guard should stop early; ran %d attempts", len(rep.Attempts))
	}
	if rep.SuggestMode != "auto" {
		t.Errorf("a recurring handoff blocker should recommend auto; got %q (diag %q)", rep.SuggestMode, rep.Diagnosis)
	}
}

// errSignature must collapse the field names the model keeps permuting onto one
// class (so a recurrence is detected) while keeping distinct nodes/classes apart.
func TestErrSignature_CollapsesPermutedFields(t *testing.T) {
	a := `flow: node "fetch_data": render input: execute template: executing "" at <.parsed.skill>: can't evaluate field skill in type interface {}`
	b := `flow: node "fetch_data": render input: execute template: executing "" at <.parsed.skill_name>: can't evaluate field skill_name in type interface {}`
	if errSignature(a) != errSignature(b) {
		t.Fatalf("permuted field names should share a signature: %q vs %q", errSignature(a), errSignature(b))
	}
	c := `flow: node "other": some unrelated failure`
	if errSignature(a) == errSignature(c) {
		t.Errorf("different nodes/classes must not collide")
	}
}
