package studio

// generatepipeline.go — Story 9 M (Cohort C). The Studio generate flow used
// to be a single opaque click that ran refine → compile → validate → repair
// inside two sequential POSTs, with intermediate phases invisible to the
// operator. This file exposes the pipeline as 5 discrete phases so the
// gateway can stream each boundary as an SSE event: `clarify_intent →
// choose_strategy → build_graph → validate → repair`.
//
// The orchestrator is single-shot from the client's point of view (streamed
// mode) but the GUI can also buffer the events and reveal them one at a time
// on Continue (wizard mode). No separate wizard endpoints — the same code
// path serves both surfaces.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// emitNodes announces each node of a freshly built draft, in declaration order,
// so a client can render the graph as it arrives. Structural endpoint blocks
// (trigger/exit) are included because the canvas draws them too — omitting them
// would make the streamed graph differ from the finished one.
func emitNodes(emit func(PipelineEvent), d Draft) {
	total := len(d.Flow.Nodes)
	for i, n := range d.Flow.Nodes {
		label := n.Description
		if strings.TrimSpace(label) == "" {
			label = n.Tool
		}
		if strings.TrimSpace(label) == "" {
			label = n.Agent
		}
		emit(PipelineEvent{
			Phase:   PhaseBuildGraph,
			Status:  StatusNode,
			Message: n.ID,
			Payload: map[string]any{
				"id": n.ID, "kind": n.Kind, "label": label,
				"index": i + 1, "total": total,
			},
		})
	}
}

// PipelineEventKind is the discrete phase identifier used on the wire.
type PipelineEventKind string

const (
	PhaseClarifyIntent  PipelineEventKind = "clarify_intent"
	PhaseChooseStrategy PipelineEventKind = "choose_strategy"
	PhaseBuildGraph     PipelineEventKind = "build_graph"
	PhaseValidate       PipelineEventKind = "validate"
	PhaseRepair         PipelineEventKind = "repair"
)

// PipelineStatus is the position of the event inside a phase.
type PipelineStatus string

const (
	StatusStart    PipelineStatus = "start"
	StatusComplete PipelineStatus = "complete"
	StatusSkip     PipelineStatus = "skip"
	StatusError    PipelineStatus = "error"
	// StatusNode reports ONE constructed node inside build_graph, so the canvas
	// fills in progressively instead of appearing whole at the end. See the note
	// on emitNodes about what this does and does not claim.
	StatusNode PipelineStatus = "node"
	// StatusCancelled is the operator stopping the run. Distinct from an error:
	// nothing went wrong, and the partial draft is still worth keeping.
	StatusCancelled PipelineStatus = "cancelled"
)

// PipelineSource attributes an event to whoever actually did the work. ST-04
// requires the transcript to distinguish deterministic planner actions from LLM
// prompt refinement — without it a user cannot tell which parts of their
// workflow a model chose and which were decided by rules.
type PipelineSource string

const (
	SourcePlanner PipelineSource = "planner"
	SourceLLM     PipelineSource = "llm"
)

// PipelineEvent is the on-wire shape emitted between phases so the GUI can
// render a live-transcript row or a wizard step card. Payload carries the
// per-phase intermediate output the operator would want to inspect: the
// refined intent, the chosen strategy, the graph summary, the contract
// verdict, the repair changes. Kept flat so the SSE JSON stays operator-
// readable when tailed with curl.
type PipelineEvent struct {
	Phase   PipelineEventKind `json:"phase"`
	Status  PipelineStatus    `json:"status"`
	Message string            `json:"message,omitempty"`
	Payload map[string]any    `json:"payload,omitempty"`
	// Source says WHO produced this step: the deterministic planner or the
	// model. The transcript previously rendered both identically, so a user
	// could not tell which decisions were rule-based and which were a model's.
	Source PipelineSource `json:"source,omitempty"`
	// ElapsedMS is milliseconds since the pipeline started. Without it the UI
	// had no way to show progress or spot a phase that had stalled, which is
	// what made a slow generate indistinguishable from a frozen one.
	ElapsedMS int64 `json:"elapsed_ms"`
}

