package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/soulacy/soulacy/internal/agentprompt"
)

// agentSpecPayload is the JSON the model returns for a ReAct/Plan-Execute agent
// (local-first pivot). Unlike a workflow it carries NO flow graph — the engine's
// reasoning loop drives the listed tools/skills/peers dynamically.
type agentSpecPayload struct {
	Name         string     `json:"name"`
	SystemPrompt string     `json:"system_prompt"`
	Trigger      Trigger    `json:"trigger"`
	Channels     []string   `json:"channels"`
	Tools        []string   `json:"tools"`     // EXACT builtin + mcp__ tool names
	Skills       []string   `json:"skills"`    // installed skill names to enable
	Knowledge    []string   `json:"knowledge"` // KB names to attach
	NewAgents    []NewAgent `json:"new_agents"`
	Rationale    string     `json:"rationale"`
	// The agent contract, stated separately from the system prompt so the Build
	// screen can SHOW it and the operator can edit it. Generation never produced
	// these, which is why that panel opened blank on every generated agent.
	Goal               string `json:"goal"`
	Instructions       string `json:"instructions"`
	CompletionCriteria string `json:"completion_criteria"`
}

// BuildAgentPrompt builds the instruction for generating a ReAct/Plan-Execute
// AGENT (not a workflow). The model picks tools from the grounded catalog and
// writes a system prompt that teaches the agent how to accomplish the task by
// reasoning over those tools — including loops and async polling that a fixed
// graph can't express.
func BuildAgentPrompt(intent string, catalog Catalog, strategy string, answers map[string]string) string {
	var sb strings.Builder
	sb.WriteString("You are the Soulacy Studio agent designer. Turn the user's intent into a ")
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "plan_execute":
		sb.WriteString("PLAN-EXECUTE")
	case "auto":
		sb.WriteString("AUTO tool-calling")
	default:
		sb.WriteString("ReAct (reasoning-loop)")
	}
	sb.WriteString(" agent — NOT a fixed workflow. The agent is driven by its system prompt and tool allowlist rather than a canvas graph. AUTO agents use the model's native tool-calling loop for ordinary tool use; ReAct/Plan-Execute agents are for more explicit long-running reasoning.\n\n")

	sb.WriteString("Output RULES:\n")
	sb.WriteString("- Respond with ONLY a single JSON object. No prose, no markdown, no code fences.\n")
	sb.WriteString("- Shape EXACTLY:\n")
	sb.WriteString(`{
  "name": "<short human name>",
  "system_prompt": "<rich instructions: the agent's role, the goal, the ordered approach in plain language (incl. looping over each item and polling async jobs until ready), the exact tools to use and when, edge-case/error handling, and the final output format>",
  "trigger": { "type": "schedule|channel|webhook|manual", "config": { "cron": "0 7 * * *" } },
  "channels": ["<channel ids the user named, from the installed list above; omit entirely if they named none>"],
  "tools": ["<EXACT builtin or mcp__server__tool names from the catalog the agent may call>"],
  "skills": ["<EXACT installed skill names to enable, if any>"],
  "knowledge": ["<EXACT KB names to attach, if relevant>"],
  "new_agents": [ { "id":"...", "name":"...", "description":"...", "system_prompt":"..." } ],
  "goal": "<one sentence: what a successful run achieves, in the user's terms>",
  "instructions": "<how the agent should behave — tone, constraints, what to prefer or avoid. One short paragraph.>",
  "completion_criteria": "<the checkable statement of done: what must be true before the run may stop>",
  "rationale": "<1-2 sentences on why a reasoning agent fits this task>"
}` + "\n\n")

	sb.WriteString("Guidance:\n")
	sb.WriteString("- DO NOT emit a flow/graph. The agent is driven by its system_prompt + tools, not nodes/edges.\n")
	sb.WriteString("- tools MUST be EXACT names from the catalog below (builtins like web_search, or full mcp__server__tool names). Never invent tool names; if a capability is missing, describe it in the prompt and pick the closest real tool.\n")
	sb.WriteString("- If the agent uses channel.send, its arguments are exactly {\"channel\":\"telegram|slack|discord|whatsapp\", \"to\":\"destination id or chat/thread id\", \"text\":\"message text\"}. The field is `text`, not `message`. Omit `to` only when the run arrived from that channel or the channel has a configured default outbound destination.\n")
	sb.WriteString("- If delivery routing is uncertain or channel.send fails, call channel.status once and follow its diagnosis/fix instead of retrying channel.send with guessed fields.\n")
	sb.WriteString("- For ordinary interactive replies, do not call channel.send just to answer the user. Return the answer normally; use channel.send only for explicit out-of-band delivery.\n")
	sb.WriteString("- The system_prompt is where the procedure lives: spell out the steps as INSTRUCTIONS (e.g. \"create the notebook, then add EACH source one at a time, then start audio generation, then POLL the status until it reports ready, then deliver the link\").\n")
	sb.WriteString("- The system_prompt MUST include a completion contract: the run is not done until every requested operation is complete. Raw search JSON, IDs, delivery receipts, or intermediate tool output are not final answers; if a later operation cannot complete, return a clear fallback naming the failed step.\n")
	sb.WriteString("- ")
	sb.WriteString(agentprompt.InstructionForBuilders())
	sb.WriteString("\n")
	sb.WriteString("- Include authentication/setup steps the user asked for as the FIRST instruction if a matching tool exists (e.g. refresh/login tools).\n")
	sb.WriteString("- Invent a peer agent ONLY if needed, and give it a full reusable system_prompt in new_agents.\n")
	sb.WriteString("- ALSO state the contract in its own fields: `goal`, `instructions` and `completion_criteria`. These are shown to the operator as an editable contract, so write them for a person to read — not as a restatement of the JSON above. `completion_criteria` must be checkable (\"the digest has been delivered to Telegram\"), never vague (\"the task is done\").\n")
	sb.WriteString("- Pull concrete values from the user's words (queries, counts, schedule cadence, target channel).\n\n")
	writeAgentStrategyGuidance(&sb, strategy)

	sb.WriteString(GenerationProfilePromptBlock(catalog.Generation))
	// Same correction the workflow builder gets. Without this the coverage retry
	// re-sent an identical prompt for every agent and could not possibly succeed.
	writeMustUseBlock(&sb, catalog, false)
	writeCatalogGrounding(&sb, catalog)
	writePatternGrounding(&sb, intent, catalog)

	if len(answers) > 0 {
		sb.WriteString("\nThe user already answered these clarifying questions — honor them:\n")
		for _, k := range sortedKeys(answers) {
			if v := strings.TrimSpace(answers[k]); v != "" {
				sb.WriteString("- ")
				sb.WriteString(k)
				sb.WriteString(": ")
				sb.WriteString(v)
				sb.WriteString("\n")
			}
		}
	}

	sb.WriteString("\nIntent:\n")
	sb.WriteString(intent)
	sb.WriteString("\n")
	return sb.String()
}

