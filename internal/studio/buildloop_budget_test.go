package studio

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/costs"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// spyRealVerifier stands in for RealRunVerifier: it reports real side effects
// and records every time it was actually allowed to run. If the build loop's
// polarity is ever inverted again, `ran` goes non-zero without an explicit
// SideEffectsReal opt-in and the safety tests below fail loudly.
type spyRealVerifier struct{ ran int }

func (v *spyRealVerifier) Verify(ctx context.Context, d Draft, tc TestCase) VerifyOutcome {
	v.ran++
	return VerifyOutcome{OK: true, Real: true}
}
func (v *spyRealVerifier) RealSideEffects() bool { return true }

// THE critical safety assertion: a caller that constructs BuildOptions without
// thinking about side effects must get the MOCKED verifier. Zero value = safe.
func TestVerifierFor_ZeroPolicyIsMocked(t *testing.T) {
	var zero SideEffectPolicy // exactly what a forgotten struct field yields
	if _, ok := VerifierFor(zero).(DryRunVerifier); !ok {
		t.Fatalf("zero SideEffectPolicy must select the mocked DryRunVerifier; got %T", VerifierFor(zero))
	}
	if _, ok := VerifierFor(SideEffectsMocked).(DryRunVerifier); !ok {
		t.Fatalf("SideEffectsMocked must select DryRunVerifier")
	}
	// Asking for real WITHOUT a runner must not produce a real verifier either —
	// a runnerless RealRunVerifier silently skips every step, which would report
	// a green "real run" that ran nothing.
	if _, ok := VerifierFor(SideEffectsReal).(DryRunVerifier); !ok {
		t.Fatalf("SideEffectsReal without a runner must fall back to mocked; got %T", VerifierFor(SideEffectsReal))
	}
}

// The explicit opt-in must actually reach real execution — the safe default is
// only acceptable if the deliberate path still works.
func TestVerifierFor_ExplicitRealSelectsRealVerifier(t *testing.T) {
	v := VerifierFor(SideEffectsReal, RealRunner{
		Tool: func(ctx context.Context, name, args string) (json.RawMessage, error) {
			return json.RawMessage(`"ok"`), nil
		},
	})
	rv, ok := v.(RealRunVerifier)
	if !ok {
		t.Fatalf("SideEffectsReal + runner must select RealRunVerifier; got %T", v)
	}
	if rv.Runner.Tool == nil {
		t.Errorf("the injected runner must be carried onto the verifier")
	}
	if !rv.RealSideEffects() {
		t.Errorf("RealRunVerifier must declare real side effects")
	}
}

// Handing the loop a real verifier but forgetting BuildOptions.SideEffects is
// the exact defect this story fixes. It must DOWNGRADE to mocked, not run.
func TestBuildUntilWorks_ZeroOptionsNeverRunsRealVerifier(t *testing.T) {
	spy := &spyRealVerifier{}
	rep := BuildUntilWorks(context.Background(), fakeLLM{err: context.Canceled}, cleanWorkflow(), Catalog{},
		BuildOptions{Verifier: spy}) // SideEffects deliberately unset
	if spy.ran != 0 {
		t.Fatalf("a real verifier must NOT run without an explicit SideEffectsReal opt-in (ran %d times)", spy.ran)
	}
	if rep.SideEffects != SideEffectsMocked {
		t.Errorf("report should record the mocked policy; got %q", rep.SideEffects)
	}
	// The build still happens — just mocked — so this is a downgrade, not a stall.
	if !rep.Verified {
		t.Errorf("the downgraded mocked run should still verify a clean draft; rep=%+v", rep)
	}
}

// The explicit opt-in must still reach the real verifier through the loop.
func TestBuildUntilWorks_ExplicitRealRunsRealVerifier(t *testing.T) {
	spy := &spyRealVerifier{}
	rep := BuildUntilWorks(context.Background(), fakeLLM{err: context.Canceled}, cleanWorkflow(), Catalog{},
		BuildOptions{Verifier: spy, SideEffects: SideEffectsReal})
	if spy.ran == 0 {
		t.Fatalf("an explicit SideEffectsReal build must run the real verifier")
	}
	if rep.SideEffects != SideEffectsReal {
		t.Errorf("report should record the real policy; got %q", rep.SideEffects)
	}
}

