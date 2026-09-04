package studio

import (
	"strings"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// DraftExplanation is a plain-language description of a generated agent (Story
// #10): what it's for, when it runs, the steps it takes, the tools/channels it
// uses, the architecture chosen and why, and anything the user still needs to
// configure. It is derived deterministically from the draft (no LLM call) so it
// is fast, free, and always consistent with what was actually built.
type DraftExplanation struct {
	Purpose  string   `json:"purpose"`
	Trigger  string   `json:"trigger"`
	Steps    []string `json:"steps"`
	Tools    []string `json:"tools,omitempty"`
	Agents   []string `json:"agents,omitempty"`
	Channels []string `json:"channels,omitempty"`
	// Skills the agent loads (from read_skill nodes) and KnowledgeBases it is
	// attached to — so the user sees which specialized capabilities/knowledge the
	// agent has, and why (Stories #5/#7).
	Skills         []string `json:"skills,omitempty"`
	KnowledgeBases []string `json:"knowledge_bases,omitempty"`
	Architecture   string   `json:"architecture"` // workflow | react | plan_execute
	ArchReason     string   `json:"arch_reason,omitempty"`
	// NeedsConfig are short, plain-language notes about setup the user may still
	// have to do (e.g. an unconfigured channel). Populated by the caller from the
	// preflight report when available; ExplainDraft leaves it empty.
	NeedsConfig []string `json:"needs_config,omitempty"`
}

// ExplainDraft builds the plain-language explanation from a draft. Pure +
// deterministic. Reuses the same ordered-step + describe helpers the saved
// system prompt uses, so the explanation matches the agent's real behaviour.
func ExplainDraft(draft Draft) DraftExplanation {
	exp := DraftExplanation{
		Purpose: draftPurpose(draft),
		Trigger: describeTrigger(draft.Trigger),
	}

	if draft.IsAgent() {
		exp.Steps = append(exp.Steps, "Dynamically execute the strategy using ReAct planning.")
		for _, t := range draft.Tools {
			exp.Tools = append(exp.Tools, friendlyToolName(t))
		}
		for _, a := range draft.NewAgents {
			exp.Agents = append(exp.Agents, a.ID)
		}
	} else {
		byID := make(map[string]sdkr.FlowNode, len(draft.Flow.Nodes))
		for _, n := range draft.Flow.Nodes {
			byID[n.ID] = n
		}
		for _, id := range orderedNodeIDs(draft.Flow) {
			if n, ok := byID[id]; ok {
				exp.Steps = append(exp.Steps, describeNode(n))
			}
		}

		// Distinct tools + agents the workflow uses, in first-seen order.
		seenTool, seenAgent := map[string]bool{}, map[string]bool{}
		for _, n := range draft.Flow.Nodes {
			if t := strings.TrimSpace(n.Tool); t != "" && !seenTool[t] {
				seenTool[t] = true
				exp.Tools = append(exp.Tools, friendlyToolName(t))
			}
			if a := strings.TrimSpace(n.Agent); a != "" && !seenAgent[a] {
				seenAgent[a] = true
				exp.Agents = append(exp.Agents, a)
			}
		}
	}
	exp.Channels = append(exp.Channels, draft.Channels...)
	exp.Skills = usedSkills(draft.Flow)
	exp.KnowledgeBases = append(exp.KnowledgeBases, draft.Knowledge...)

	arch := draft.Recommendation
	if arch == nil {
		inferred := inferArchitecture(draft)
		arch = &inferred
	}
	exp.Architecture = arch.Mode
	exp.ArchReason = arch.Rationale
	return exp
}

// draftPurpose extracts a one-line purpose: the first sentence of the system
// prompt if present, else a name-based fallback.
func draftPurpose(draft Draft) string {
	sp := strings.TrimSpace(draft.SystemPrompt)
	if sp != "" {
		if i := strings.IndexAny(sp, ".!?"); i > 0 {
			return strings.TrimSpace(sp[:i+1])
		}
		if len(sp) > 160 {
			return strings.TrimSpace(sp[:160]) + "…"
		}
		return sp
	}
	name := strings.TrimSpace(draft.Name)
	if name == "" {
		return "An automation built in Soulacy Studio."
	}
	return "An automation named " + name + "."
}

// friendlyToolName makes an MCP tool name readable: mcp__notebooklm__create →
// "notebooklm: create". Builtins pass through unchanged.
func friendlyToolName(tool string) string {
	if !strings.HasPrefix(tool, "mcp__") {
		return tool
	}
	rest := strings.TrimPrefix(tool, "mcp__")
	if i := strings.Index(rest, "__"); i >= 0 {
		return rest[:i] + ": " + strings.ReplaceAll(rest[i+2:], "_", " ")
	}
	return tool
}

// inferArchitecture is the deterministic backstop for the execution-mode
// recommendation (Story #4) when the model didn't supply one. It mirrors the
// reasoning the compiler prompt asks the model to apply, in plain language.
func inferArchitecture(draft Draft) Recommendation {
	if draft.IsAgent() {
		return Recommendation{
			Mode:      "react",
			Rationale: "This task requires dynamic execution, API polling, or reasoning that a fixed DAG workflow cannot reliably handle.",
		}
	}
	// A scheduled, fixed pipeline is the canonical "workflow" case.
	if strings.EqualFold(strings.TrimSpace(draft.Trigger.Type), "schedule") {
		return Recommendation{
			Mode:      "workflow",
			Rationale: "This runs the same fixed steps on a schedule, so a deterministic workflow is the right fit.",
		}
	}
	// Heuristic: many MCP/tool steps whose outputs feed later steps tend to be
	// exploratory; but without that signal, a small linear graph is a workflow.
	toolNodes := 0
	for _, n := range draft.Flow.Nodes {
		if n.Kind == "tool" {
			toolNodes++
		}
	}
	if toolNodes >= 4 {
		return Recommendation{
			Mode:      "react",
			Rationale: "The task chains several tool calls where later steps depend on earlier results, which a reasoning (ReAct) agent handles more robustly than a frozen graph.",
		}
	}
	return Recommendation{
		Mode:      "workflow",
		Rationale: "The steps are known up front and run in a fixed order, so a workflow is the simplest reliable choice.",
	}
}