func writeAgentStrategyGuidance(sb *strings.Builder, strategy string) {
	sb.WriteString("Strategy-specific system_prompt requirements:\n")
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "plan_execute":
		sb.WriteString("- Plan-Execute agents MUST keep a compact numbered plan internally, execute it phase by phase, and mark a phase complete only after tool output proves the success criteria were met.\n")
		sb.WriteString("- Include explicit success criteria for each major phase: discovery, validation, creation, polling, delivery, and final confirmation when those phases apply.\n")
		sb.WriteString("- Bound loops and polling: name the item list, process each item once unless a retry is justified, set a clear max poll/retry count, and preserve partial results if a downstream phase fails.\n")
		sb.WriteString("- On a blocker, revise the plan at most once using the actual error. Do not keep retrying the same failed call with identical arguments.\n")
		sb.WriteString("- Final response must state completed phases, skipped items, failed phase if any, and the best usable artifact/result.\n\n")
	case "auto":
		sb.WriteString("- Auto agents should rely on the model's native tool-calling ability. Do not ask the model to emit Thought/Action JSON, ReAct transcripts, or a visible plan protocol.\n")
		sb.WriteString("- The system_prompt should focus on role, tool choice rules, safety/delivery rules, completion criteria, and the final answer format.\n")
		sb.WriteString("- Use Auto for capable models unless the user explicitly needs a visible step trace, strict phased execution, or a long asynchronous job.\n\n")
	default:
		sb.WriteString("- ReAct agents MUST follow an observe-decide-act cycle: inspect the request/context, choose the smallest next action, call exactly one tool, read the actual result, then decide the next step.\n")
		sb.WriteString("- Validate required tool arguments before every call. Repair obvious aliases once (for example message->text or ticker->symbol), but never retry the same failed tool call with identical arguments.\n")
		sb.WriteString("- If the same tool/argument shape fails twice, switch approach or stop with a clear blocker and partial result.\n")
		sb.WriteString("- Keep reasoning internal. The final answer must be user-facing, not raw tool JSON, ReAct JSON, or internal scratchpad text.\n\n")
	}
}