// A Runner without the policy is not an opt-in either.
func TestBuildUntilWorks_RunnerWithoutPolicyStaysMocked(t *testing.T) {
	var toolCalls int
	opts := BuildOptions{Runner: &RealRunner{
		Tool: func(ctx context.Context, name, args string) (json.RawMessage, error) {
			toolCalls++
			return json.RawMessage(`"ok"`), nil
		},
	}}
	rep := BuildUntilWorks(context.Background(), fakeLLM{err: context.Canceled}, cleanWorkflow(), Catalog{}, opts)
	if toolCalls != 0 {
		t.Fatalf("supplying a RealRunner must not by itself execute real tools (%d calls)", toolCalls)
	}
	if rep.SideEffects != SideEffectsMocked {
		t.Errorf("policy should be mocked; got %q", rep.SideEffects)
	}
}

// A converged build reports "converged" — the only success stop reason.
func TestBuildUntilWorks_StoppedReasonConverged(t *testing.T) {
	rep := BuildUntilWorks(context.Background(), fakeLLM{err: context.Canceled}, cleanWorkflow(), Catalog{},
		BuildOptions{Verifier: &seqVerifier{outs: []VerifyOutcome{{OK: true, Real: true}}}})
	if rep.StoppedReason != StoppedConverged {
		t.Fatalf("a passing build must report %q; got %q", StoppedConverged, rep.StoppedReason)
	}
	if rep.Elapsed <= 0 {
		t.Errorf("Elapsed must be populated so the UI can show how long the build took")
	}
	for _, a := range rep.Attempts {
		if a.StartedAt.IsZero() {
			t.Errorf("every attempt needs StartedAt for progress display; got %+v", a)
		}
	}
}

// alwaysFailVerifier keeps the loop repairing so the attempt budget is the
// thing that ends it.
type alwaysFailVerifier struct{ n int }

func (v *alwaysFailVerifier) Verify(ctx context.Context, d Draft, tc TestCase) VerifyOutcome {
	v.n++
	// A fresh error on a DIFFERENT node every time, so neither the exact-match
	// nor the error-class non-convergence guard fires and the loop genuinely
	// runs to its attempt budget (which is what these tests are measuring).
	return VerifyOutcome{OK: false, Real: false, Error: `node "n` + itoa(v.n) + `": tool call failed`}
}

// alternatingLLM returns a different valid draft on each call so every repair
// "changes" the draft and the loop keeps going.
type alternatingLLM struct{ n int }

func (l *alternatingLLM) Complete(ctx context.Context, prompt string) (string, error) {
	l.n++
	d := Draft{
		Name: "Fixed" + itoa(l.n), Trigger: Trigger{Type: "manual"},
		Flow: Flow{Entry: "a", Nodes: []sdkr.FlowNode{
			{ID: "a", Kind: sdkr.FlowNodeTool, Tool: "web_search",
				Input: `{"query":"q` + itoa(l.n) + `"}`, Output: "r"},
		}},
	}
	raw, _ := json.Marshal(d)
	return string(raw), nil
}

func TestBuildUntilWorks_StoppedReasonMaxAttempts(t *testing.T) {
	rep := BuildUntilWorks(context.Background(), &alternatingLLM{}, cleanWorkflow(), Catalog{}, BuildOptions{
		MaxAttempts: 3,
		Verifier:    &alwaysFailVerifier{},
	})
	if rep.StoppedReason != StoppedMaxAttempts {
		t.Fatalf("exhausting the attempt budget must report %q; got %q (attempts=%d)",
			StoppedMaxAttempts, rep.StoppedReason, len(rep.Attempts))
	}
	if len(rep.Attempts) != 3 {
		t.Errorf("expected exactly 3 attempts; got %d", len(rep.Attempts))
	}
}

// An already-spent wall-clock budget must stop the loop cleanly — before any
// work is committed — and say so, rather than erroring out mid-attempt.
func TestBuildUntilWorks_StoppedReasonTimeBudget(t *testing.T) {
	spy := &spyRealVerifier{}
	rep := BuildUntilWorks(context.Background(), &alternatingLLM{}, cleanWorkflow(), Catalog{}, BuildOptions{
		MaxAttempts: 6,
		MaxElapsed:  time.Nanosecond, // already spent by the time attempt 1 starts
		Verifier:    spy,
		SideEffects: SideEffectsReal,
	})
	if rep.StoppedReason != StoppedTimeBudget {
		t.Fatalf("an exhausted time budget must report %q; got %q", StoppedTimeBudget, rep.StoppedReason)
	}
	if spy.ran != 0 {
		t.Errorf("no attempt should have been started; verifier ran %d times", spy.ran)
	}
	if rep.OK || rep.Verified {
		t.Errorf("a budget stop must not be reported as a successful build; rep=%+v", rep)
	}
	if !strings.Contains(strings.ToLower(rep.Summary), "time budget") {
		t.Errorf("the summary should name the budget that ran out; got %q", rep.Summary)
	}
}

