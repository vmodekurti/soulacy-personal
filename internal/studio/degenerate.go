// degenerate.go — reject a draft that parsed as JSON but isn't a real workflow.
//
// A weak builder model (typically a small local model that can't hold Studio's
// schema) sometimes emits structurally-valid JSON whose "steps" are empty
// placeholders: nodes with no tool, no agent and no code, and no edges between
// them. Rendering that produces a canvas full of meaningless BRANCH boxes and a
// pile of "no incoming edge" warnings — the user is left debugging debris
// instead of being told the model failed.
//
// DegenerateReason names the problem in plain English so the caller can fail
// loudly and tell the operator to use a stronger builder model. Pure and
// unit-tested.
package studio

import "strings"

// DegenerateReason returns a plain-English reason when a draft is not a usable
// workflow, or "" when the draft looks real. It is deliberately conservative —
// it only fires on drafts that could not possibly execute.
func DegenerateReason(d Draft) string {
	// A reasoning agent (react / plan_execute) legitimately carries NO flow —
	// its behavior lives in the strategy loop, not a graph. Never flag it.
	if strings.TrimSpace(d.Strategy) != "" {
		return ""
	}
	nodes := d.Flow.Nodes
	if len(nodes) == 0 {
		// An empty flow is handled by the normal validation path.
		return ""
	}

	// Does ANY step actually do work? A node earns its keep by naming a tool,
	// delegating to an agent, carrying Python code, or being an llm step.
	real := 0
	for _, n := range nodes {
		if strings.TrimSpace(n.Tool) != "" ||
			strings.TrimSpace(n.Agent) != "" ||
			strings.TrimSpace(n.Code) != "" ||
			strings.EqualFold(strings.TrimSpace(n.Kind), "llm") {
			real++
		}
	}
	if real == 0 {
		return "every step is an empty placeholder — the model assigned no tool, agent, or code to any of them"
	}

	// Multiple steps with nothing connecting them can never execute as a flow.
	if len(nodes) >= 2 && len(d.Flow.Edges) == 0 {
		return "the steps are not connected to each other — the model produced no edges between them"
	}

	return ""
}
