// buildloop.go — the autonomous build-verify-repair orchestrator (Architect
// pillar #1): the engine that makes a generated draft actually work, at any cost.
//
// Everything Studio learned to do in isolation — ground, validate against the
// live environment, deep-introspect tool calls, deterministically auto-wire,
// LLM-repair, and execute — is assembled here into a single self-driving loop
// with an attempt budget and a full, transparent transcript:
//
//	repeat up to MaxAttempts:
//	  1. deterministic repair (auto-wire args, reconcile vars, fix template typos)
//	  2. preflight against the live environment + deep tool-arg introspection
//	  3. if blockers → LLM-repair the EXACT problems, re-run; if no progress, stop
//	  4. once clean → VERIFY by actually running it (pluggable Verifier)
//	  5. if the run fails → repair against the REAL runtime error, re-run
//	  6. stop when a real run passes, or the budget/▢no-progress is hit
//
// The loop is pure orchestration over injected seams (an LLM and a Verifier), so
// it is fully unit-testable with fakes. The gateway supplies a real-execution
// Verifier so "verified" means the agent genuinely ran end-to-end against real
// tools — not that a mock said so.
package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/costs"
)

// SideEffectPolicy decides whether a build is allowed to touch the real world.
//
// This exists because "verify by running it" is the one part of the loop that
// can do something IRREVERSIBLE on the user's behalf — post to a channel, file a
// ticket, send an email — while the draft is still, by definition, not known to
// be correct. The failure mode we are preventing is a half-built agent spamming
// production during its own repair loop, potentially several times (once per
// attempt). So the policy is opt-IN, never opt-out.
type SideEffectPolicy string

const (
	// SideEffectsMocked runs verification against the mock walk (DryRunVerifier):
	// full structural + assertion confidence, zero side effects. This is the
	// DEFAULT and it is deliberately the ZERO VALUE of SideEffectPolicy, so a
	// caller that forgets to set BuildOptions.SideEffects cannot fire production
	// tools by accident. Adding a field must never make a system less safe.
	SideEffectsMocked SideEffectPolicy = "mocked"
	// SideEffectsReal executes the draft against real tools/Python. It must be
	// requested explicitly and consciously by the caller (in the gateway: an
	// operator ticking "run it for real").
	SideEffectsReal SideEffectPolicy = "real"
)

// StopReason names WHY the loop stopped, so the caller can tell "it works" apart
// from "we ran out of budget" apart from "we gave up". Before this existed, a
// budget exhaustion looked identical to a failed build in the report, which made
// it impossible for the GUI to offer the only useful next action ("give it more
// time / more attempts").
type StopReason string

const (
	// StoppedConverged — the draft passed (verified, or validated when no
	// verifier was requested). The only success value.
	StoppedConverged StopReason = "converged"
	// StoppedMaxAttempts — the attempt budget ran out.
	StoppedMaxAttempts StopReason = "max_attempts"
	// StoppedTimeBudget — BuildOptions.MaxElapsed was reached.
	StoppedTimeBudget StopReason = "time_budget"
	// StoppedTokenBudget — BuildOptions.MaxTokens was reached.
	StoppedTokenBudget StopReason = "token_budget"
	// StoppedCostBudget — BuildOptions.MaxCostUSD was reached.
	StoppedCostBudget StopReason = "cost_budget"
	// StoppedNoProgress — the loop detected it was repairing the same thing over
	// and over (or had no fix at all) and stopped rather than burn the budget.
	StoppedNoProgress StopReason = "no_progress"
)

// DefaultMaxElapsed is the wall-clock budget a build gets when
// BuildOptions.MaxElapsed is zero. Ten minutes is chosen to be comfortably
// longer than a legitimate slow build (a handful of attempts, each running real
// tools that may take tens of seconds) while still bounding the worst case: an
// unattended loop that keeps "making progress" forever holds an LLM budget, a
// gateway request and — under SideEffectsReal — a live tool connection open.
const DefaultMaxElapsed = 10 * time.Minute

// VerifyOutcome is the result of executing a draft once. OK reports success;
// when false, Error is the runtime failure to repair against. Trace is a short
// human-readable record of what happened, surfaced in the transcript. Real
// reports whether the run actually invoked tools (vs a mocked dry-run).
type VerifyOutcome struct {
	OK    bool     `json:"ok"`
	Error string   `json:"error,omitempty"`
	Trace []string `json:"trace,omitempty"`
	Real  bool     `json:"real"`
}

// Verifier executes a draft for one test case and reports the outcome. Two
// implementations exist: a mocked dry-run (DryRunVerifier) for structural
// confidence with zero side effects, and a real-execution verifier supplied by
// the gateway that runs the agent against real tools. The full TestCase is
// passed so the verifier can evaluate the case's assertions.
type Verifier interface {
	Verify(ctx context.Context, draft Draft, tc TestCase) VerifyOutcome
}

// TestCase is one self-test exercised during verification: an input plus the
// assertions that must hold. Assertions are evaluated by the verifier (the
// dry-run verifier uses EvaluateAssertions; a real verifier may check the final
// output). An empty Assertions slice means "just don't error".
type TestCase struct {
	Input      string      `json:"input"`
	Assertions []Assertion `json:"assertions,omitempty"`
}

