package studio

// agentpolicy.go — the editable contract behind the Studio Build step (ST-02).
//
// Before this, choosing "Auto" or "ReAct" or "Plan-Execute" set a single string
// and everything that actually governed the run — when to stop, what counts as
// done, how many bad steps to tolerate, whether a side effect needs approval —
// was either a hard-coded default or buried in prose inside SystemPrompt. That
// made the mode picker a label rather than a contract: two agents on the same
// strategy could behave completely differently for reasons nothing displayed.
//
// AgentPolicy makes those knobs explicit, per strategy, so the Build screen can
// show them, the user can edit them, and validation can check them.
//
// Every field is optional and zero-value compatible. A nil *AgentPolicy means
// "use the defaults for this strategy", so existing drafts keep working and a
// round-trip through Studio never invents configuration the user did not set.

import (
	"strings"

	"github.com/soulacy/soulacy/pkg/agent"
)

// Strategy tokens. Centralised so the panel, the advisor and the compiler agree.
const (
	StrategyWorkflow    = "workflow"
	StrategyAuto        = "auto"
	StrategyReAct       = "react"
	StrategyPlanExecute = "plan_execute"
)

// AgentPolicy is the whole per-strategy contract. The three sub-blocks are
// deliberately separate rather than one flat bag: a ReAct budget means nothing
// under Plan-Execute, and flattening them made it impossible to tell which
// fields were actually in force for the selected mode.
type AgentPolicy struct {
	// Contract applies to every reasoning strategy (Auto, ReAct, Plan-Execute).
	Contract *AgentContract `json:"contract,omitempty"`
	// ReAct applies only when Strategy == "react".
	ReAct *ReActPolicy `json:"react,omitempty"`
	// Plan applies only when Strategy == "plan_execute".
	Plan *PlanExecutePolicy `json:"plan,omitempty"`
}

// AgentContract is what the agent is FOR — the part a non-engineer reads.
type AgentContract struct {
	// Goal is the single sentence describing what a successful run achieves.
	Goal string `json:"goal,omitempty"`
	// Instructions is the operating guidance handed to the model. Distinct from
	// Goal because "what done looks like" and "how to behave" are edited by
	// different people at different times.
	Instructions string `json:"instructions,omitempty"`
	// CompletionCriteria is the explicit, checkable statement of done. Without
	// it a reasoning loop's only stop condition is its step budget, which is a
	// timeout dressed up as a decision.
	CompletionCriteria string `json:"completion_criteria,omitempty"`
	// ToolChoice is "auto" | "required" | "none" — whether the model may decide
	// to call no tool at all. Empty = auto.
	ToolChoice string `json:"tool_choice,omitempty"`
	// RecoveryRetries bounds how many times a failed step is retried before the
	// run gives up. Zero = Studio default.
	RecoveryRetries int `json:"recovery_retries,omitempty"`
}

// NOTE ON THE BOOLEAN TAGS IN THE TWO STRUCTS BELOW: none carry `omitempty`,
// and that is load-bearing. Every one of these flags defaults to TRUE, and the
// Studio panel renders a missing field as `?? true`. With `omitempty` a flag the
// operator turned OFF was dropped from the JSON entirely and came back ON — the
// panel showed the opposite of what was saved, and the next edit pushed that
// reverted value back down. False has to travel.
// ReActPolicy bounds the observe→act loop. Every field here exists because an
// unbounded ReAct loop fails by burning budget rather than by stopping.
type ReActPolicy struct {
	// Objective is the loop's stated goal, shown at the top of the reasoning
	// policy panel.
	Objective string `json:"objective,omitempty"`
	// ObserveActContract describes the one-action-at-a-time discipline the loop
	// must follow.
	ObserveActContract string `json:"observe_act_contract,omitempty"`
	// StopConditions is the plain-language statement of when to stop.
	StopConditions string `json:"stop_conditions,omitempty"`
	// RecoveryBehavior says what happens on an invalid or repeated action.
	RecoveryBehavior string `json:"recovery_behavior,omitempty"`
	// InvalidStepBudget caps malformed tool calls before the loop gives up.
	// This is the budget that actually protects against a weak JSON model
	// looping forever on unparseable output.
	InvalidStepBudget int `json:"invalid_step_budget,omitempty"`
	// RepeatedToolLimit caps calling the same tool with the same args, which is
	// the single most common ReAct failure: the model re-reads the same page
	// waiting for a different answer.
	RepeatedToolLimit int `json:"repeated_tool_limit,omitempty"`
	// ConfidenceThreshold is the score at or above which the loop may stop early.
	ConfidenceThreshold float64 `json:"confidence_threshold,omitempty"`
	// PreserveBestResult keeps the best intermediate answer, so a loop that
	// exhausts its budget still returns its best work instead of nothing.
	PreserveBestResult bool `json:"preserve_best_result"`
	// FallbackToAuto downgrades to Auto after repeated invalid steps rather than
	// failing the run. This is what makes ReAct safe to offer at all on models
	// whose tool-calling is unreliable.
	FallbackToAuto bool `json:"fallback_to_auto"`
}