// countingUsage is a usage source that charges a fixed number of tokens per
// sample-triggering LLM call, mimicking costs.UsageRecord's cumulative shape.
type countingUsage struct {
	perCall costs.UsageRecord
	llm     *alternatingLLM
}

func (c *countingUsage) sample() costs.UsageRecord {
	calls := c.llm.n
	return costs.UsageRecord{
		PromptTokens: c.perCall.PromptTokens * calls,
		CompTokens:   c.perCall.CompTokens * calls,
		TotalTokens:  c.perCall.TotalTokens * calls,
		CostUSD:      c.perCall.CostUSD * float64(calls),
	}
}

func TestBuildUntilWorks_StoppedReasonTokenBudget(t *testing.T) {
	llm := &alternatingLLM{}
	usage := &countingUsage{perCall: costs.UsageRecord{PromptTokens: 400, CompTokens: 100, TotalTokens: 500, CostUSD: 0.02}, llm: llm}
	rep := BuildUntilWorks(context.Background(), llm, cleanWorkflow(), Catalog{}, BuildOptions{
		MaxAttempts: 6,
		MaxTokens:   600, // one repair (500) is fine; the second attempt is not
		Usage:       usage.sample,
		Verifier:    &alwaysFailVerifier{},
	})
	if rep.StoppedReason != StoppedTokenBudget {
		t.Fatalf("an exhausted token budget must report %q; got %q (attempts=%d tokens=%d)",
			StoppedTokenBudget, rep.StoppedReason, len(rep.Attempts), rep.TokensUsed)
	}
	if len(rep.Attempts) >= 6 {
		t.Errorf("the token budget should have stopped the loop early; ran %d attempts", len(rep.Attempts))
	}
	if rep.TokensUsed <= 0 {
		t.Errorf("consumption must be accumulated onto the report; got %d", rep.TokensUsed)
	}
	if rep.CostUSD <= 0 {
		t.Errorf("cost must be accumulated onto the report; got %f", rep.CostUSD)
	}
	// Per-attempt accounting is what lets the UI show where the budget went.
	var charged bool
	for _, a := range rep.Attempts {
		if a.Tokens > 0 {
			charged = true
		}
	}
	if !charged {
		t.Errorf("no attempt was charged any tokens; attempts=%+v", rep.Attempts)
	}
}

func TestBuildUntilWorks_StoppedReasonCostBudget(t *testing.T) {
	llm := &alternatingLLM{}
	usage := &countingUsage{perCall: costs.UsageRecord{TotalTokens: 10, CostUSD: 1.50}, llm: llm}
	rep := BuildUntilWorks(context.Background(), llm, cleanWorkflow(), Catalog{}, BuildOptions{
		MaxAttempts: 6,
		MaxCostUSD:  2.00,
		Usage:       usage.sample,
		Verifier:    &alwaysFailVerifier{},
	})
	if rep.StoppedReason != StoppedCostBudget {
		t.Fatalf("an exhausted cost budget must report %q; got %q", StoppedCostBudget, rep.StoppedReason)
	}
}

// Budgets that are left at zero must mean UNLIMITED, not "instantly exhausted".
func TestBuildUntilWorks_ZeroBudgetsAreUnlimited(t *testing.T) {
	llm := &alternatingLLM{}
	usage := &countingUsage{perCall: costs.UsageRecord{TotalTokens: 10_000_000, CostUSD: 999}, llm: llm}
	rep := BuildUntilWorks(context.Background(), llm, cleanWorkflow(), Catalog{}, BuildOptions{
		MaxAttempts: 2,
		Usage:       usage.sample,
		Verifier:    &alwaysFailVerifier{},
	})
	if rep.StoppedReason != StoppedMaxAttempts {
		t.Fatalf("zero token/cost budgets must be unlimited; got stop reason %q", rep.StoppedReason)
	}
}