// BuildOptions configures one autonomous build.
type BuildOptions struct {
	// MaxAttempts caps the loop. Zero defaults to 6.
	MaxAttempts int
	// In is the live-environment state preflight validates against.
	In PreflightInput
	// Verifier, when set, runs the draft after it passes preflight. When nil the
	// loop is validation-only (it stops once preflight is clean).
	Verifier Verifier
	// Tests are the self-tests to exercise during verification. When empty a
	// single no-input run is used.
	Tests []TestCase
	// OnEvent, when set, is called as the loop progresses so a caller can stream
	// live status to the user (each attempt starting, what it's repairing, when
	// it's running, the outcome). Nil-safe.
	OnEvent func(BuildEvent)
	// Trace, when set, durably records every phase of the loop (draft snapshots,
	// preflight, each repair, each verify, the final result) with timings and
	// structured detail, so a build is fully debuggable after the fact. Nil-safe:
	// all BuildTrace methods are no-ops on a nil trace.
	Trace *BuildTrace
	// ExtraProblems, when set, contributes additional deterministic problem lines
	// to each attempt's problem set (fed to the repair model). The gateway uses it
	// to add findings that need host access preflight can't do purely — e.g.
	// syntax-checking a python node with the interpreter. Nil-safe.
	ExtraProblems func(Draft) []string

	// SideEffects decides whether verification may touch the real world. The
	// ZERO VALUE ("") means SideEffectsMocked — deliberately, so a caller that
	// forgets this field gets the safe behaviour, never a production side effect.
	// Set it to SideEffectsReal to opt in to real execution.
	//
	// It also GOVERNS the Verifier field: a verifier that reports real side
	// effects (RealRunVerifier) is downgraded to the dry-run verifier unless the
	// policy is SideEffectsReal. That way the polarity can't be inverted again by
	// a call site that just hands the loop a real verifier and forgets the flag.
	SideEffects SideEffectPolicy
	// Runner, when set, lets the loop build the real verifier itself from the
	// engine primitives (see VerifierFor). It is only consulted when SideEffects
	// is SideEffectsReal and Verifier is nil, so supplying a Runner without the
	// policy still cannot cause a real run.
	Runner *RealRunner

	// MaxElapsed is the wall-clock budget for the whole build. Zero means
	// DefaultMaxElapsed (10 minutes). It is enforced BETWEEN attempts and is also
	// installed as a deadline on the context every attempt's LLM/verify calls
	// use, so a single wedged call can't outlive the budget either. Hitting it is
	// a clean stop reported as StoppedTimeBudget — not an error, not a crash.
	MaxElapsed time.Duration
	// MaxTokens caps total tokens consumed across all attempts. Zero = unlimited.
	// Requires a usage source (Usage, or an LLM implementing UsageReporter);
	// without one nothing is counted and the budget never trips.
	MaxTokens int
	// MaxCostUSD caps total estimated spend across all attempts. Zero = unlimited.
	// Same usage-source requirement as MaxTokens.
	MaxCostUSD float64
	// Usage, when set, reports CUMULATIVE token/cost consumption to date. The
	// loop samples it before and after each attempt and charges the difference to
	// that attempt. It reuses costs.UsageRecord — the repo's single accounting
	// type (internal/costs) — so a Studio budget is denominated in exactly the
	// units the cost store already persists, rather than a parallel counter that
	// would inevitably drift from the bill. When nil, an LLM implementing
	// UsageReporter is used instead; when neither is available, consumption
	// reports as zero and token/cost budgets are inert.
	Usage func() costs.UsageRecord

	// RegressionTests are self-tests carried over from EARLIER builds of this
	// draft. They are run on EVERY attempt in addition to the freshly synthesized
	// Tests, which is what makes a build a regression gate rather than a fresh
	// opinion each time: SynthesizeTests invents new cases per build, so without
	// this a repair could silently break something a previous build had proven.
	RegressionTests []TestCase
}

// UsageReporter is the optional accounting seam an LLM can implement so the
// build loop can meter it. Implementations return CUMULATIVE usage since the
// client was created; the loop only ever looks at differences. Reuses
// costs.UsageRecord rather than a Studio-local token struct so budgets and the
// persisted cost ledger can never disagree about what a token is.
type UsageReporter interface {
	Usage() costs.UsageRecord
}

// SideEffecting is implemented by verifiers that can cause REAL, irreversible
// side effects (RealRunVerifier). The loop uses it to refuse to run such a
// verifier unless the caller explicitly asked for SideEffectsReal. Verifiers
// that don't implement it are assumed side-effect-free, which is safe: the only
// thing that assumption grants is "you may run", and a mock running is harmless.
type SideEffecting interface {
	RealSideEffects() bool
}

// VerifierFor picks the verifier for a side-effect policy. This is the ONLY
// place the mocked/real choice is made, so there is exactly one line of code to
// audit for "can a build touch production?".
//
// The default path is unambiguous: an empty/unknown policy — including the zero
// value of SideEffectPolicy — yields the mocked DryRunVerifier. Real execution
// requires BOTH the explicit SideEffectsReal policy AND an injected RealRunner;
// asking for "real" without a runner falls back to mocked rather than silently
// producing a verifier that skips every step.
func VerifierFor(policy SideEffectPolicy, realRunner ...RealRunner) Verifier {
	if policy == SideEffectsReal && len(realRunner) > 0 {
		return RealRunVerifier{Runner: realRunner[0]}
	}
	return DryRunVerifier{}
}

// effectiveVerifier resolves which verifier this build actually runs with, and
// reports whether the caller's choice was downgraded for safety.
//
// Rules, in order:
//  1. No verifier and no runner → nil (validation-only build; unchanged
//     behaviour for callers that never asked for a run).
//  2. Policy is not "real" and the supplied verifier reports real side effects →
//     replaced by DryRunVerifier. THIS is the inverted-polarity fix: forgetting
//     the field downgrades you, it does not upgrade you.
//  3. Policy is "real" → the supplied verifier, or one built from Runner.
func (o BuildOptions) effectiveVerifier() (Verifier, SideEffectPolicy, bool) {
	policy := o.SideEffects
	if policy != SideEffectsReal {
		policy = SideEffectsMocked
	}
	if o.Verifier == nil && o.Runner == nil {
		return nil, policy, false
	}
	if policy == SideEffectsReal {
		if o.Verifier != nil {
			return o.Verifier, policy, false
		}
		return VerifierFor(SideEffectsReal, *o.Runner), policy, false
	}
	// Mocked policy. A runner alone never becomes a real verifier.
	if o.Verifier == nil {
		return DryRunVerifier{}, policy, false
	}
	if se, ok := o.Verifier.(SideEffecting); ok && se.RealSideEffects() {
		return DryRunVerifier{}, policy, true
	}
	return o.Verifier, policy, false
}