// PlanExecutePolicy governs the plan→execute split.
type PlanExecutePolicy struct {
	// Steps is the planner's declared plan. Each entry is editable before the
	// run so the plan is reviewable rather than discovered at execution time.
	Steps []PlanExecuteStep `json:"steps,omitempty"`
	// ReplanAfterFailure lets the planner revise the plan when a step fails,
	// instead of aborting a multi-phase run because phase two moved.
	ReplanAfterFailure bool `json:"replan_after_failure"`
	// ParallelIndependentSteps allows independent steps to run concurrently.
	ParallelIndependentSteps bool `json:"parallel_independent_steps"`
	// ApprovalBeforeSideEffects pauses for a human before any step that would
	// touch the outside world. Defaults off only because it is meaningless
	// without an interactive session; the save path still gates side effects.
	ApprovalBeforeSideEffects bool `json:"approval_before_side_effects"`
	// PlanTimeout caps the PLANNING phase specifically, separate from the step
	// budget — a planner that cannot converge should not consume the whole run.
	PlanTimeout string `json:"plan_timeout,omitempty"`
}

// PlanExecuteStep is one declared phase of a Plan-Execute run.
//
// Named to distinguish it from patterns.go's PlanStep, which is a different
// thing entirely: that one is a node in a deterministic pre-plan SKELETON used
// to prompt the generator, whereas this is a phase of a reasoning agent's
// runtime plan. Sharing a name would have made two unrelated concepts look
// interchangeable.
type PlanExecuteStep struct {
	Title string `json:"title"`
	// Status is "pending" | "running" | "done" | "failed" — the live view uses it.
	Status string `json:"status,omitempty"`
	// AllowedTools narrows what this step may call. An empty list means the
	// agent's full allowlist, so narrowing is opt-in rather than a trap.
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// ExpectedOutput is what this step must produce for the plan to continue.
	ExpectedOutput string `json:"expected_output,omitempty"`
	// Verification is how the step's output is checked.
	Verification string `json:"verification,omitempty"`
	// DependsOn names steps that must finish first. Steps with no dependency on
	// each other are what ParallelIndependentSteps is allowed to overlap.
	DependsOn []string `json:"depends_on,omitempty"`
}

// DefaultAgentPolicy returns the policy Studio applies when a draft declares
// none. Exposed (rather than applied silently at run time) so the Build screen
// can SHOW the effective values — a default the user cannot see is
// indistinguishable from behaviour they cannot control.
func DefaultAgentPolicy(strategy string) AgentPolicy {
	p := AgentPolicy{
		Contract: &AgentContract{ToolChoice: "auto", RecoveryRetries: 2},
	}
	switch normalizeStrategy(strategy) {
	case StrategyReAct:
		p.ReAct = &ReActPolicy{
			InvalidStepBudget:   2,
			RepeatedToolLimit:   2,
			ConfidenceThreshold: 0.72,
			PreserveBestResult:  true,
			// On by default: ReAct is offered as "advanced", and the honest
			// default for an advanced-but-brittle mode is to degrade rather
			// than to fail the user's run.
			FallbackToAuto: true,
		}
	case StrategyPlanExecute:
		p.Plan = &PlanExecutePolicy{
			ReplanAfterFailure:        true,
			ParallelIndependentSteps:  true,
			ApprovalBeforeSideEffects: true,
			PlanTimeout:               "2m",
		}
	}
	return p
}

// EffectivePolicy merges a draft's policy over the defaults for its strategy,
// so callers always get a complete picture without mutating the draft.
func EffectivePolicy(d Draft) AgentPolicy {
	out := DefaultAgentPolicy(d.Strategy)
	p := d.Policy
	if p == nil {
		return out
	}
	if p.Contract != nil {
		c := *out.Contract
		if p.Contract.Goal != "" {
			c.Goal = p.Contract.Goal
		}
		if p.Contract.Instructions != "" {
			c.Instructions = p.Contract.Instructions
		}
		if p.Contract.CompletionCriteria != "" {
			c.CompletionCriteria = p.Contract.CompletionCriteria
		}
		if p.Contract.ToolChoice != "" {
			c.ToolChoice = p.Contract.ToolChoice
		}
		if p.Contract.RecoveryRetries > 0 {
			c.RecoveryRetries = p.Contract.RecoveryRetries
		}
		out.Contract = &c
	}
	// The two loop policies are replaced wholesale rather than field-merged:
	// a user who edited the ReAct block has stated the whole budget, and
	// half-merging it would produce a combination nobody chose.
	if p.ReAct != nil && out.ReAct != nil {
		out.ReAct = p.ReAct
	}
	if p.Plan != nil && out.Plan != nil {
		out.Plan = p.Plan
	}
	return out
}

