package studio

import "strings"

// StrategyAdvice is Studio's server-side execution decision. The LLM may
// refine the user's prompt, but Soulacy owns this routing decision so graph
// architecture stays predictable.
type StrategyAdvice struct {
	Mode                 string `json:"mode"` // workflow | auto | plan_execute | react
	RuntimeStrategy      string `json:"runtime_strategy,omitempty"`
	Reason               string `json:"reason"`
	Confidence           string `json:"confidence,omitempty"`
	Provider             string `json:"provider,omitempty"`
	Model                string `json:"model,omitempty"`
	Local                bool   `json:"local,omitempty"`
	Strong               bool   `json:"strong,omitempty"`
	Compact              bool   `json:"compact,omitempty"`
	DeterministicPattern string `json:"deterministic_pattern,omitempty"`
	// CapabilityWarning is set when the chosen mode exceeds what the selected
	// model can actually do (P0-5). Advisory, never blocking: the operator may
	// know something the registry doesn't, but they should not find out from a
	// 3am failure that their model can't emit well-formed tool arguments.
	CapabilityWarning string `json:"capability_warning,omitempty"`
	// Capabilities is the resolved profile the advice was based on, so the UI
	// can show WHY a mode was recommended instead of asserting it.
	Capabilities *Capabilities `json:"capabilities,omitempty"`
}