// PipelineOptions bundles the knobs a caller can pass. Emit is the SSE
// sink — a nil Emit turns this into a synchronous non-streaming pipeline
// (useful for tests).
type PipelineOptions struct {
	Answers    map[string]string
	Light      bool // use LightRefinePrompt for a touched-up re-generate
	In         PreflightInput
	Emit       func(PipelineEvent)
	SkipRepair bool
	AutoRepair bool // when true, apply deterministic repairs; false = report only
	// PreferDeterministic pins graph design to the deterministic planner and
	// keeps the model out of the loop entirely.
	//
	// The default is model-first, because the deterministic planner cannot see
	// most of a workspace's capabilities and so cannot be relied on for accuracy.
	// This flag exists for the case where reproducibility genuinely matters more
	// — a regression suite, an air-gapped build, an audit that must show no model
	// touched the design. It is opt-in rather than the default so that choosing
	// reproducibility over correctness is a deliberate act.
	PreferDeterministic bool
}

// PipelineResult mirrors compile.Result but is enriched with the phase
// outputs so the final `done` event carries everything the operator saw
// streaming plus the final drafted workflow.
type PipelineResult struct {
	Refinement PromptRefinement `json:"refinement"`
	Strategy   string           `json:"strategy"`
	Compile    Result           `json:"compile"`
	Contract   ContractResult   `json:"contract"`
	Preflight  PreflightResult  `json:"preflight"`
	Repaired   bool             `json:"repaired,omitempty"`
	// PhaseLog is the ordered list of events emitted during the run — kept
	// so a caller that used the sync form still gets the transcript.
	PhaseLog []PipelineEvent `json:"phase_log,omitempty"`
}

