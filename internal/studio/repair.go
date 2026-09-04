package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// residualDependencyIssues returns the data-flow dependency problems remaining
// in a draft (a reference to a flow var no earlier step produces). Pure.
func residualDependencyIssues(draft Draft) []PreflightIssue {
	var issues []PreflightIssue
	checkDataFlow(draft, func(sev, kind, node, msg, fix string) {
		if kind == "dependency" && sev == "block" {
			issues = append(issues, PreflightIssue{Severity: sev, Kind: kind, NodeID: node, Message: msg, Fix: fix})
		}
	})
	return issues
}

// RepairContractStructure performs deterministic, platform-owned structural
// repair. It is intentionally broader than RepairWiring: when the contract says
// the architecture itself is bad, Soulacy replaces the graph/strategy with the
// deterministic planner for the original intent instead of asking an LLM to
// redesign the workflow.
func RepairContractStructure(draft *Draft, intent string, cat Catalog, answers map[string]string, contract ContractResult) bool {
	if draft == nil {
		return false
	}
	intent = strings.TrimSpace(nonEmpty(intent, draft.Intent))
	if intent == "" {
		return false
	}
	needsReplace := len(draft.Flow.Nodes) == 0 && !draft.IsAgent()
	for _, check := range contract.Checks {
		if check.Status != "block" {
			continue
		}
		switch check.ID {
		case "architecture.empty", "architecture.size", "graph.integrity":
			needsReplace = true
		default:
			if strings.Contains(strings.ToLower(check.Message), "not valid json") ||
				strings.Contains(strings.ToLower(check.Message), "unknown tool") ||
				strings.Contains(strings.ToLower(check.Message), "no runnable steps") {
				needsReplace = true
			}
		}
	}
	if draft.IsAgent() && draft.Strategy == "react" && !explicitReActRequested(intent) {
		if res, ok := CompileDeterministicAgent(intent, cat, AdviseStrategy(intent, cat, "", false).RuntimeStrategy, answers); ok {
			*draft = res.Workflow
			return true
		}
	}
	if !needsReplace {
		return false
	}
	advice := AdviseStrategy(intent, cat, "", false)
	var res Result
	var ok bool
	if advice.Mode == "workflow" {
		res, ok = CompileDeterministicWorkflow(intent, cat, answers)
	} else {
		res, ok = CompileDeterministicAgent(intent, cat, advice.RuntimeStrategy, answers)
	}
	if !ok {
		return false
	}
	*draft = res.Workflow
	return true
}

// RepairWithProblems is the general LLM repair: given a draft and a list of
// concrete problems (preflight blockers, validation messages, or a runtime
// error), it asks the model to return the CORRECTED full draft — fixing only
// those problems, keeping everything else identical, grounded in the real
// catalog so it uses exact tool names. Handles BOTH workflows and ReAct agents.
// Best-effort: returns the original draft + false on any failure.
func RepairWithProblems(ctx context.Context, llm LLM, draft Draft, problems []string, cat Catalog) (Draft, bool) {
	if llm == nil || len(problems) == 0 {
		return draft, false
	}
	raw, err := llm.Complete(ctx, buildProblemRepairPrompt(draft, problems, cat))
	if err != nil {
		return draft, false
	}
	fixed, err := ParseDraft(raw)
	if err != nil || (len(fixed.Flow.Nodes) == 0 && !fixed.IsAgent() && !draft.IsAgent() && len(draft.Flow.Nodes) > 0) {
		// Parse failed, or the model returned something structurally empty for a
		// workflow that had nodes — don't accept a worse draft.
		return draft, false
	}
	// Preserve the draft's kind + identity the model shouldn't change.
	if fixed.Strategy == "" {
		fixed.Strategy = draft.Strategy
	}
	if strings.TrimSpace(fixed.Name) == "" {
		fixed.Name = draft.Name
	}
	if strings.TrimSpace(fixed.Intent) == "" {
		fixed.Intent = draft.Intent
	}
	// Re-run deterministic normalization/repair over the model's result.
	normalizeTrigger(&fixed, fixed.Intent)
	if !fixed.IsAgent() {
		normalizeFlow(&fixed)
		reconcilePorts(&fixed)
		RepairWiring(&fixed, cat)
		classifyFlowNodes(&fixed.Flow)
		ensureNewAgents(&fixed, cat)
	}
	return fixed, true
}

// buildProblemRepairPrompt asks the model to fix the listed problems and return
// the full corrected draft JSON.
func buildProblemRepairPrompt(draft Draft, problems []string, cat Catalog) string {
	var sb strings.Builder
	sb.WriteString("You are repairing an existing Soulacy ")
	if draft.IsAgent() {
		sb.WriteString("ReAct/Plan-Execute AGENT")
	} else {
		sb.WriteString("workflow")
	}
	sb.WriteString(". Fix ONLY the problems listed below. Keep everything else byte-for-byte the same — same node ids, same output variable names, same structure. Do not add or remove steps unless a problem explicitly requires it.\n\n")
	sb.WriteString("Problems to fix:\n")
	for _, p := range problems {
		sb.WriteString("- ")
		sb.WriteString(strings.TrimSpace(p))
		sb.WriteString("\n")
	}
	sb.WriteString("\nCommon fixes: a referenced {{ .var }} must match an upstream step's output variable; tool inputs must be valid JSON with the tool's REAL argument names; template expressions must be valid Go templates (no double dots, balanced {{ }}); python nodes must define `def run(inputs)`; MCP tool names must be the EXACT mcp__server__tool from the catalog; wire an async job's id from the step that produced it.\n\n")

	if b, err := json.Marshal(draft); err == nil {
		sb.WriteString("Current draft JSON:\n")
		sb.Write(b)
		sb.WriteString("\n\n")
	}

	writeCatalogGrounding(&sb, cat)

	sb.WriteString("\nReturn ONLY the corrected full draft as a single JSON object (same schema as the input). No prose, no markdown, no code fences.\n")
	return sb.String()
}