// BuildEvent is one live progress update emitted during BuildUntilWorks. Kind is
// "attempt" | "repair" | "verify" | "result". Message is a short, plain-language
// line ready to show the user.
type BuildEvent struct {
	Kind    string `json:"kind"`
	Attempt int    `json:"attempt"`
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message"`
}

// BuildAttempt records one pass of the loop for the transcript.
type BuildAttempt struct {
	N        int      `json:"n"`
	Phase    string   `json:"phase"`    // "repair" | "verify"
	Problems []string `json:"problems"` // what was wrong at the start of this attempt
	Action   string   `json:"action"`   // what the loop did about it
	Changed  bool     `json:"changed"`  // whether the draft was modified
	OK       bool     `json:"ok"`       // whether this attempt left the draft passing
	// StartedAt / Elapsed let the UI show progress while a build is running and,
	// afterwards, show WHERE the time went — a build that spends 8 of its 10
	// minutes in one verify is a slow tool, not a slow loop, and the operator
	// can only tell those apart with per-attempt timings.
	StartedAt time.Time     `json:"started_at,omitempty"`
	Elapsed   time.Duration `json:"elapsed_ns,omitempty"`
	// Tokens / CostUSD are this attempt's share of the budget (the difference
	// between the usage snapshots taken around it). Zero when no usage source is
	// wired.
	Tokens  int     `json:"tokens,omitempty"`
	CostUSD float64 `json:"cost_usd,omitempty"`
}

// BuildReport is the full result of an autonomous build: the final draft, a
// verdict, and the complete attempt transcript so the user sees exactly what was
// wrong and how each problem was fixed.
type BuildReport struct {
	Workflow Draft          `json:"workflow"`
	OK       bool           `json:"ok"`       // preflight clean (and verified, if a verifier ran)
	Verified bool           `json:"verified"` // a real/dry run actually passed
	Attempts []BuildAttempt `json:"attempts"`
	Summary  string         `json:"summary"`
	Contract ContractResult `json:"contract"`
	Residual []string       `json:"residual,omitempty"` // problems still unresolved at the end
	// Changes is a plain-language rollup of what the loop actually altered
	// across all attempts. Story 2b (Cohort C): the story says "'Build until
	// it works' explains what it changed" — the attempts array carries the
	// raw record; Changes is the "at-a-glance" summary the GUI shows above
	// the attempt log so the operator doesn't have to scan every row.
	Changes []string `json:"changes,omitempty"`
	// NeedsExternal, when set, tells the operator the loop stopped because
	// it needs input a machine can't provide: a credential, a decision, an
	// external API's rate reset. Extracted from the residual list so the GUI
	// can render it as a call-to-action rather than a generic error.
	NeedsExternal []string `json:"needs_external,omitempty"`
	// Diagnosis, when set, is a plain-language explanation of WHY the build could
	// not be fixed automatically — and what to do about it. It is set when the
	// loop detects it is repairing the same failure over and over (cosmetic edits
	// that don't converge), most importantly when a fixed flow is fighting a
	// reasoning task and the answer is to switch to an agent instead.
	Diagnosis string `json:"diagnosis,omitempty"`
	// SuggestMode, when non-empty ("auto"|"plan_execute"|"react"), tells the GUI the loop
	// believes this task should be authored as an agent in that mode, so it can
	// offer a one-click switch. Embodies "Studio figures it out, not the user."
	SuggestMode string `json:"suggest_mode,omitempty"`

	// StoppedReason says WHY the loop ended: StoppedConverged on success, or one
	// of the budget/no-progress reasons. Without it, "we ran out of time" and "we
	// couldn't fix it" are indistinguishable in the report, and the GUI can't
	// offer the right next step.
	StoppedReason StopReason `json:"stopped_reason,omitempty"`
	// SideEffects records the policy the build actually ran under, so the report
	// itself answers "did this touch production?" — including when the loop
	// downgraded a real verifier because the caller didn't opt in.
	SideEffects SideEffectPolicy `json:"side_effects,omitempty"`
	// Elapsed / TokensUsed / CostUSD are what the build consumed in total.
	Elapsed    time.Duration `json:"elapsed_ns,omitempty"`
	TokensUsed int           `json:"tokens_used,omitempty"`
	CostUSD    float64       `json:"cost_usd,omitempty"`
	// RegressionTests is the test set that was green at the end of a converged
	// build (carried-over regressions + this build's synthesized tests, deduped).
	// The caller persists it onto the draft and feeds it back as
	// BuildOptions.RegressionTests next time, which is what turns each build into
	// a gate over everything previous builds proved. Empty unless the build
	// converged — an unproven set must never be promoted to a regression suite.
	RegressionTests []TestCase `json:"regression_tests,omitempty"`
}