// recordingVerifier remembers which test inputs it saw, per attempt, so we can
// prove the regression suite is exercised on EVERY attempt and not just once.
type recordingVerifier struct {
	attempts [][]string
	failFor  int // fail while len(attempts) <= failFor
}

func (v *recordingVerifier) Verify(ctx context.Context, d Draft, tc TestCase) VerifyOutcome {
	// Each verifyAll pass replays the whole plan; a new pass starts when the
	// first case of the plan comes round again.
	if len(v.attempts) == 0 || tc.Input == "regression-case" {
		v.attempts = append(v.attempts, nil)
	}
	i := len(v.attempts) - 1
	v.attempts[i] = append(v.attempts[i], tc.Input)
	if i < v.failFor {
		return VerifyOutcome{OK: false, Real: false, Error: "transient failure " + itoa(i)}
	}
	return VerifyOutcome{OK: true, Real: false}
}

// ST-12 AC5: regression tests carried in from previous builds must run on every
// attempt alongside the freshly synthesized ones, and the green set must come
// back on the report so the caller can persist it onto the draft.
func TestBuildUntilWorks_RegressionTestsRunEveryAttemptAndAreReturned(t *testing.T) {
	v := &recordingVerifier{failFor: 1} // fail the first pass, pass the second
	rep := BuildUntilWorks(context.Background(), &alternatingLLM{}, cleanWorkflow(), Catalog{}, BuildOptions{
		MaxAttempts:     4,
		Verifier:        v,
		RegressionTests: []TestCase{{Input: "regression-case"}},
		Tests:           []TestCase{{Input: "fresh-case"}},
	})
	if len(v.attempts) < 2 {
		t.Fatalf("expected at least two verify passes; got %d (%v)", len(v.attempts), v.attempts)
	}
	for i, inputs := range v.attempts {
		if !containsString(inputs, "regression-case") {
			t.Errorf("attempt %d did not run the carried-over regression test; ran %v", i+1, inputs)
		}
	}
	// The green pass must have exercised BOTH sets (earlier passes stop at the
	// first failure by design — the loop repairs one concrete error at a time).
	last := v.attempts[len(v.attempts)-1]
	if !containsString(last, "regression-case") || !containsString(last, "fresh-case") {
		t.Errorf("the passing attempt must run regression + synthesized tests; ran %v", last)
	}
	// The regression case must come FIRST so a regression surfaces before the
	// new behaviour is even exercised.
	if v.attempts[0][0] != "regression-case" {
		t.Errorf("regression tests should be run first; order was %v", v.attempts[0])
	}
	if !rep.Verified || rep.StoppedReason != StoppedConverged {
		t.Fatalf("build should have converged; rep=%+v", rep)
	}
	if len(rep.RegressionTests) != 2 {
		t.Fatalf("the green plan (regression + synthesized) must be returned for persistence; got %+v", rep.RegressionTests)
	}
}

// A build that never went green must NOT promote its tests to a regression
// suite — an unproven set would poison every later build.
func TestBuildUntilWorks_UnverifiedBuildReturnsNoRegressionSuite(t *testing.T) {
	rep := BuildUntilWorks(context.Background(), &alternatingLLM{}, cleanWorkflow(), Catalog{}, BuildOptions{
		MaxAttempts:     2,
		Verifier:        &alwaysFailVerifier{},
		RegressionTests: []TestCase{{Input: "regression-case"}},
	})
	if len(rep.RegressionTests) != 0 {
		t.Errorf("a failing build must not publish a regression suite; got %+v", rep.RegressionTests)
	}
}

// mergeTestPlan must not pay for the same case twice when a synthesized case
// happens to duplicate a carried-over one.
func TestMergeTestPlan_DedupesAndOrders(t *testing.T) {
	plan := mergeTestPlan(
		[]TestCase{{Input: "a"}, {Input: "b"}},
		[]TestCase{{Input: "b"}, {Input: "c"}},
	)
	got := make([]string, 0, len(plan))
	for _, tc := range plan {
		got = append(got, tc.Input)
	}
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("plan = %v, want %v", got, want)
		}
	}
	if mergeTestPlan(nil, nil) != nil {
		t.Errorf("an empty plan should stay nil so verifyAll's single no-input run still applies")
	}
}