// CompileAgent generates a tool/reasoning agent Draft (no flow) from an intent.
// strategy is "auto" (default native tool calling), "react" (manual/advanced),
// or "plan_execute".
// Like Compile it is tolerant of fenced/prose-wrapped model output; it validates
// that the result has a system prompt and at least one tool or peer agent.
func CompileAgent(ctx context.Context, llm LLM, intent string, catalog Catalog, strategy string, answers map[string]string) (Result, error) {
	if strings.TrimSpace(intent) == "" {
		return Result{}, fmt.Errorf("studio: intent is required")
	}
	if llm == nil {
		return Result{}, fmt.Errorf("studio: no LLM configured")
	}
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	switch strategy {
	case "auto", "react", "plan_execute":
	default:
		strategy = "auto"
	}

	prompt := BuildAgentPrompt(intent, catalog, strategy, answers)
	raw, err := llm.Complete(ctx, prompt)
	if err != nil {
		return Result{}, fmt.Errorf("studio: llm complete: %w", err)
	}
	payload, perr := parseAgentSpec(raw)
	if perr != nil {
		if shouldRepairMalformedDraft(catalog.Generation) {
			if repairedRaw, repairErr := repairMalformedDraft(ctx, llm, prompt, raw, perr); repairErr == nil {
				if repairedPayload, parseErr := parseAgentSpec(repairedRaw); parseErr == nil {
					payload = repairedPayload
					perr = nil
				}
			}
		}
	}
	if perr != nil {
		return Result{}, perr
	}

	draft := Draft{
		Name:         strings.TrimSpace(payload.Name),
		SystemPrompt: agentprompt.EnsureShared(payload.SystemPrompt),
		Intent:       intent,
		// The user's original words, so capability grounding below matches against
		// what they asked for rather than the refiner's expanded version of it.
		RawIntent: strings.TrimSpace(catalog.RawIntent),
		Trigger:   payload.Trigger,
		Channels:  trimStrings(payload.Channels),
		Strategy:  strategy,
		Tools:     trimStrings(payload.Tools),
		Skills:    trimStrings(payload.Skills),
		Knowledge: trimStrings(payload.Knowledge),
		NewAgents: payload.NewAgents,
		Recommendation: &Recommendation{
			Mode:      strategy,
			Rationale: strings.TrimSpace(payload.Rationale),
		},
		Policy: contractPolicyFrom(payload, intent),
	}
	// Make the exact request visible in the Build editor immediately. The same
	// helper runs again on save, making this idempotent and protecting agents
	// generated by both model and deterministic builder paths.
	draft.SystemPrompt = strings.TrimSpace(stripRequestedOutcome(draft.SystemPrompt) + "\n\n" + requestedOutcomePrompt(draft.RawIntent, draft.Intent))
	if draft.Name == "" {
		draft.Name = "Studio Agent"
	}
	normalizeTrigger(&draft, intent)
	// Guarantee every referenced/invented peer agent has a full reusable profile.
	ensureNewAgents(&draft, catalog)

	// Hard contract: a usable agent needs a system prompt AND at least one tool
	// or peer to act with.
	if draft.SystemPrompt == "" {
		return Result{}, fmt.Errorf("studio: agent spec has no system prompt")
	}
	if len(draft.Tools) == 0 && len(draft.NewAgents) == 0 {
		return Result{}, fmt.Errorf("studio: agent spec lists no tools or agents to act with")
	}

	// Capture any skills the model named that aren't installed BEFORE grounding
	// drops them, so they surface in "Needs setup" instead of vanishing silently.
	missingSkills := MissingSkillSuggestions(draft.Skills, catalog)

	// Ground the model's capability picks against the live index: verify + correct
	// chosen skills/tools, and inject installed skills the intent clearly calls for.
	// This is the BASELINE — the user can still add/modify skills and tools by hand
	// in the agent editor. Runs before ExplainDraft so the explanation reflects the
	// grounded set.
	groundNotes := GroundAgentCapabilities(&draft, catalog)
	defaultNotes := applyGenerationDefaults(&draft, intent)
	// The operator's explicit channel choice wins over whatever the model named.
	ApplyChannelAnswers(&draft, answers)

	explanation := ExplainDraft(draft)
	notes := []string{"Generated a " + recoLabelGo(strategy) + " agent — it reasons over its tools rather than running a fixed graph."}
	if catalog.Generation != nil && catalog.Generation.Local && catalog.Generation.Compact {
		notes = append(notes, "Local-model Studio mode: used stricter JSON repair and catalog-grounded tool selection because the builder is a compact local model.")
	}
	if sk := draft.Skills; len(sk) > 0 {
		notes = append(notes, "Enables skill(s): "+strings.Join(sk, ", ")+".")
	}
	notes = append(notes, groundNotes...)
	notes = append(notes, defaultNotes...)
	if catalog.Generation != nil {
		gp := *catalog.Generation
		gp.PatternMatched = len(MatchPatterns(intent, catalog, 1)) > 0
		gp.LessonsApplied = len(catalog.Lessons)
		gp.Confidence, gp.NextAction = generationConfidence(gp)
		catalog.Generation = &gp
	}
	return Result{
		Workflow:    draft,
		Notes:       notes,
		Suggestions: append(suggestMissingAgent(draft, catalog), missingSkills...),
		Explanation: &explanation,
		Generation:  catalog.Generation,
	}, nil
}