// BuildUntilWorks drives the autonomous build-verify-repair loop over an
// already-generated draft. It returns a BuildReport with the best draft it could
// reach and a transparent transcript of every attempt. It never panics on a bad
// model or verifier — failures degrade to a report explaining what's still wrong.
func BuildUntilWorks(ctx context.Context, llm LLM, draft Draft, cat Catalog, opts BuildOptions) BuildReport {
	max := opts.MaxAttempts
	if max <= 0 {
		max = 6
	}
	maxElapsed := opts.MaxElapsed
	if maxElapsed <= 0 {
		maxElapsed = DefaultMaxElapsed
	}
	// The verifier is chosen from the side-effect POLICY, never from a bare flag
	// on the call site. A real verifier handed in without SideEffectsReal is
	// downgraded here (see effectiveVerifier) so a forgotten field can't fire
	// production tools.
	verifier, policy, downgraded := opts.effectiveVerifier()

	rep := BuildReport{Workflow: draft, SideEffects: policy}
	tr := opts.Trace // nil-safe throughout
	emit := func(ev BuildEvent) {
		tr.Event(ev) // durably record every user-facing progress line
		if opts.OnEvent != nil {
			opts.OnEvent(ev)
		}
	}

	// Budget bookkeeping. The deadline is ABSOLUTE, so the single context below
	// gives every attempt exactly the budget that is still left — an attempt
	// started with two minutes remaining gets two minutes, not a fresh ten.
	start := time.Now()
	deadline := start.Add(maxElapsed)
	budgetCtx, cancelBudget := context.WithDeadline(ctx, deadline)
	defer cancelBudget()
	usageNow := usageSampler(opts, llm)
	baseUsage := usageNow()

	// tests is the plan run on EVERY attempt: carried-over regressions first (a
	// repair must not break what an earlier build already proved), then this
	// build's synthesized cases.
	tests := mergeTestPlan(opts.RegressionTests, opts.Tests)

	tr.Logd("phase", "loop", 0, "build-verify-repair loop starting", map[string]any{
		"max_attempts": max, "verify": verifier != nil, "tests": len(tests),
		"regression_tests": len(opts.RegressionTests),
		"side_effects":     string(policy), "max_elapsed": maxElapsed.String(),
		"max_tokens": opts.MaxTokens, "max_cost_usd": opts.MaxCostUSD,
	})
	if downgraded {
		// Loud, durable, and in the user-facing transcript: silently neutering a
		// verifier would otherwise look like "the build passed for real".
		msg := "Verification will run MOCKED (no side effects) — real execution was not explicitly requested."
		emit(BuildEvent{Kind: "verify", Phase: "verify", Message: msg})
		tr.Logd("phase", "verify", 0, msg, map[string]any{"side_effects": string(policy)})
	}

	// stopped is the loop's verdict. It stays empty until something decides;
	// falling out of the for-loop without one means the attempt budget ran out.
	var stopped StopReason
	// Per-attempt accounting, stamped onto every attempt by record().
	attemptStart := start
	tok0, cost0 := baseUsage.TotalTokens, baseUsage.CostUSD
	record := func(att BuildAttempt) {
		u := usageNow()
		att.StartedAt = attemptStart
		att.Elapsed = time.Since(attemptStart)
		att.Tokens = u.TotalTokens - tok0
		att.CostUSD = u.CostUSD - cost0
		rep.Attempts = append(rep.Attempts, att)
	}

	verified := false
	preflightClean := false
	// Non-convergence tracking for the VERIFY phase. We distinguish two cases so a
	// legitimately-progressing repair isn't cut off after a single pass:
	//   • seenExact — the IDENTICAL runtime error text recurred ⇒ the last repair
	//     changed nothing that mattered ⇒ stop now (truly stuck).
	//   • sigCount — the same coarse error CLASS (node|kind) recurred but with
	//     different specifics (e.g. fixed arg A, now arg B of the same tool) ⇒ that
	//     is progress; allow up to maxRepairsPerClass passes before giving up.
	seenExact := map[string]bool{}
	sigCount := map[string]int{}
	const maxRepairsPerClass = 2
	// seenProblemSets does the same for the PREFLIGHT/repair phase: if the exact
	// same blocker set comes back after a repair claimed to change the draft, the
	// loop isn't converging — stop rather than re-repairing the same problems.
	seenProblemSets := map[string]bool{}

	for n := 1; n <= max; n++ {
		// Budgets are checked BEFORE committing to another attempt: an attempt is
		// the expensive unit (an LLM repair plus a full verify run), so the only
		// useful place to stop is at its boundary. Stopping here is a clean,
		// reported outcome — the report keeps the best draft reached so far.
		if reason := budgetExceeded(opts, start, maxElapsed, usageNow(), baseUsage); reason != "" {
			stopped = reason
			emit(BuildEvent{Kind: "result", Attempt: n, Phase: "budget", Message: budgetMessage(reason)})
			tr.Logd("result", "budget", n, budgetMessage(reason), map[string]any{
				"reason": string(reason), "elapsed": time.Since(start).String(),
			})
			break
		}
		attemptStart = time.Now()
		u0 := usageNow()
		tok0, cost0 = u0.TotalTokens, u0.CostUSD

		emit(BuildEvent{Kind: "attempt", Attempt: n, Message: fmt.Sprintf("Attempt %d — checking the draft against your setup…", n)})
		tr.Snapshot("attempt-start", n, rep.Workflow)

		// (1) Deterministic repair first — free, and it shrinks the problem set
		// the model has to reason about.
		RepairWiring(&rep.Workflow, cat)

		// (2) Validate against the live environment + deep tool introspection.
		donePf := tr.Step("preflight", "repair", n, "preflight against the live environment")
		pf := Preflight(rep.Workflow, opts.In)
		problems := buildProblemSet(pf, rep.Workflow, cat)
		if opts.ExtraProblems != nil {
			problems = append(problems, opts.ExtraProblems(rep.Workflow)...)
		}
		donePf(nil, map[string]any{
			"blockers": len(pf.Blockers), "warnings": len(pf.Warnings),
			"problems": problems,
		})

		if len(problems) > 0 {
			emit(BuildEvent{Kind: "repair", Attempt: n, Phase: "repair", Message: fmt.Sprintf("Found %s — repairing…", plural(len(problems), "problem"))})
			att := BuildAttempt{N: n, Phase: "repair", Problems: problems}

			// Repair non-convergence: if this EXACT blocker set already came back
			// after a prior repair, editing the flow isn't resolving it. Stop —
			// and if the residual is reasoning-fit (a fixed flow carrying an agent
			// task), recommend an agent mode instead of looping to the attempt budget.
			problemSig := strings.Join(dedupeStrings(problems), "\n")
			if seenProblemSets[problemSig] {
				att.Action = "same problems recurred after repair — stopping (not converging)"
				att.Changed = false
				record(att)
				stopped = StoppedNoProgress
				rep.Residual = problems
				if problemsAreReasoningFit(problems) {
					rep.Diagnosis, rep.SuggestMode = reasoningFitDiagnosis()
					emit(BuildEvent{Kind: "result", Attempt: n, Phase: "repair", Message: rep.Diagnosis})
				} else {
					emit(BuildEvent{Kind: "result", Attempt: n, Phase: "repair", Message: "Couldn't fix it automatically — see remaining problems."})
				}
				tr.Logd("repair", "repair", n, att.Action, map[string]any{"problems": problems, "suggest_mode": rep.SuggestMode})
				break
			}
			seenProblemSets[problemSig] = true

			// (3) Repair the exact problems. Try the general full-draft LLM repair
			// first; fall back to a deterministic focused repair if the model
			// can't help. If neither makes progress, stop — looping is pointless.
			repaired, changed := RepairWithProblems(budgetCtx, llm, rep.Workflow, problems, cat)
			if changed {
				RepairWiring(&repaired, cat)
				// S6 (Cohort F): the LLM repair MUST NOT sneak in a
				// privileged tool or a shared-channel exposure the
				// operator didn't already have in their draft. If the
				// repair added one, revert to the pre-repair draft and
				// record the block so the surface tells the operator
				// what happened.
				if blocked, reason := detectPrivilegedRegression(rep.Workflow, repaired); blocked {
					att.Action = "LLM repair blocked: " + reason
					att.Changed = false
					record(att)
					stopped = StoppedNoProgress
					rep.Residual = problems
					emit(BuildEvent{Kind: "result", Attempt: n, Phase: "repair", Message: att.Action})
					tr.Logd("repair", "security", n, att.Action, map[string]any{"reason": reason})
					break
				}
				rep.Workflow = repaired
				att.Action = "LLM repair of " + plural(len(problems), "problem")
				att.Changed = true
			} else if focusedRepair(budgetCtx, llm, &rep.Workflow) {
				att.Action = "focused repair of broken steps"
				att.Changed = true
			} else {
				att.Action = "no automated fix available — stopping"
				att.Changed = false
				record(att)
				stopped = StoppedNoProgress
				rep.Residual = problems
				// If the unfixable blockers are reasoning-fit, this flow can't be
				// repaired in workflow mode — point the user at an agent mode.
				if problemsAreReasoningFit(problems) {
					rep.Diagnosis, rep.SuggestMode = reasoningFitDiagnosis()
					emit(BuildEvent{Kind: "result", Attempt: n, Phase: "repair", Message: rep.Diagnosis})
				} else {
					emit(BuildEvent{Kind: "result", Attempt: n, Phase: "repair", Message: "Couldn't fix it automatically — see remaining problems."})
				}
				tr.Logd("repair", "repair", n, att.Action, map[string]any{"problems": problems, "changed": false, "suggest_mode": rep.SuggestMode})
				break
			}
			emit(BuildEvent{Kind: "repair", Attempt: n, Phase: "repair", Message: "✓ " + att.Action})
			tr.Logd("repair", "repair", n, att.Action, map[string]any{"problems": problems, "changed": att.Changed})
			record(att)
			continue
		}

		// Preflight is clean.
		preflightClean = true

		// (4) No verifier → validation-only build is done.
		if verifier == nil {
			record(BuildAttempt{
				N: n, Phase: "verify", Action: "validation passed (no execution requested)", OK: true,
			})
			stopped = StoppedConverged
			break
		}

		// (4) Verify by actually running it. Every attempt runs the FULL plan —
		// carried-over regression tests included — so a repair that fixes today's
		// failure by breaking something a previous build proved is caught here
		// rather than by the user in production.
		emit(BuildEvent{Kind: "verify", Attempt: n, Phase: "verify", Message: "Validation passed — running it to verify…"})
		doneV := tr.Step("verify", "verify", n, "running the agent to verify it works")
		out := verifyAll(budgetCtx, verifier, rep.Workflow, tests)
		doneV(errOrNil(out.Error), map[string]any{
			"ok": out.OK, "real": out.Real, "error": out.Error, "run_trace": out.Trace,
		})
		att := BuildAttempt{N: n, Phase: "verify", OK: out.OK}
		if out.OK {
			att.Action = runWord(out.Real) + " succeeded"
			att.OK = true
			emit(BuildEvent{Kind: "result", Attempt: n, Phase: "verify", Message: "✓ " + att.Action})
			record(att)
			verified = true
			stopped = StoppedConverged
			// The plan just went green end to end, so it is a legitimate
			// regression suite for the NEXT build of this draft.
			rep.RegressionTests = tests
			break
		}

		// (5) Runtime failure. Stop only when the loop is genuinely not converging:
		// the identical error came back (last repair was cosmetic), or the same
		// error class has now failed more than maxRepairsPerClass times. A class
		// recurring with NEW specifics is progress and gets another pass.
		sig := errSignature(out.Error)
		if seenExact[out.Error] || sigCount[sig] >= maxRepairsPerClass {
			att.Action = "same failure recurred after repair — stopping (not converging)"
			att.Problems = []string{out.Error}
			rep.Residual = []string{out.Error}
			rep.Diagnosis, rep.SuggestMode = nonConvergenceDiagnosis(out.Error)
			emit(BuildEvent{Kind: "result", Attempt: n, Phase: "verify", Message: rep.Diagnosis})
			tr.Logd("repair", "verify", n, att.Action, map[string]any{"runtime_error": out.Error, "signature": sig, "suggest_mode": rep.SuggestMode})
			record(att)
			stopped = StoppedNoProgress
			break
		}
		seenExact[out.Error] = true
		sigCount[sig]++

		emit(BuildEvent{Kind: "verify", Attempt: n, Phase: "verify", Message: "Run failed: " + out.Error + " — repairing…"})
		att.Problems = []string{out.Error}
		repaired, changed := RepairWithProblems(budgetCtx, llm, rep.Workflow,
			[]string{"At RUN TIME the agent failed with this error — change it so this cannot happen again: " + out.Error}, cat)
		if changed {
			RepairWiring(&repaired, cat)
			rep.Workflow = repaired
			att.Action = "repaired against runtime error"
			att.Changed = true
			tr.Logd("repair", "verify", n, att.Action, map[string]any{"runtime_error": out.Error, "changed": true})
			record(att)
			continue
		}
		att.Action = "runtime error persists — no automated fix available"
		tr.Logd("repair", "verify", n, att.Action, map[string]any{"runtime_error": out.Error, "changed": false})
		record(att)
		stopped = StoppedNoProgress
		rep.Residual = []string{out.Error}
		// Even on the first try, a handoff/parse error is a flow-vs-reasoning
		// mismatch the loop can't fix by editing the flow — recommend agent mode.
		if isHandoffReasoningError(out.Error) {
			rep.Diagnosis, rep.SuggestMode = nonConvergenceDiagnosis(out.Error)
			emit(BuildEvent{Kind: "result", Attempt: n, Phase: "verify", Message: rep.Diagnosis})
		}
		break
	}

	// Falling out of the for-loop with no verdict means every attempt was spent.
	if stopped == "" {
		stopped = StoppedMaxAttempts
	}
	rep.StoppedReason = stopped
	rep.Verified = verified
	rep.OK = preflightClean && (verifier == nil || verified)
	final := usageNow()
	rep.Elapsed = time.Since(start)
	rep.TokensUsed = final.TotalTokens - baseUsage.TotalTokens
	rep.CostUSD = final.CostUSD - baseUsage.CostUSD
	rep.Contract = AssessContract(rep.Workflow, cat, opts.In)
	rep.Changes = summarizeBuildChanges(rep.Attempts)
	rep.NeedsExternal = extractExternalBlockers(rep.Residual)
	rep.Summary = buildSummary(rep)
	tr.Snapshot("final", 0, rep.Workflow)
	tr.Logd("result", "done", 0, rep.Summary, map[string]any{
		"ok": rep.OK, "verified": rep.Verified, "stopped_reason": string(rep.StoppedReason),
		"attempts": len(rep.Attempts), "residual": rep.Residual,
		"elapsed": rep.Elapsed.String(), "tokens_used": rep.TokensUsed, "cost_usd": rep.CostUSD,
		"side_effects": string(rep.SideEffects),
	})
	return rep
}

