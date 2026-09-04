package studio

import "testing"

func TestDefaultAgentPolicy_PerStrategy(t *testing.T) {
	// A workflow has no reasoning budget to configure.
	if p := DefaultAgentPolicy(StrategyWorkflow); p.ReAct != nil || p.Plan != nil {
		t.Errorf("workflow should carry no loop policy: %+v", p)
	}
	// Auto gets a contract but neither loop block — the knobs it does not have
	// must not be shown as if it did.
	p := DefaultAgentPolicy(StrategyAuto)
	if p.Contract == nil {
		t.Fatal("auto needs a contract")
	}
	if p.ReAct != nil || p.Plan != nil {
		t.Errorf("auto should carry no ReAct/Plan block: %+v", p)
	}

	r := DefaultAgentPolicy(StrategyReAct)
	if r.ReAct == nil {
		t.Fatal("react needs a react policy")
	}
	// The default that makes an "advanced but brittle" mode safe to offer.
	if !r.ReAct.FallbackToAuto {
		t.Error("ReAct must degrade to Auto by default rather than fail the run")
	}
	if !r.ReAct.PreserveBestResult {
		t.Error("an exhausted loop should still return its best work")
	}

	pe := DefaultAgentPolicy(StrategyPlanExecute)
	if pe.Plan == nil {
		t.Fatal("plan_execute needs a plan policy")
	}
	if !pe.Plan.ApprovalBeforeSideEffects {
		t.Error("side effects should default to requiring approval")
	}
}

func TestNormalizeStrategy_AcceptsSpellings(t *testing.T) {
	for _, in := range []string{"plan_execute", "plan-execute", "planexecute", "PLAN_EXECUTE", " plan_execute "} {
		if got := normalizeStrategy(in); got != StrategyPlanExecute {
			t.Errorf("normalizeStrategy(%q) = %q", in, got)
		}
	}
	if got := normalizeStrategy("something else"); got != StrategyWorkflow {
		t.Errorf("unknown strategy should fall back to workflow, got %q", got)
	}
}

func TestEffectivePolicy_MergesOverDefaults(t *testing.T) {
	d := Draft{Strategy: StrategyReAct}

	// nil policy = pure defaults, and the draft is not mutated.
	got := EffectivePolicy(d)
	if got.ReAct == nil || got.ReAct.RepeatedToolLimit != 2 {
		t.Fatalf("expected defaults, got %+v", got.ReAct)
	}
	if d.Policy != nil {
		t.Error("EffectivePolicy must not write back onto the draft")
	}

	// A partial contract overrides only what it sets.
	d.Policy = &AgentPolicy{Contract: &AgentContract{Goal: "answer the question"}}
	got = EffectivePolicy(d)
	if got.Contract.Goal != "answer the question" {
		t.Errorf("goal not applied: %+v", got.Contract)
	}
	if got.Contract.ToolChoice != "auto" || got.Contract.RecoveryRetries != 2 {
		t.Errorf("unset contract fields should keep defaults: %+v", got.Contract)
	}
}

func TestEffectivePolicy_ReplacesLoopBlockWholesale(t *testing.T) {
	// A user who edited the ReAct block stated the whole budget; field-merging
	// would produce a combination nobody chose.
	d := Draft{
		Strategy: StrategyReAct,
		Policy: &AgentPolicy{ReAct: &ReActPolicy{
			InvalidStepBudget: 5, RepeatedToolLimit: 1,
			FallbackToAuto: false, PreserveBestResult: false,
		}},
	}
	got := EffectivePolicy(d)
	if got.ReAct.FallbackToAuto || got.ReAct.PreserveBestResult {
		t.Errorf("explicit false must survive the merge: %+v", got.ReAct)
	}
	if got.ReAct.InvalidStepBudget != 5 || got.ReAct.RepeatedToolLimit != 1 {
		t.Errorf("explicit budgets not applied: %+v", got.ReAct)
	}
}

func TestEffectivePolicy_IgnoresBlockForWrongStrategy(t *testing.T) {
	// A ReAct block on a Plan-Execute draft is stale config, not intent.
	d := Draft{
		Strategy: StrategyPlanExecute,
		Policy:   &AgentPolicy{ReAct: &ReActPolicy{InvalidStepBudget: 9}},
	}
	if got := EffectivePolicy(d); got.ReAct != nil {
		t.Errorf("react block must not apply under plan_execute: %+v", got.ReAct)
	}
}

func TestValidatePolicy(t *testing.T) {
	// Workflows have no reasoning contract to complain about.
	if w := ValidatePolicy(Draft{Strategy: StrategyWorkflow}); w != nil {
		t.Errorf("workflow should produce no policy warnings: %v", w)
	}

	// The default agent has no completion criteria, which is the warning that
	// matters most: without it the step budget is the only stop condition.
	warns := ValidatePolicy(Draft{Strategy: StrategyAuto})
	if !containsSubstr(warns, "completion criteria") {
		t.Errorf("expected a completion-criteria warning, got %v", warns)
	}

	// An out-of-range confidence threshold is meaningless.
	d := Draft{Strategy: StrategyReAct, Policy: &AgentPolicy{ReAct: &ReActPolicy{
		ConfidenceThreshold: 1.5, InvalidStepBudget: 1, FallbackToAuto: true,
	}}}
	if !containsSubstr(ValidatePolicy(d), "between 0 and 1") {
		t.Errorf("expected a threshold warning, got %v", ValidatePolicy(d))
	}

	// Neither safety net = a run that can return nothing at all.
	d = Draft{Strategy: StrategyReAct, Policy: &AgentPolicy{ReAct: &ReActPolicy{
		ConfidenceThreshold: 0.5, InvalidStepBudget: 2,
		FallbackToAuto: false, PreserveBestResult: false,
	}}}
	if !containsSubstr(ValidatePolicy(d), "returns nothing at all") {
		t.Errorf("expected a no-safety-net warning, got %v", ValidatePolicy(d))
	}

	// An untitled plan step is named by position so it can be found.
	d = Draft{Strategy: StrategyPlanExecute, Policy: &AgentPolicy{Plan: &PlanExecutePolicy{
		Steps: []PlanExecuteStep{{Title: "ok"}, {Title: "  "}},
	}}}
	if !containsSubstr(ValidatePolicy(d), "step 2") {
		t.Errorf("expected step 2 to be flagged, got %v", ValidatePolicy(d))
	}
}

func TestItoa(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{{0, "0"}, {7, "7"}, {42, "42"}, {1000, "1000"}, {-3, "-3"}} {
		if got := itoa(tc.in); got != tc.want {
			t.Errorf("itoa(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func containsSubstr(list []string, want string) bool {
	for _, s := range list {
		for i := 0; i+len(want) <= len(s); i++ {
			if s[i:i+len(want)] == want {
				return true
			}
		}
	}
	return false
}