// AdviseStrategy is the single rule-based Strategy Advisor for Studio. It is
// deliberately deterministic: model capability can bias Auto vs Plan-Execute,
// but it never lets an LLM choose the architecture or implicitly choose ReAct.
func AdviseStrategy(intent string, cat Catalog, requested string, forceWorkflow bool) StrategyAdvice {
	intent = strings.TrimSpace(intent)
	req := normalizeMode(requested)
	advice := StrategyAdvice{
		Mode:       "auto",
		Confidence: "medium",
		Reason:     "Defaulting to Auto for an interactive tool-capable agent.",
	}
	if cat.Generation != nil {
		advice.Provider = cat.Generation.Provider
		advice.Model = cat.Generation.Model
		advice.Local = cat.Generation.Local
		advice.Strong = cat.Generation.Strong
		advice.Compact = cat.Generation.Compact
	}

	// Resolve the model's measured capabilities once; every branch below may
	// consult them (P0-5). Replaces the name-substring heuristics that scored an
	// unrecognised model "assumed fine".
	caps := LookupCapabilities(advice.Provider, advice.Model)
	advice.Capabilities = &caps
	// Only gate on capabilities when a model has actually been CHOSEN. "No
	// model selected yet" (a bare intent classification) is a different state
	// from "a named model we don't recognise", and conflating them would let
	// the gate override intent-based advice before there is anything to judge.
	modelChosen := strings.TrimSpace(advice.Model) != ""

	// Generated workflows are an explicit experimental opt-in. The refiner and
	// deterministic pattern matcher may still RECOGNISE a fixed procedure, but
	// neither is allowed to select Workflow for the operator. Only the dedicated
	// UI/API opt-in sets forceWorkflow.
	if forceWorkflow {
		pattern := deterministicWorkflowPattern(intent)
		advice.Mode = "workflow"
		advice.RuntimeStrategy = ""
		advice.Confidence = "low"
		advice.DeterministicPattern = pattern
		advice.Reason = "You explicitly selected Experimental Workflow, so Studio will generate a fixed graph for review and testing."
		advice.CapabilityWarning = "Experimental workflow generation can choose the wrong tools, lose inputs between steps, or require manual wiring. Review and test every step before deployment."
		if ConversationalIntent(intent) {
			advice.CapabilityWarning += " This request also reads as conversational: a fixed graph cannot ask a clarifying question and wait for the reply, and it keeps no context between messages."
		}
		return advice
	}

	if explicitReActRequested(intent) {
		advice.Mode = "react"
		advice.RuntimeStrategy = "react"
		advice.Confidence = "medium"
		advice.Reason = "The user explicitly requested a ReAct-style loop; Studio will not choose ReAct implicitly."
		// Honour the explicit request, but say plainly when the model cannot
		// sustain it — this is the case where silence costs the most.
		if w := StrategyWarning(advice.Provider, advice.Model, "react"); modelChosen && w != "" {
			advice.CapabilityWarning = w
			advice.Confidence = "low"
		}
		return advice
	}
	// A mode selected through the strategy control is stronger than any cue in
	// the prompt. In particular, a prompt that mentions a fixed procedure must
	// not turn an explicit Auto selection into Plan-Execute.
	if req == "auto" {
		advice.Mode = "auto"
		advice.RuntimeStrategy = "auto"
		advice.Confidence = "high"
		advice.Reason = "The user selected Auto; the runtime can use native tool calling when the model supports it."
		return advice
	}
	pattern := deterministicWorkflowPattern(intent)
	structuredProcedure := structuredWorkflowProcedureRequested(intent)
	// Studio's refiner formats conversational agents as numbered operating specs.
	// Do not let that formatting (or broad digest keywords) outvote explicit
	// interactive language. A literal workflow phrase is still treated as a
	// fixed-procedure cue, but it remains Plan-Execute until the UI opt-in.
	if ConversationalIntent(intent) && !explicitWorkflowPhrase(intent) && req != "workflow" {
		pattern = ""
		structuredProcedure = false
	}
	advice.DeterministicPattern = pattern
	// An interactive intent must not be captured by a pipeline pattern.
	//
	// The digest patterns match on topic keywords, so "the agent SEARCHES for
	// travel DEALS when a user asks" satisfied deal_digest and Studio built a
	// fixed two-node graph with HIGH confidence — for a prompt that said, in as
	// many words, that the agent responds on demand and asks clarifying questions
	// first. A fixed graph cannot ask a question and branch on the reply.
	//
	// Conversational language suppresses only inferred fixed-procedure signals.
	// A literal workflow request remains useful routing evidence, but generation
	// still requires the forceWorkflow opt-in handled above.
	// A fixed-looking request is routed to Plan-Execute by default. The pattern is
	// retained as evidence for the recommendation, but it is not permission to
	// generate a graph. Even wording such as "as a workflow" remains only intent
	// text; the explicit Experimental Workflow control is the opt-in boundary.
	fixedProcedure := pattern != "" || explicitWorkflowPhrase(intent) || structuredProcedure || req == "workflow"
	if req == "plan_execute" || hasPlanExecuteCues(intent) || dynamicSkillRoutingIntent(intent) || fixedProcedure {
		// Capability gate (P0-5): workflow is no longer a silent fallback. Use Auto
		// when the selected model cannot sustain planning and explain the tradeoff.
		if planOK, why := caps.SupportsPlanExecute(); modelChosen && !planOK && req != "plan_execute" {
			advice.Mode = "auto"
			advice.RuntimeStrategy = "auto"
			advice.Confidence = "low"
			advice.Reason = "Studio kept workflow generation off and selected Auto because " + modelLabel(caps) + " cannot sustain Plan-Execute: " + why + "."
			advice.CapabilityWarning = "This task looks multi-step, but the selected model is not proven for planning. Choose a stronger model or explicitly opt into Experimental Workflow and review every step."
			return advice
		}
		advice.Mode = "plan_execute"
		advice.RuntimeStrategy = "plan_execute"
		advice.Confidence = "high"
		if req == "plan_execute" {
			advice.Reason = "The user selected Plan-Execute for a tool-agent with explicit planning."
			if w := StrategyWarning(advice.Provider, advice.Model, "plan_execute"); modelChosen && w != "" {
				advice.CapabilityWarning = w
				advice.Confidence = "low"
			}
		} else {
			advice.Reason = "Soulacy selected Plan-Execute because the task needs a multi-phase or fixed procedure, while workflow generation remains an experimental opt-in."
		}
		noteFanOutNeedsWorkflow(&advice, intent)
		return advice
	}
	li := strings.ToLower(intent)
	if hasStrongReasoningCues(intent) && !interactiveAssistantIntent(li) {
		advice.Mode = "plan_execute"
		advice.RuntimeStrategy = "plan_execute"
		advice.Confidence = "medium"
		advice.Reason = "Soulacy selected Plan-Execute because the task needs multi-phase reasoning without a stable fixed workflow."
		noteFanOutNeedsWorkflow(&advice, intent)
		return advice
	}
	// Capability-driven (P0-5), replacing the model-name substring heuristic:
	// Auto leans on native tool calling and short adaptive plans, so require the
	// model to actually have them rather than inferring it from the name.
	if modelChosen && caps.Known && caps.NativeTools && caps.ArgAccuracy >= ReActMinArgAccuracy {
		advice.Mode = "auto"
		advice.RuntimeStrategy = "auto"
		advice.Confidence = "high"
		advice.Reason = "Soulacy selected Auto because " + modelLabel(caps) +
			" has native tool calling and reliable tool-argument accuracy."
		return advice
	}
	// An unprofiled model no longer falls back to an implicitly generated
	// workflow. Keep the safe default visible and warn that the model is unproven.
	if modelChosen && !caps.Known {
		advice.Mode = "auto"
		advice.RuntimeStrategy = "auto"
		advice.Confidence = "low"
		advice.Reason = "Studio selected Auto because workflow generation is experimental and " + modelLabel(caps) + " has not been profiled."
		advice.CapabilityWarning = caps.Notes + " Verify tool calling before deployment or choose a known model."
		return advice
	}
	if cat.Generation != nil && cat.Generation.Compact && hasStrongReasoningCues(intent) {
		advice.Mode = "plan_execute"
		advice.RuntimeStrategy = "plan_execute"
		advice.Confidence = "medium"
		advice.Reason = "Soulacy selected Plan-Execute because compact/local models perform better with explicit planning scaffolds."
		return advice
	}
	advice.RuntimeStrategy = "auto"
	return advice
}