// usageSampler returns a function reporting CUMULATIVE token/cost consumption.
// It prefers the caller's explicit hook (the gateway's LLM is wrapped in
// routers/decorators, so the concrete client is often not reachable), then the
// LLM itself if it implements UsageReporter, and finally a zero source. A zero
// source is not an error: it just means token/cost budgets are inert, which is
// the honest behaviour — refusing to build because we can't meter would be worse
// than building without a meter.
func usageSampler(opts BuildOptions, llm LLM) func() costs.UsageRecord {
	if opts.Usage != nil {
		return opts.Usage
	}
	if r, ok := llm.(UsageReporter); ok && r != nil {
		return r.Usage
	}
	return func() costs.UsageRecord { return costs.UsageRecord{} }
}

// budgetExceeded reports which budget (if any) is spent. Returns "" when the
// loop may continue. Zero MaxTokens/MaxCostUSD mean unlimited; MaxElapsed has
// already been defaulted by the caller.
func budgetExceeded(opts BuildOptions, start time.Time, maxElapsed time.Duration, now, base costs.UsageRecord) StopReason {
	if time.Since(start) >= maxElapsed {
		return StoppedTimeBudget
	}
	if opts.MaxTokens > 0 && now.TotalTokens-base.TotalTokens >= opts.MaxTokens {
		return StoppedTokenBudget
	}
	if opts.MaxCostUSD > 0 && now.CostUSD-base.CostUSD >= opts.MaxCostUSD {
		return StoppedCostBudget
	}
	return ""
}