// recoLabelGo is a tiny server-side label for the strategy.
func recoLabelGo(strategy string) string {
	switch {
	case strings.EqualFold(strategy, "plan_execute"):
		return "Plan-Execute"
	case strings.EqualFold(strategy, "auto"):
		return "Auto tool-calling"
	default:
		return "ReAct"
	}
}

// parseAgentSpec tolerantly extracts the agent JSON (fence/prose tolerant).
func parseAgentSpec(raw string) (agentSpecPayload, error) {
	s := stripFences(strings.TrimSpace(raw))
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < 0 || end < start {
		return agentSpecPayload{}, fmt.Errorf("studio: no JSON object found in agent spec output")
	}
	var p agentSpecPayload
	body := s[start : end+1]
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		// See escapeRawControlChars: an agent spec carries a multi-line system
		// prompt, so this is the failure this parser hits most. Observed live —
		// "parse agent spec: invalid character '\n' in string literal" threw away
		// the whole generated workflow and fell back to a two-node skeleton.
		if err2 := json.Unmarshal([]byte(escapeRawControlChars(body)), &p); err2 != nil {
			return agentSpecPayload{}, fmt.Errorf("studio: parse agent spec: %w", err)
		}
		return p, nil
	}
	return p, nil
}

// suggestMissingAgent flags tools/skills/KBs the agent references that aren't in
// the catalog, reusing the MCP-aware tool check.
func suggestMissingAgent(draft Draft, cat Catalog) []Suggestion {
	if len(cat.Tools) == 0 && len(cat.MCP) == 0 {
		return nil
	}
	toolSet := lowerSet(cat.Tools)
	mcpSet := map[string]bool{}
	for _, srv := range cat.MCP {
		for _, t := range srv.Tools {
			if n := strings.TrimSpace(t.Name); n != "" {
				mcpSet[strings.ToLower(n)] = true
			}
		}
	}
	var out []Suggestion
	for _, t := range draft.Tools {
		key := strings.ToLower(strings.TrimSpace(t))
		if key == "" {
			continue
		}
		installed := toolSet[key]
		if !installed && strings.HasPrefix(key, "mcp__") {
			installed = mcpSet[key]
		}
		if !installed {
			out = append(out, Suggestion{Kind: "tool", Name: t, Installed: false, Reason: capabilityReason("tool", t, false)})
		}
	}
	return out
}

// contractPolicyFrom turns the designer's stated contract into the draft policy
// the Build screen shows.
//
// The Goal falls back to the intent because that IS what the user said a
// successful run achieves — an empty Goal box on a freshly generated agent read
// as "you forgot to fill this in" when nothing had ever filled it. The other two
// are left empty when the model says nothing rather than being invented, so the
// missing-completion-criteria warning stays honest.
func contractPolicyFrom(payload agentSpecPayload, intent string) *AgentPolicy {
	goal := strings.TrimSpace(payload.Goal)
	if goal == "" {
		goal = strings.TrimSpace(intent)
	}
	instructions := strings.TrimSpace(payload.Instructions)
	completion := strings.TrimSpace(payload.CompletionCriteria)
	if completion == "" && (goal != "" || instructions != "") {
		completion = completionGeneralDefault
	}
	if goal == "" && instructions == "" && completion == "" {
		return nil
	}
	return &AgentPolicy{Contract: &AgentContract{
		Goal:               goal,
		Instructions:       instructions,
		CompletionCriteria: completion,
	}}
}