// normalizeStrategy folds the accepted spellings to a canonical token.
func normalizeStrategy(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case StrategyAuto:
		return StrategyAuto
	case StrategyReAct:
		return StrategyReAct
	case StrategyPlanExecute, "plan-execute", "planexecute":
		return StrategyPlanExecute
	default:
		return StrategyWorkflow
	}
}

// ValidatePolicy reports human-readable problems with a policy. Returned as
// warnings rather than errors: an aggressive budget is a judgement call, and
// refusing to save it would be Studio overriding the operator on their own
// tradeoff. The exception is a contradiction that cannot mean anything.
func ValidatePolicy(d Draft) []string {
	if normalizeStrategy(d.Strategy) == StrategyWorkflow {
		return nil
	}
	p := EffectivePolicy(d)
	var out []string

	if p.Contract != nil && strings.TrimSpace(p.Contract.CompletionCriteria) == "" {
		out = append(out, "No completion criteria: the only thing that will stop this agent is its step budget, which is a timeout rather than a decision.")
	}
	if r := p.ReAct; r != nil {
		if r.ConfidenceThreshold < 0 || r.ConfidenceThreshold > 1 {
			out = append(out, "Confidence threshold must be between 0 and 1.")
		}
		if r.InvalidStepBudget <= 0 {
			out = append(out, "Invalid-step budget of 0 lets a malformed tool call retry until the run's total budget is gone.")
		}
		if !r.FallbackToAuto && !r.PreserveBestResult {
			out = append(out, "With neither fallback-to-Auto nor preserve-best-result, a loop that exhausts its budget returns nothing at all.")
		}
	}
	if pl := p.Plan; pl != nil {
		if pl.ParallelIndependentSteps && pl.ApprovalBeforeSideEffects {
			// Not a contradiction, but worth stating: approvals serialise the
			// very steps parallelism was enabled to overlap.
			out = append(out, "Parallel steps with per-step approval will still pause one at a time for anything with a side effect.")
		}
		for i, s := range pl.Steps {
			if strings.TrimSpace(s.Title) == "" {
				out = append(out, "Plan step "+itoa(i+1)+" has no title.")
			}
		}
	}
	return out
}

// ── Persistence ────────────────────────────────────────────────────────────
//
// AgentPolicy used to be write-only: the Build step edited it, the API accepted
// it, and nothing ever wrote it to the agent definition or read it back. Every
// goal, instruction and completion criterion an operator typed was discarded on
// save. These two functions are the bridge, and they are deliberately EXACT —
// only what the draft actually states is written, so a round-trip cannot invent
// a policy the operator never chose.

// ToReasoningContract projects a draft's policy onto the agent definition.
// Returns nil when the draft states nothing, so a plain agent's SOUL.yaml stays
// free of an empty contract block.
func (p *AgentPolicy) ToReasoningContract() *agent.ReasoningContract {
	if p == nil {
		return nil
	}
	out := &agent.ReasoningContract{}
	any := false
	if c := p.Contract; c != nil {
		out.Goal = c.Goal
		out.Instructions = c.Instructions
		out.CompletionCriteria = c.CompletionCriteria
		out.ToolChoice = c.ToolChoice
		out.RecoveryRetries = c.RecoveryRetries
		any = any || c.Goal != "" || c.Instructions != "" || c.CompletionCriteria != "" ||
			c.ToolChoice != "" || c.RecoveryRetries > 0
	}
	if r := p.ReAct; r != nil {
		out.ReAct = &agent.ReasoningReActPolicy{
			Objective:           r.Objective,
			ObserveActContract:  r.ObserveActContract,
			StopConditions:      r.StopConditions,
			RecoveryBehavior:    r.RecoveryBehavior,
			InvalidStepBudget:   r.InvalidStepBudget,
			RepeatedToolLimit:   r.RepeatedToolLimit,
			ConfidenceThreshold: r.ConfidenceThreshold,
			PreserveBestResult:  boolPtr(r.PreserveBestResult),
			FallbackToAuto:      boolPtr(r.FallbackToAuto),
		}
		any = true
	}
	if pl := p.Plan; pl != nil {
		steps := make([]agent.ReasoningPlanStep, 0, len(pl.Steps))
		for _, s := range pl.Steps {
			steps = append(steps, agent.ReasoningPlanStep{
				Title:          s.Title,
				Status:         s.Status,
				AllowedTools:   append([]string(nil), s.AllowedTools...),
				ExpectedOutput: s.ExpectedOutput,
				Verification:   s.Verification,
				DependsOn:      append([]string(nil), s.DependsOn...),
			})
		}
		out.Plan = &agent.ReasoningPlanPolicy{
			Steps:                     steps,
			ReplanAfterFailure:        boolPtr(pl.ReplanAfterFailure),
			ParallelIndependentSteps:  boolPtr(pl.ParallelIndependentSteps),
			ApprovalBeforeSideEffects: boolPtr(pl.ApprovalBeforeSideEffects),
			PlanTimeout:               pl.PlanTimeout,
		}
		any = true
	}
	if !any {
		return nil
	}
	return out
}