// budgetMessage is the plain-language line shown when a budget stops the build.
// It names the budget explicitly because the only useful operator action —
// raise that budget and re-run — depends on knowing which one ran out.
func budgetMessage(reason StopReason) string {
	switch reason {
	case StoppedTimeBudget:
		return "Stopped — the build reached its time budget. The best draft so far is kept; raise the time budget to keep going."
	case StoppedTokenBudget:
		return "Stopped — the build reached its token budget. The best draft so far is kept; raise the token budget to keep going."
	case StoppedCostBudget:
		return "Stopped — the build reached its cost budget. The best draft so far is kept; raise the cost budget to keep going."
	}
	return "Stopped — the build reached a budget limit."
}

// mergeTestPlan builds the per-attempt test plan: carried-over regression tests
// FIRST (they encode what already worked, so a regression surfaces before the
// new behaviour is even exercised), then this build's synthesized tests, with
// exact duplicates removed so a case isn't paid for twice every attempt.
func mergeTestPlan(regression, synthesized []TestCase) []TestCase {
	if len(regression) == 0 && len(synthesized) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(regression)+len(synthesized))
	out := make([]TestCase, 0, len(regression)+len(synthesized))
	add := func(cases []TestCase) {
		for _, tc := range cases {
			key, err := json.Marshal(tc)
			if err != nil {
				// Unmarshalable case: keep it rather than drop a test.
				out = append(out, tc)
				continue
			}
			if seen[string(key)] {
				continue
			}
			seen[string(key)] = true
			out = append(out, tc)
		}
	}
	add(regression)
	add(synthesized)
	return out
}