// RunGeneratePipeline orchestrates the 5 phases and emits one PipelineEvent
// at each start/complete/skip boundary. The LLM is allowed to clarify/refine
// wording, but Soulacy owns strategy selection and graph construction.
func RunGeneratePipeline(ctx context.Context, llm LLM, intent string, catalog Catalog, opts PipelineOptions) (PipelineResult, error) {
	res := PipelineResult{}
	started := time.Now()
	emit := func(ev PipelineEvent) {
		ev.ElapsedMS = time.Since(started).Milliseconds()
		if ev.Source == "" {
			// Everything except prompt refinement is rule-based, so planner is the
			// correct default and an unlabelled event cannot silently imply a model
			// made the decision.
			ev.Source = SourcePlanner
		}
		res.PhaseLog = append(res.PhaseLog, ev)
		if opts.Emit != nil {
			opts.Emit(ev)
		}
	}

	// cancelled reports operator cancellation and records it in the transcript.
	// Checked between phases: the phases themselves are single synchronous calls,
	// so this bounds cancellation latency to one phase rather than pretending to
	// interrupt work mid-flight.
	cancelled := func(phase PipelineEventKind) bool {
		if ctx.Err() == nil {
			return false
		}
		emit(PipelineEvent{
			Phase: phase, Status: StatusCancelled,
			Message: "Cancelled — the partial draft is kept so nothing you had is lost.",
		})
		return true
	}

	// Phase 1 — clarify_intent (RefinePrompt). The ONLY phase a model touches.
	emit(PipelineEvent{Phase: PhaseClarifyIntent, Status: StatusStart, Message: "Clarifying intent", Source: SourceLLM})
	refineFn := RefinePrompt
	if opts.Light {
		refineFn = LightRefinePrompt
	}
	refinement, err := refineFn(ctx, llm, intent, catalog)
	if err != nil {
		emit(PipelineEvent{Phase: PhaseClarifyIntent, Status: StatusError, Message: err.Error(), Source: SourceLLM})
		return res, fmt.Errorf("clarify_intent: %w", err)
	}
	res.Refinement = refinement
	if cancelled(PhaseClarifyIntent) {
		return res, ctx.Err()
	}
	emit(PipelineEvent{
		Phase:   PhaseClarifyIntent,
		Status:  StatusComplete,
		Source:  SourceLLM,
		Message: refinementSummary(refinement),
		Payload: map[string]any{
			"refined_intent": refinement.RefinedIntent,
			"summary":        refinement.Summary,
			"assumptions":    refinement.Assumptions,
			"questions":      refinement.Questions,
		},
	})

	// Phase 2 — choose_strategy (deterministic Strategy Advisor over the refined text).
	emit(PipelineEvent{Phase: PhaseChooseStrategy, Status: StatusStart, Message: "Choosing execution strategy"})
	combined := strings.TrimSpace(refinement.RefinedIntent + " " + intent)
	advice := AdviseStrategy(combined, catalog, refinement.RecommendedMode, false)
	strategy := advice.RuntimeStrategy
	res.Strategy = strategy
	strategyMsg := "workflow (fixed graph)"
	if advice.Mode != "workflow" {
		strategyMsg = advice.Mode + " (reasoning agent)"
	}
	emit(PipelineEvent{
		Phase:   PhaseChooseStrategy,
		Status:  StatusComplete,
		Message: "Strategy: " + strategyMsg,
		Payload: map[string]any{"strategy": strategy, "mode": advice.Mode, "reason": advice.Reason, "pattern": advice.DeterministicPattern},
	})

	// Phase 3 — build_graph.
	//
	// The MODEL designs the graph, grounded in the whole catalogue, and the
	// deterministic planner is the safety net rather than the default.
	//
	// It used to be the other way round: a keyword pattern match won outright and
	// the model never saw the intent. That is reproducible but blind — the
	// workflow skeletons are hardcoded to web_search and never reference MCP, and
	// neither deterministic path selects skills at all. So a prompt naming an
	// installed MCP tool produced a graph that used none of it. Reproducibly
	// wrong is still wrong.
	//
	// Ordering the two this way keeps what the deterministic planner is actually
	// good for: it still catches the case where the model errors, and — because
	// the model's graph is CONTRACT-CHECKED before it is accepted — the case
	// where a weak builder model produces something that does not hold together.
	// Accuracy first, with a floor under it.
	emit(PipelineEvent{Phase: PhaseBuildGraph, Status: StatusStart, Message: "Designing the graph"})
	// Prefer the refined intent for the compile step so the model sees the
	// operator-visible version.
	compileIntent := combined
	if strings.TrimSpace(refinement.RefinedIntent) != "" {
		compileIntent = refinement.RefinedIntent
	}

	// Coverage is judged against the refined intent AND the original.
	//
	// The user names a capability in THEIR words; refinement rewrites those words.
	// When the rewrite drops the server name — which it is free to do, it is
	// prose, not a contract — checking only the refined text means the request
	// that named an MCP server and the graph that ignored it both look consistent,
	// and the whole coverage guard silently disengages. That made the guard work
	// on prompts whose refinement happened to repeat the name and fail on
	// otherwise identical ones, which is indistinguishable from it being tuned to
	// one example.
	//
	// Concatenating is enough: every check over this text asks "does the intent
	// NAME something installed", and a name in either version is a name.
	coverageIntent := compileIntent
	if o := strings.TrimSpace(combined); o != "" && o != compileIntent {
		coverageIntent = compileIntent + "\n" + o
	}

	// Carry the user's ORIGINAL words alongside the refined intent.
	//
	// Capability grounding matches against this: the refined intent is a long
	// expansion that shares ordinary vocabulary with dozens of unrelated skills,
	// so matching on it attaches them all. groundSkills already prefers
	// Draft.RawIntent for exactly this reason, but nothing on the generation path
	// ever set that field — it is populated only when loading a saved agent — so
	// the guard silently fell back to the text it exists to avoid, and a weather
	// agent came out carrying finance and design skills.
	//
	// `intent`, NOT `combined`: combined is the refined text concatenated with the
	// original, so using it here would smuggle the expansion back in and reproduce
	// the exact bloat this is meant to prevent.
	catalog.RawIntent = strings.TrimSpace(intent)

	deterministic := func() (Result, bool) {
		if advice.Mode == "workflow" {
			return CompileDeterministicWorkflow(compileIntent, catalog, opts.Answers)
		}
		return CompileDeterministicAgent(compileIntent, catalog, strategy, opts.Answers)
	}

	var compileRes Result
	var ok bool
	designedBy := SourceLLM

	// Curated graph up front: free (no model call), used both as the fallback and
	// — when it encodes a real procedure — as a worked example for the model.
	detRes, detOK := deterministic()

	if llm != nil && !opts.PreferDeterministic {
		designCat := catalog
		if detOK && EncodesProcedure(detRes) {
			if ref, mErr := json.MarshalIndent(detRes.Workflow, "", "  "); mErr == nil {
				designCat.ReferenceGraph = string(ref)
			}
		}
		msg := "The builder model is choosing tools, skills and MCP servers from what is installed."
		if designCat.ReferenceGraph != "" {
			msg += " It is starting from Soulacy's curated graph for this kind of job."
		}
		emit(PipelineEvent{
			Phase: PhaseBuildGraph, Status: StatusStart, Source: SourceLLM, Message: msg,
		})
		var lerr error
		if advice.Mode == "workflow" {
			compileRes, lerr = Compile(ctx, llm, compileIntent, designCat, opts.Answers)
		} else {
			compileRes, lerr = CompileAgent(ctx, llm, compileIntent, designCat, strategy, opts.Answers)
		}
		ok = lerr == nil

		// Coverage retry: the model built something valid but skipped a capability
		// the user NAMED.
		//
		// This is the "it used web_search instead of the travel MCP server I asked
		// for" failure. Structurally the graph is fine, so no contract blocker
		// fires and nothing below would notice; the only previous response was a
		// note advising the user to re-read the steps themselves.
		//
		// Retrying — rather than substituting the tool name deterministically — is
		// what keeps the ARGUMENTS right. web_search takes {"query": ...} and an
		// MCP tool takes whatever its schema says, so swapping the name in place
		// would trade a graph that runs and does the wrong thing for one that does
		// not run at all. The model has the tool's real parameters in its
		// catalogue, so asking again is the repair that can fill them in.
		//
		// Once, and only when something concrete was missed: a named capability
		// that this workspace actually has. Costs one extra call on a request that
		// has already demonstrably gone wrong.
		if ok {
			if short := CoverageShortfall(coverageIntent, catalog, compileRes); short != "" {
				emit(PipelineEvent{
					Phase: PhaseBuildGraph, Status: StatusStart, Source: SourceLLM,
					Message: "Retrying: the first graph " + short + ".",
				})
				retryCat := designCat
				retryCat.MustUseTools = namedMCPTools(coverageIntent, catalog)
				retryCat.MustUseSkills = namedSkills(coverageIntent, catalog)
				var retryRes Result
				var rerr error
				if advice.Mode == "workflow" {
					retryRes, rerr = Compile(ctx, llm, compileIntent, retryCat, opts.Answers)
				} else {
					retryRes, rerr = CompileAgent(ctx, llm, compileIntent, retryCat, strategy, opts.Answers)
				}
				// Keep the retry only if it actually closed the gap. A retry that
				// misses too is not evidence of anything, and swapping in a second
				// unrelated graph would just churn the canvas.
				if rerr == nil && CoverageShortfall(coverageIntent, catalog, retryRes) == "" {
					compileRes = retryRes
					compileRes.Notes = append(compileRes.Notes,
						"The first graph "+short+", so Studio rebuilt it using the capabilities you named.")
					emit(PipelineEvent{
						Phase: PhaseBuildGraph, Status: StatusComplete, Source: SourceLLM,
						Message: "Retry wired in the capability you asked for.",
					})
				} else {
					emit(PipelineEvent{
						Phase: PhaseBuildGraph, Status: StatusSkip, Source: SourceLLM,
						Message: "Retry did not close the gap; keeping the first graph.",
					})
				}
			}
		}

		if ok {
			if c := AssessContract(compileRes.Workflow, catalog, opts.In); c.Blockers > 0 {
				// Repair BEFORE giving up on it. These are the same deterministic
				// wiring/contract fixes phase 5 runs, and most of what a weak builder
				// model gets wrong is structural — a dangling reference, an unwired
				// port — not a bad choice of tools. Throwing the graph away for that
				// discards a correct plan over a fixable defect.
				emit(PipelineEvent{
					Phase: PhaseBuildGraph, Status: StatusStart,
					Message: fmt.Sprintf("Repairing %d structural issue(s) in the model's graph.", c.Blockers),
				})
				RepairContractStructure(&compileRes.Workflow, compileIntent, catalog, opts.Answers, c)
				RepairWiring(&compileRes.Workflow, catalog)
				if c2 := AssessContract(compileRes.Workflow, catalog, opts.In); c2.Blockers > 0 {
					ok = false
					lerr = fmt.Errorf("the model's graph did not pass its contract (%d blocker(s)) and could not be repaired", c2.Blockers)
				}
			}
		}

		if !ok && lerr != nil {
			// Before falling back, check what the fallback would COST.
			//
			// The deterministic skeletons are hardcoded to web_search and reference
			// no MCP at all, so falling back can silently swap a graph that used the
			// right capability for one that does not — and the replacement looks
			// cleaner, because its only virtue is passing a structural check.
			//
			// A graph with visible blockers beats a graph that is quietly wrong: the
			// blockers are on screen with a Fix button, whereas "it used web_search
			// instead of your travel MCP" is invisible until someone reads the nodes.
			// So when the model captured a named capability and the fallback would
			// drop it, keep the model's graph and report the blockers.
			modelShort := CoverageShortfall(coverageIntent, catalog, compileRes)
			if detRes, detOK := deterministic(); detOK {
				detShort := CoverageShortfall(coverageIntent, catalog, detRes)

				// Does the fallback actually make this MORE ready to save?
				//
				// Coverage was the only question asked here, and it is not the only
				// way the swap can be a downgrade. Observed live: a prompt naming
				// three parallel analysts and an editor produced a model graph with
				// one blocker, which was discarded for a deterministic skeleton
				// carrying TWO blockers and two warnings of its own — a two-node
				// "search then summarize" that had also dropped every agent the user
				// asked for. We traded a rich graph with one fixable defect for a
				// bare one with more, and told the user it was a fallback.
				//
				// The whole justification for falling back is that the replacement
				// is sounder. When it is not, there is nothing to trade for: keep
				// the graph that at least matches what was asked.
				modelC := AssessContract(compileRes.Workflow, catalog, opts.In)
				detC := AssessContract(detRes.Workflow, catalog, opts.In)

				keepReason, noteReason := keepModelGraph(modelShort, detShort, modelC.Blockers, detC.Blockers)

				if keepReason != "" {
					emit(PipelineEvent{
						Phase: PhaseBuildGraph, Status: StatusSkip, Source: SourceLLM,
						Message: "Keeping the model's graph despite its blockers: " + keepReason + ".",
					})
					compileRes.Notes = append(compileRes.Notes,
						"This graph still has unresolved blockers, kept because "+
							noteReason+". Fix the blockers rather than regenerating.")
					ok = true
				} else {
					emit(PipelineEvent{
						Phase: PhaseBuildGraph, Status: StatusSkip, Source: SourceLLM,
						Message: "Falling back to the deterministic planner: " + lerr.Error(),
					})
					compileRes, ok = detRes, true
					designedBy = SourcePlanner
				}
			} else {
				// No deterministic alternative exists, so the model's graph — blockers
				// and all — is the only thing there is. Better than nothing, and the
				// blockers are reported.
				emit(PipelineEvent{
					Phase: PhaseBuildGraph, Status: StatusSkip, Source: SourceLLM,
					Message: "Keeping the model's graph: no deterministic pattern fits this intent.",
				})
				ok = compileRes.Workflow.Name != "" || len(compileRes.Workflow.Flow.Nodes) > 0
			}
		}
	}

	if !ok {
		designedBy = SourcePlanner
		compileRes, ok = deterministic()
	}

	if !ok {
		err := fmt.Errorf("neither the builder model nor the deterministic planner could build a %s draft for this intent", advice.Mode)
		emit(PipelineEvent{Phase: PhaseBuildGraph, Status: StatusError, Message: err.Error()})
		return res, fmt.Errorf("build_graph: %w", err)
	}

	// Say WHO designed the graph. The deterministic path advertises "no LLM
	// designed the graph"; when that stops being true the user has to be told, or
	// the guarantee silently becomes a lie.
	if designedBy == SourceLLM {
		compileRes.Notes = append(compileRes.Notes,
			"The builder model designed this graph from the installed tools, skills and MCP servers.")
	}

	// Coverage is checked on EVERY path, not just the deterministic one. A
	// model-designed graph can miss a named capability too, and the symptom is
	// identical from the outside: a graph that runs and quietly does the wrong
	// thing. Checking only the planner would have left the more common case
	// unguarded now that the model designs by default.
	if short := CoverageShortfall(coverageIntent, catalog, compileRes); short != "" {
		who := "the builder model"
		if designedBy == SourcePlanner {
			who = "the deterministic planner"
		}
		compileRes.Notes = append(compileRes.Notes,
			"Heads up: "+who+" built this graph and "+short+
				". It will run, but it is not using a capability you asked for — review the steps before saving.")
		emit(PipelineEvent{
			Phase: PhaseBuildGraph, Status: StatusComplete, Source: designedBy,
			Message: "Coverage gap: " + short,
			Payload: map[string]any{"coverage_shortfall": short},
		})
	}
	if compileRes.Generation != nil {
		compileRes.Generation.PlanMatched = len(compileRes.Plan) > 0
		compileRes.Generation.PatternMatched = advice.DeterministicPattern != "" || len(MatchPatterns(compileIntent, catalog, 1)) > 0
	}
	res.Compile = compileRes

	// Announce each constructed node so the canvas fills in progressively rather
	// than appearing whole at the end.
	//
	// Honest about the mechanism: the deterministic planner is ONE synchronous
	// pass, so these are emitted after it returns, not from inside it. That is
	// still strictly more information than the old aggregate count — the user
	// sees which steps exist, in order, and what each one is — and it is emitted
	// at full speed with no artificial pacing. Faking a delay to look like
	// incremental construction would be theatre, and would make a fast generate
	// slower for no reason. Emitting from inside the planner needs a callback
	// threaded through CompileDeterministicWorkflow/Agent; that is the real fix
	// and is noted in the handoff.
	emitNodes(emit, compileRes.Workflow)

	if cancelled(PhaseBuildGraph) {
		// The draft is already on res.Compile, so a cancel here keeps the graph
		// that was built instead of discarding the work.
		return res, ctx.Err()
	}
	emit(PipelineEvent{
		Phase:   PhaseBuildGraph,
		Status:  StatusComplete,
		Message: buildGraphSummary(compileRes.Workflow),
		Payload: map[string]any{
			"nodes":              len(compileRes.Workflow.Flow.Nodes),
			"edges":              len(compileRes.Workflow.Flow.Edges),
			"tools":              len(compileRes.Workflow.Tools),
			"questions":          compileRes.Questions,
			"deterministic_plan": len(compileRes.Notes) > 0 && strings.Contains(strings.Join(compileRes.Notes, "\n"), "deterministic planner"),
		},
	})

	// Phase 4 — validate (Preflight + AssessContract).
	emit(PipelineEvent{Phase: PhaseValidate, Status: StatusStart, Message: "Validating the draft"})
	pf := Preflight(compileRes.Workflow, opts.In)
	contract := AssessContract(compileRes.Workflow, catalog, opts.In)
	res.Preflight = pf
	res.Contract = contract
	emit(PipelineEvent{
		Phase:   PhaseValidate,
		Status:  StatusComplete,
		Message: contract.Summary,
		Payload: map[string]any{
			"score":     contract.Score,
			"blockers":  contract.Blockers,
			"warnings":  contract.Warnings,
			"preflight": pf.OK,
		},
	})

	// Phase 5 — repair. Deterministic-only in this pipeline (LLM repair
	// stays in BuildUntilWorks, which the "Build until it works" button
	// still owns). This phase either applies a wiring pass or reports that
	// nothing repairable was found.
	if opts.SkipRepair || contract.OK {
		emit(PipelineEvent{Phase: PhaseRepair, Status: StatusSkip, Message: "No repair needed"})
	} else if opts.AutoRepair {
		before := countIssues(pf, contract)
		RepairContractStructure(&compileRes.Workflow, compileIntent, catalog, opts.Answers, contract)
		RepairWiring(&compileRes.Workflow, catalog)
		pf2 := Preflight(compileRes.Workflow, opts.In)
		contract2 := AssessContract(compileRes.Workflow, catalog, opts.In)
		after := countIssues(pf2, contract2)
		res.Compile = compileRes
		res.Preflight = pf2
		res.Contract = contract2
		res.Repaired = after < before
		msg := "Applied deterministic wiring repair"
		if res.Repaired {
			msg = fmt.Sprintf("Applied wiring repair — %d issue(s) fixed", before-after)
		} else {
			msg = "Wiring repair pass found nothing to change"
		}
		emit(PipelineEvent{
			Phase:   PhaseRepair,
			Status:  StatusComplete,
			Message: msg,
			Payload: map[string]any{"issues_before": before, "issues_after": after},
		})
	} else {
		emit(PipelineEvent{
			Phase:   PhaseRepair,
			Status:  StatusSkip,
			Message: "Repair available but auto-repair is off — use Build until it works",
			Payload: map[string]any{"blockers": contract.Blockers, "warnings": contract.Warnings},
		})
	}
	return res, nil
}