// AgentPolicyFromReasoning reads a stored contract back into a draft policy, so
// re-opening a saved agent shows the operator what they wrote rather than an
// empty panel.
func AgentPolicyFromReasoning(c *agent.ReasoningContract) *AgentPolicy {
	if c == nil {
		return nil
	}
	out := &AgentPolicy{}
	if c.Goal != "" || c.Instructions != "" || c.CompletionCriteria != "" ||
		c.ToolChoice != "" || c.RecoveryRetries > 0 {
		out.Contract = &AgentContract{
			Goal:               c.Goal,
			Instructions:       c.Instructions,
			CompletionCriteria: c.CompletionCriteria,
			ToolChoice:         c.ToolChoice,
			RecoveryRetries:    c.RecoveryRetries,
		}
	}
	if r := c.ReAct; r != nil {
		out.ReAct = &ReActPolicy{
			Objective:           r.Objective,
			ObserveActContract:  r.ObserveActContract,
			StopConditions:      r.StopConditions,
			RecoveryBehavior:    r.RecoveryBehavior,
			InvalidStepBudget:   r.InvalidStepBudget,
			RepeatedToolLimit:   r.RepeatedToolLimit,
			ConfidenceThreshold: r.ConfidenceThreshold,
			PreserveBestResult:  boolOr(r.PreserveBestResult, true),
			FallbackToAuto:      boolOr(r.FallbackToAuto, true),
		}
	}
	if pl := c.Plan; pl != nil {
		steps := make([]PlanExecuteStep, 0, len(pl.Steps))
		for _, s := range pl.Steps {
			steps = append(steps, PlanExecuteStep{
				Title:          s.Title,
				Status:         s.Status,
				AllowedTools:   append([]string(nil), s.AllowedTools...),
				ExpectedOutput: s.ExpectedOutput,
				Verification:   s.Verification,
				DependsOn:      append([]string(nil), s.DependsOn...),
			})
		}
		out.Plan = &PlanExecutePolicy{
			Steps:                     steps,
			ReplanAfterFailure:        boolOr(pl.ReplanAfterFailure, true),
			ParallelIndependentSteps:  boolOr(pl.ParallelIndependentSteps, true),
			ApprovalBeforeSideEffects: boolOr(pl.ApprovalBeforeSideEffects, true),
			PlanTimeout:               pl.PlanTimeout,
		}
	}
	if out.Contract == nil && out.ReAct == nil && out.Plan == nil {
		return nil
	}
	return out
}

func boolPtr(b bool) *bool { return &b }

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// ContractPrompt renders the operator's contract as the paragraph handed to the
// model. This is what makes the panel more than record-keeping: the reasoning
// loop is driven by the system prompt, so a goal or a completion criterion that
// never reaches it changes nothing about how the agent behaves.
func ContractPrompt(p *AgentPolicy) string {
	if p == nil || p.Contract == nil {
		return ""
	}
	c := p.Contract
	var b strings.Builder
	if g := strings.TrimSpace(c.Goal); g != "" {
		b.WriteString("Goal of a successful run: ")
		b.WriteString(g)
	}
	if i := strings.TrimSpace(c.Instructions); i != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("How to behave: ")
		b.WriteString(i)
	}
	if d := strings.TrimSpace(c.CompletionCriteria); d != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("You are done only when: ")
		b.WriteString(d)
	}
	if b.Len() == 0 {
		return ""
	}
	return operatorContractHeading + "\n" + b.String()
}

// operatorContractHeading marks the block so a re-save can detect it is already
// present instead of stacking another copy on every round-trip.
const operatorContractHeading = "### Operator contract"

// GetPlan returns the draft's Plan-Execute policy, or nil. Nil-safe so callers
// do not each repeat the two-level check.
func (p *AgentPolicy) GetPlan() *PlanExecutePolicy {
	if p == nil {
		return nil
	}
	return p.Plan
}