// errSignature reduces a runtime error to a stable class so the loop can tell
// when it is repairing the same failure over and over. It keeps the node id and
// the structural SHAPE of the error but drops the specific field name / offsets
// the model keeps permuting (.parsed.skill → .parsed.skill_name → …), so two
// "different" errors that are really the same root cause collapse to one key.
func errSignature(msg string) string {
	s := strings.ToLower(msg)
	node := ""
	if i := strings.Index(s, `node "`); i >= 0 {
		if j := strings.Index(s[i+6:], `"`); j >= 0 {
			node = s[i+6 : i+6+j]
		}
	}
	kind := "runtime"
	switch {
	case strings.Contains(s, "can't evaluate field") && strings.Contains(s, "interface {}"):
		kind = "template-field-on-untyped"
	case strings.Contains(s, "execute template"), strings.Contains(s, "render input"):
		kind = "template-render"
	case strings.Contains(s, "unmarshal"), strings.Contains(s, "invalid character") && strings.Contains(s, "json"):
		kind = "json-parse"
	}
	return node + "|" + kind
}

// isHandoffReasoningError reports whether a runtime error is the signature of a
// FIXED FLOW trying to mechanically read structure out of an upstream step's
// FREE-FORM output (an LLM/agent reply, typed interface{}): template-field /
// template-render / json-parse failures. These don't get fixed by renaming the
// field — the upstream has no guaranteed shape. The task is a reasoning task.
func isHandoffReasoningError(msg string) bool {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(msg, "can't evaluate field") && strings.Contains(msg, "interface {}"):
		return true
	case strings.Contains(msg, "execute template"), strings.Contains(msg, "render input"):
		return true
	case strings.Contains(low, "unmarshal"):
		return true
	// Preflight phrasing: a fixed step references a variable (the user's message,
	// conversation context, a channel, …) that no upstream step can produce. In a
	// fixed graph there is nowhere for that value to come from — it's inherently a
	// reasoning-agent input. This is the dominant signature of a flow miscast from
	// an agent task (the finance-assistant case).
	case strings.Contains(low, "no earlier step produces"):
		return true
	}
	return false
}

// problemsAreReasoningFit reports whether the residual build problems are, in the
// main, the signature of a fixed flow carrying a reasoning task (steps wired to
// values nothing produces, template/parse handoff failures). When they are, no
// amount of flow editing will fix it — the right move is an Auto/Plan-Execute agent.
func problemsAreReasoningFit(problems []string) bool {
	if len(problems) == 0 {
		return false
	}
	hits := 0
	for _, p := range problems {
		if isHandoffReasoningError(p) {
			hits++
		}
	}
	return hits*2 >= len(problems) // a majority are reasoning-fit blockers
}

// reasoningFitDiagnosis is the verdict shown when the residual blockers say the
// task belongs in an agent, not a fixed flow. Returns the message + suggest mode.
func reasoningFitDiagnosis() (diagnosis, suggestMode string) {
	return "These steps reference values — like the user's message, the conversation context, or the channel — that no fixed step can produce. The workflow is being asked to route and reason over free-form input, which is an agent's job, not a fixed graph. Switch to an Auto tool-calling agent so capable models can use native tool calling; use Plan-Execute for longer multi-phase work.", "auto"
}

// nonConvergenceDiagnosis explains why the loop gave up and what to do about it.
// When the recurring failure is a handoff/parse error, the verdict is decisive:
// this is a reasoning task wrongly cast as a fixed pipeline — author it as an
// Auto/Plan-Execute agent and let the agent pick the skill/tool per query. Returns the
// human-readable diagnosis plus a suggested mode for the GUI's one-click switch.
func nonConvergenceDiagnosis(err string) (diagnosis, suggestMode string) {
	if isHandoffReasoningError(err) {
		return "This step keeps failing because a fixed workflow is trying to read fields out of an upstream step's free-form output (an LLM or agent reply), which has no guaranteed shape — each repair just renames the field and it breaks again. This is an agent task, not a fixed pipeline: switch it to Auto so the agent can choose the skill/tool per request, or Plan-Execute for longer multi-phase work.", "auto"
	}
	return "The same failure recurred after repair, so automatic fixing stopped to avoid looping. Review the run error above and adjust the step, or try authoring this as an Auto agent if the flow is fighting free-form data.", ""
}

// errOrNil adapts a string error field to an error for the trace Step closure.
func errOrNil(msg string) error {
	if msg == "" {
		return nil
	}
	return fmt.Errorf("%s", msg)
}

// buildProblemSet turns a preflight report into the concrete problem lines the
// repair loop acts on: the BLOCKERS only (a required argument left empty, a
// dangling reference, a missing entry, …) — the things that are definitely wrong.
//
// Deep-introspection argument WARNINGS ("argument X is not accepted by tool Y")
// are deliberately NOT included. They compare a node's args against the MCP
// server's *published* schema, which is frequently incomplete — a tool routinely
// accepts arguments it never advertises (e.g. NotebookLM's `max_wait` / `task_id`,
// confirmed working in real runs). Feeding those false positives to the loop sends
// it chasing phantom errors it can't resolve, burning its whole attempt budget.
// Real verification is the authority: if an argument is genuinely rejected, the
// tool errors when the verify phase runs it, and the loop repairs against that
// real error. So warnings still inform the user (Studio validation surfaces them)
// but never drive the autonomous build. (Pure-config warnings — missing channel
// token, unattended tradeoff — are likewise excluded: they need the user.)
func buildProblemSet(pf PreflightResult, draft Draft, cat Catalog) []string {
	var out []string
	for _, b := range pf.Blockers {
		out = append(out, issueLine(b))
	}
	return dedupeStrings(out)
}