// FocusedRepair is the exported entry point for the focused-LLM repair: it asks
// the model to fix ONLY the nodes with residual data-flow problems (not
// regenerate the whole agent). Returns true if it changed the draft. Used by the
// "Fix automatically" action for semantic var mismatches the deterministic
// passes can't resolve (e.g. {{ .title }} → {{ .notebook_params.title }}).
func FocusedRepair(ctx context.Context, llm LLM, draft *Draft) bool {
	return focusedRepair(ctx, llm, draft)
}

// focusedRepair attempts ONE local-model-friendly repair pass: if the draft has
// residual dependency blockers, it asks the model to fix ONLY the broken nodes
// (via BuildRepairPrompt), parses the returned nodes, and merges them back by
// id. Returns true if it changed the draft. Best-effort: any parse/LLM failure
// leaves the draft untouched (the caller's validation still guards correctness).
func focusedRepair(ctx context.Context, llm LLM, draft *Draft) bool {
	if draft == nil || llm == nil {
		return false
	}
	issues := residualDependencyIssues(*draft)
	if len(issues) == 0 {
		return false
	}
	raw, err := llm.Complete(ctx, BuildRepairPrompt(*draft, issues))
	if err != nil {
		return false
	}
	fixed, err := parseRepairNodes(raw)
	if err != nil || len(fixed) == 0 {
		return false
	}
	return mergeNodesByID(draft, fixed)
}

// parseRepairNodes extracts {"nodes":[...]} from focused-repair model output,
// tolerant of fences/prose (reuses the draft fence stripper).
func parseRepairNodes(raw string) ([]sdkr.FlowNode, error) {
	s := stripFences(strings.TrimSpace(raw))
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < 0 || end < start {
		return nil, fmt.Errorf("studio: no JSON object in repair output")
	}
	var payload struct {
		Nodes []sdkr.FlowNode `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &payload); err != nil {
		return nil, err
	}
	return payload.Nodes, nil
}

// mergeNodesByID replaces draft nodes with same-id fixed nodes. Unknown ids are
// ignored (focused repair must not add/remove nodes). Returns true if anything
// changed.
func mergeNodesByID(draft *Draft, fixed []sdkr.FlowNode) bool {
	idx := map[string]int{}
	for i, n := range draft.Flow.Nodes {
		idx[n.ID] = i
	}
	changed := false
	for _, fn := range fixed {
		if i, ok := idx[strings.TrimSpace(fn.ID)]; ok {
			draft.Flow.Nodes[i] = fn
			changed = true
		}
	}
	return changed
}

// AutoRepair runs the DETERMINISTIC repair pass over a freshly-parsed draft and
// reports what remains broken. It is the local-first repair loop's first stage
// (Stories #16/#17): fix everything we can without the model (auto-wiring), then
// return the residual blockers so the caller can decide whether a single focused
// LLM repair is warranted. Pure except for mutating draft.
//
// It returns the preflight-style blockers that survive auto-wiring, scoped to
// the data-flow/required-arg issues a focused repair can address.
func AutoRepair(draft *Draft, cat Catalog) []PreflightIssue {
	if draft == nil {
		return nil
	}
	AutoWire(draft, cat)

	// Collect the residual fixable issues (missing required args / dangling
	// data-flow refs) via the existing pure checks.
	var issues []PreflightIssue
	add := func(sev, kind, node, msg, fix string) {
		if kind == "dependency" {
			issues = append(issues, PreflightIssue{Severity: sev, Kind: kind, NodeID: node, Message: msg, Fix: fix})
		}
	}
	checkDataFlow(*draft, add)
	return issues
}

// BuildRepairPrompt builds a FOCUSED repair instruction: it shows the model ONLY
// the broken nodes and the specific problems, and asks for corrected nodes —
// never a full regeneration (Story #17). This keeps the task small enough for a
// local model. The model is told to return a JSON object {"nodes":[...]} with
// only the fixed nodes, preserving ids and output var names.
func BuildRepairPrompt(draft Draft, issues []PreflightIssue) string {
	// Index broken node ids.
	broken := map[string]bool{}
	for _, is := range issues {
		if is.NodeID != "" {
			broken[is.NodeID] = true
		}
	}

	var sb strings.Builder
	sb.WriteString("You are repairing ONE part of an existing automation workflow. ")
	sb.WriteString("Do NOT redesign it. Fix ONLY the specific problems listed, keeping every node id and output variable name exactly as-is.\n\n")
	sb.WriteString("Problems to fix:\n")
	for _, is := range issues {
		sb.WriteString("- ")
		if is.NodeID != "" {
			sb.WriteString("node \"")
			sb.WriteString(is.NodeID)
			sb.WriteString("\": ")
		}
		sb.WriteString(is.Message)
		if is.Fix != "" {
			sb.WriteString(" (")
			sb.WriteString(is.Fix)
			sb.WriteString(")")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\nThe broken node(s), as JSON:\n")
	for _, n := range draft.Flow.Nodes {
		if !broken[n.ID] {
			continue
		}
		if b, err := json.Marshal(n); err == nil {
			sb.WriteString(string(b))
			sb.WriteString("\n")
		}
	}

	sb.WriteString("\nReturn ONLY a JSON object of the form {\"nodes\":[ ... ]} containing the corrected version of just those node(s). ")
	sb.WriteString("Wire missing values from upstream outputs using {{ .var }} where a producing step exists. No prose, no code fences.\n")
	return sb.String()
}
