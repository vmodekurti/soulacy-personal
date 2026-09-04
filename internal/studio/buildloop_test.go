package studio

import (
	"context"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// seqVerifier returns a scripted sequence of outcomes, one per Verify call.
type seqVerifier struct {
	outs []VerifyOutcome
	i    int
}

func (s *seqVerifier) Verify(ctx context.Context, draft Draft, tc TestCase) VerifyOutcome {
	if s.i >= len(s.outs) {
		return VerifyOutcome{OK: true, Real: true}
	}
	o := s.outs[s.i]
	s.i++
	return o
}

func cleanWorkflow() Draft {
	return Draft{
		Name:    "Clean",
		Trigger: Trigger{Type: "manual"},
		Flow: Flow{
			Nodes: []sdkr.FlowNode{
				{ID: "a", Kind: "tool", Tool: "web_search", Input: `{"query":"x"}`, Output: "r"},
			},
			Entry: "a",
		},
	}
}

func TestBuildUntilWorks_CleanDraftVerifies(t *testing.T) {
	v := &seqVerifier{outs: []VerifyOutcome{{OK: true, Real: true}}}
	rep := BuildUntilWorks(context.Background(), fakeLLM{err: context.Canceled}, cleanWorkflow(), Catalog{},
		BuildOptions{Verifier: v})
	if !rep.OK || !rep.Verified {
		t.Fatalf("clean draft should build+verify; rep.OK=%v verified=%v attempts=%+v", rep.OK, rep.Verified, rep.Attempts)
	}
}

func TestBuildUntilWorks_RepairsRuntimeErrorThenVerifies(t *testing.T) {
	// The fake model returns a (different) valid workflow so RepairWithProblems
	// reports a change; the verifier fails the first run, passes the second.
	fixed := `{"name":"Fixed","trigger":{"type":"manual"},"flow":{"nodes":[` +
		`{"id":"a","kind":"tool","tool":"web_search","input":"{\"query\":\"y\"}","output":"r"}],"entry":"a"}}`
	v := &seqVerifier{outs: []VerifyOutcome{
		{OK: false, Real: true, Error: "tool web_search returned nothing"},
		{OK: true, Real: true},
	}}
	rep := BuildUntilWorks(context.Background(), fakeLLM{out: fixed}, cleanWorkflow(), Catalog{},
		BuildOptions{Verifier: v})
	if !rep.OK || !rep.Verified {
		t.Fatalf("should repair runtime error then verify; rep=%+v", rep)
	}
	// There must be a verify attempt that repaired against the runtime error.
	var repaired bool
	for _, a := range rep.Attempts {
		if a.Phase == "verify" && a.Changed {
			repaired = true
		}
	}
	if !repaired {
		t.Errorf("expected a verify attempt that repaired against the runtime error; got %+v", rep.Attempts)
	}
}

func TestBuildUntilWorks_StopsWhenModelCannotFix(t *testing.T) {
	// A blocker the deterministic passes can't resolve and the model won't fix
	// (LLM errors) must terminate with residual problems, not loop forever.
	bad := Draft{
		Name:    "Bad",
		Trigger: Trigger{Type: "schedule"}, // schedule with no cron → blocker
		Flow: Flow{
			Nodes: []sdkr.FlowNode{{ID: "a", Kind: "tool", Tool: "web_search", Input: `{"query":"x"}`, Output: "r"}},
			Entry: "a",
		},
	}
	rep := BuildUntilWorks(context.Background(), fakeLLM{err: context.Canceled}, bad, Catalog{},
		BuildOptions{Verifier: &seqVerifier{}, MaxAttempts: 4})
	if rep.OK {
		t.Fatalf("unfixable blocker should not report OK; rep=%+v", rep)
	}
	if len(rep.Residual) == 0 {
		t.Errorf("expected residual problems; got none. attempts=%+v", rep.Attempts)
	}
	if len(rep.Attempts) > 4 {
		t.Errorf("should respect attempt budget; got %d attempts", len(rep.Attempts))
	}
}

func TestBuildUntilWorks_ValidationOnlyWhenNoVerifier(t *testing.T) {
	rep := BuildUntilWorks(context.Background(), fakeLLM{err: context.Canceled}, cleanWorkflow(), Catalog{},
		BuildOptions{})
	if !rep.OK {
		t.Fatalf("clean draft should pass validation-only build; rep=%+v", rep)
	}
	if rep.Verified {
		t.Errorf("no verifier → Verified must be false")
	}
}