// issueLine renders a preflight issue as a single repair instruction.
func issueLine(i PreflightIssue) string {
	out := i.Message
	if i.NodeID != "" {
		out = "step \"" + i.NodeID + "\": " + out
	}
	if i.Fix != "" {
		out += " (" + i.Fix + ")"
	}
	return out
}

// verifyAll runs every test case through the verifier and returns the first
// failure (so the loop repairs one concrete error at a time), or an aggregate
// success. With no tests, a single empty-input run is performed.
func verifyAll(ctx context.Context, v Verifier, draft Draft, tests []TestCase) VerifyOutcome {
	if len(tests) == 0 {
		tests = []TestCase{{}}
	}
	var anyReal bool
	var trace []string
	for i, tc := range tests {
		out := v.Verify(ctx, draft, tc)
		anyReal = anyReal || out.Real
		trace = append(trace, out.Trace...)
		if !out.OK {
			out.Trace = trace
			if out.Error == "" {
				out.Error = fmt.Sprintf("test %d failed with no error detail", i+1)
			}
			return out
		}
	}
	return VerifyOutcome{OK: true, Real: anyReal, Trace: trace}
}

func runWord(real bool) string {
	if real {
		return "real run"
	}
	return "dry run"
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// buildSummary writes a one-line plain-language verdict for the report header.
func buildSummary(rep BuildReport) string {
	switch {
	case rep.OK && rep.Verified:
		return fmt.Sprintf("Built and verified by running it — %s.", attemptsPhrase(rep))
	case rep.OK:
		return fmt.Sprintf("Validated clean against your setup — %s. Run it to verify end-to-end.", attemptsPhrase(rep))
	case rep.StoppedReason == StoppedTimeBudget,
		rep.StoppedReason == StoppedTokenBudget,
		rep.StoppedReason == StoppedCostBudget:
		// A budget stop is NOT a failed build — surfacing it as one would send
		// the operator hunting for a defect that isn't there.
		return budgetMessage(rep.StoppedReason)
	case rep.Diagnosis != "":
		return rep.Diagnosis
	case len(rep.NeedsExternal) > 0:
		// Stopping condition the story explicitly calls out: hand the operator
		// exactly what they need to unblock, not a raw residual dump.
		return "Stopped — needs your input: " + rep.NeedsExternal[0]
	case len(rep.Residual) > 0:
		return "Could not fully fix it automatically. Remaining: " + strings.Join(rep.Residual, "; ")
	default:
		return "Stopped without a clean build; see the attempt log."
	}
}

func attemptsPhrase(rep BuildReport) string {
	repairs := 0
	for _, a := range rep.Attempts {
		if a.Phase == "repair" && a.Changed {
			repairs++
		}
	}
	if repairs == 0 {
		return "no repairs were needed"
	}
	return "after " + plural(repairs, "automatic fix")
}

// summarizeBuildChanges rolls the attempt log up into a plain-language
// "what actually changed" list, so the GUI can render one bullet per
// meaningful edit rather than making the operator scan every attempt.
// Story 2b (Cohort C): closes Story 2's leftover S ("'Build until it works'
// explains what it changed and stops when external credentials/input are
// required").
func summarizeBuildChanges(attempts []BuildAttempt) []string {
	if len(attempts) == 0 {
		return nil
	}
	out := make([]string, 0, len(attempts))
	for i, a := range attempts {
		if !a.Changed {
			continue
		}
		action := strings.TrimSpace(a.Action)
		if action == "" {
			continue
		}
		// The action string already reads as a plain-language sentence
		// (e.g. "Repaired shape drift on node fetch_url"). Prefix with the
		// attempt number so the operator can cross-reference the log.
		out = append(out, fmt.Sprintf("Attempt %d — %s", i+1, action))
	}
	return dedupeStrings(out)
}

// extractExternalBlockers filters the residual list for problems that CANNOT
// be fixed by another repair pass — they need an operator to act (paste a
// credential, wait for a rate reset, invite a bot to a channel). The classifier
// is intentionally conservative — anything that doesn't match falls through
// to the generic Residual bucket the GUI already renders.
func extractExternalBlockers(residual []string) []string {
	if len(residual) == 0 {
		return nil
	}
	var out []string
	for _, r := range residual {
		lc := strings.ToLower(r)
		switch {
		case strings.Contains(lc, "credential"), strings.Contains(lc, "api key"), strings.Contains(lc, "token"), strings.Contains(lc, "unauthorized"), strings.Contains(lc, "401"), strings.Contains(lc, "403"):
			out = append(out, "Needs a credential or token — the loop can't fix this without you pasting one: "+r)
		case strings.Contains(lc, "not_in_channel"), strings.Contains(lc, "not in channel"), strings.Contains(lc, "bot has not been added"):
			out = append(out, "Needs the bot invited to the channel: "+r)
		case strings.Contains(lc, "rate"), strings.Contains(lc, "429"), strings.Contains(lc, "quota"):
			out = append(out, "Needs the provider's rate window to reset before retry: "+r)
		case strings.Contains(lc, "chat not found"), strings.Contains(lc, "channel_not_found"), strings.Contains(lc, "invalid destination"):
			out = append(out, "Needs a valid destination — verify the channel/chat id: "+r)
		}
	}
	return dedupeStrings(out)
}

// dedupeStrings removes duplicate lines, preserving order.
func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