func refinementSummary(r PromptRefinement) string {
	sum := strings.TrimSpace(r.Summary)
	if sum != "" {
		return sum
	}
	if q := len(r.Questions); q > 0 {
		return fmt.Sprintf("Clarified with %d question(s)", q)
	}
	return "Intent refined"
}

func buildGraphSummary(d Draft) string {
	if d.IsAgent() {
		return fmt.Sprintf("%s agent draft (%d tools, %d peers)", strings.TrimSpace(d.Strategy), len(d.Tools), len(d.NewAgents))
	}
	return fmt.Sprintf("Workflow draft (%d nodes, %d edges)", len(d.Flow.Nodes), len(d.Flow.Edges))
}

func countIssues(pf PreflightResult, c ContractResult) int {
	n := c.Blockers + c.Warnings
	if !pf.OK {
		n += len(pf.Blockers)
	}
	return n
}

// keepModelGraph decides whether to keep the builder model's graph instead of
// swapping in the deterministic skeleton, and says why.
//
// Returns ("", "") to fall back. The two strings are the progress-event reason
// and the shorter note pinned to the draft.
//
// Two ways the swap can be a downgrade:
//
//  1. Coverage. The skeletons are hardcoded to web_search and name no MCP, so
//     falling back can quietly replace a graph that used the capability the user
//     asked for with one that does not. Blockers are on screen with a Fix
//     button; "it used web_search instead of your travel MCP" is invisible until
//     someone reads the nodes.
//
//  2. Contract health. This one was not checked at all, and it is the one that
//     bit. Observed live: a prompt naming three parallel analysts and an editor
//     produced a model graph carrying ONE blocker. It was discarded for a
//     deterministic skeleton carrying TWO blockers and two warnings — a two-node
//     "search then summarize" that had also dropped every agent in the request.
//     The entire justification for falling back is that the replacement is
//     sounder; when it is not, there is nothing being traded for, and the graph
//     that at least matches the request should survive.
//
// Ties keep the model's graph deliberately. Equal blockers means the fallback
// buys nothing, and the model's graph is the one shaped like what was asked.
func keepModelGraph(modelShortfall, detShortfall string, modelBlockers, detBlockers int) (reason, note string) {
	if detShortfall != "" && modelShortfall == "" {
		return detShortfall + ", so falling back would lose a capability you asked for",
			"the deterministic alternative " + detShortfall
	}
	if detBlockers >= modelBlockers {
		return fmt.Sprintf(
				"the deterministic alternative has %d blocker(s) of its own against this graph's %d, so falling back would not make it any readier to save",
				detBlockers, modelBlockers),
			fmt.Sprintf("the deterministic alternative carries %d blocker(s) of its own", detBlockers)
	}
	return "", ""
}
