package studio

import "strings"

// BuildPlan returns the deterministic pre-plan skeleton for an intent, derived
// from the best-matching known pattern. When a plan exists, Studio asks the
// model to REALISE these exact steps (fill in tool names + inputs) instead of
// inventing an architecture — the key to making small/local models reliable
// (local-first pivot, Stories #13/#14). Returns nil when no pattern matches, in
// which case the compiler falls back to free-form generation.
func BuildPlan(intent string, cat Catalog) []PlanStep {
	matched := MatchPatterns(intent, cat, 1)
	if len(matched) == 0 || len(matched[0].Plan) == 0 {
		return nil
	}
	// Return a copy so callers can't mutate the shared pattern catalog.
	plan := make([]PlanStep, len(matched[0].Plan))
	copy(plan, matched[0].Plan)
	return plan
}

// writePlanGrounding appends the deterministic plan to the compile prompt and
// instructs the model to realise it exactly — same steps, same order, same
// wiring — only filling in concrete tool/agent names (from the catalog) and
// input values. No-op when there's no plan.
func writePlanGrounding(sb *strings.Builder, intent string, cat Catalog) {
	plan := BuildPlan(intent, cat)
	if len(plan) == 0 {
		return
	}
	sb.WriteString("\nDETERMINISTIC PLAN — realise EXACTLY these steps, in THIS order, with THIS wiring. Do NOT add, drop, or reorder steps; only fill in the concrete tool/agent names (from the catalog above) and the input values. Keep the given output variable names so the wiring holds:\n")
	for i, st := range plan {
		sb.WriteString("  ")
		sb.WriteString(itoa(i + 1))
		sb.WriteString(". id=")
		sb.WriteString(st.ID)
		sb.WriteString(" [")
		sb.WriteString(st.Kind)
		sb.WriteString("] — ")
		sb.WriteString(st.Description)
		if st.Uses != "" {
			sb.WriteString(". Use: ")
			sb.WriteString(st.Uses)
		}
		if st.Output != "" {
			sb.WriteString(". Output var: ")
			sb.WriteString(st.Output)
		}
		if st.Fills != "" {
			sb.WriteString(". Fill: ")
			sb.WriteString(st.Fills)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("If a step's capability is not available in the catalog, keep the step but use the closest available tool or a python node; never silently drop a step.\n")
}