func deterministicWorkflowPattern(intent string) string {
	li := strings.ToLower(intent)
	switch {
	case deterministicNotebookPodcastWorkflow(intent):
		return "NotebookLM podcast"
	case knowledgeIngestionWorkflow(intent):
		return "knowledge ingestion"
	case researchDigestWorkflow(intent):
		return "research digest"
	case dealDigestWorkflow(intent):
		return "deal digest"
	case stockDigestWorkflow(intent):
		return "market digest"
	case scheduledDeliveryWorkflow(intent):
		return "scheduled delivery"
	case structuredWorkflowProcedureRequested(intent):
		return "numbered procedure"
	case anyContains(li, "fixed workflow", "deterministic workflow", "as a workflow"):
		return "fixed workflow"
	default:
		return ""
	}
}

func interactiveAssistantIntent(li string) bool {
	return anyContains(li,
		"chat", "answer", "interactive", "assistant", "ask", "question",
		"weather", "stock advisor", "options", "explain", "help me")
}

func dynamicSkillRoutingIntent(intent string) bool {
	li := strings.ToLower(intent)
	return anyContains(li,
		"appropriate skill", "appropriate skills", "best-matching skill",
		"right skill", "selects and calls", "select the skill",
		"selects the skill", "choose the skill", "which skill", "which tool",
		"based on the parsed intent", "based on the question",
		"depending on the question", "selects the appropriate",
		"select the appropriate", "routes to the", "route to the")
}

// noteFanOutNeedsWorkflow tells the user that the specific thing they described
// lives behind the Workflow switch.
//
// Routing a fan-out request to Plan-Execute is the intended behaviour: graph
// generation is an experimental opt-in and the advisor is not allowed to choose
// it. But the reason it gave — "the task needs a multi-phase or fixed
// procedure" — describes a category, not their request. Someone who wrote out
// three named specialists working in parallel and an editor combining them gets
// back a single reasoning agent with no graph, and nothing connects that
// outcome to the switch that would have changed it. They are left to conclude
// the product cannot do what they asked, when it can.
//
// Advice only: the routing is untouched, and this uses the same bar as
// StructureShortfall and the template guard so all three agree about what
// counts as a described structure.
func noteFanOutNeedsWorkflow(advice *StrategyAdvice, intent string) {
	if advice == nil || !StructureNamedButUnbuildable(intent) {
		return
	}
	msg := "You described several specialists working in parallel. That is a fixed graph, which Studio only " +
		"builds when you turn on the Workflow switch — this reasoning agent will work through the same steps " +
		"one at a time instead."
	if advice.CapabilityWarning == "" {
		advice.CapabilityWarning = msg
		return
	}
	advice.CapabilityWarning += " " + msg
}
