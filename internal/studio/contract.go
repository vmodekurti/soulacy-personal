package studio

import (
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
)

// ContractOption tunes AssessContract without breaking backward callers. Story
// 2b (Cohort C): security- and persona-scoped checks need the fuller
// agent.Definition (Draft doesn't carry Policy / NonNegotiables / Builtins);
// callers that have the Definition on hand pass it via WithAgentDefinition so
// the enriched checks run.
type ContractOption func(*contractOpts)

type contractOpts struct {
	def *agent.Definition
}

// WithAgentDefinition supplies the source agent.Definition so contract checks
// that depend on fields not present on Draft (security, persona, builtins)
// can run. Callers that don't have a Definition (e.g. the pure Studio build
// loop) leave this off — those checks then quietly skip.
func WithAgentDefinition(def *agent.Definition) ContractOption {
	return func(o *contractOpts) { o.def = def }
}

// ContractResult is Studio's platform-wide generation contract. It consolidates
// graph compile checks, runtime preflight checks, and authoring-rule hygiene into
// one deterministic report so every generated workflow can be judged the same
// way before save, build, or repair.
type ContractResult struct {
	OK       bool            `json:"ok"`
	Score    int             `json:"score"`
	Blockers int             `json:"blockers"`
	Warnings int             `json:"warnings"`
	Checks   []ContractCheck `json:"checks"`
	Summary  string          `json:"summary"`
}

// ContractCheck is one rule verdict. Status is "pass", "warn", or "block".
type ContractCheck struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	NodeID  string `json:"nodeId,omitempty"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
	// Action and ActionLabel come from the shared vocabulary in fixactions.go.
	// A check that leaves them empty still gets an action derived from its id
	// (see actionForContractCheck) — this is for checks that know something the
	// id cannot express, in particular the ones Studio can fix outright.
	Action       string            `json:"action,omitempty"`
	ActionLabel  string            `json:"actionLabel,omitempty"`
	ActionParams map[string]string `json:"actionParams,omitempty"`
}

// contractAdd records one check. contractAddFix does the same for a check that
// can also hand the user a button — a separate signature so the many existing
// add() calls keep their shape.
type contractAdd func(id, title, status, node, msg, fix string)
type contractAddFix func(id, title, status, node, msg, fix, action, label string, params map[string]string)

// AssessContract runs the Studio generation contract over a draft. It is pure
// and LLM-free; callers provide the same live-state input used by Preflight.
// Options (see ContractOption) supply extra context — currently the source
// agent.Definition for security/persona/builtin checks (Story 2b).
func AssessContract(draft Draft, cat Catalog, in PreflightInput, options ...ContractOption) ContractResult {
	opts := contractOpts{}
	for _, opt := range options {
		if opt != nil {
			opt(&opts)
		}
	}
	if in.Catalog.Tools == nil && in.Catalog.MCP == nil && in.Catalog.Agents == nil {
		in.Catalog = cat
	}
	var res ContractResult
	add := func(id, title, status, node, msg, fix string) {
		res.Checks = append(res.Checks, ContractCheck{
			ID: id, Title: title, Status: status, NodeID: node, Message: msg, Fix: fix,
		})
		switch status {
		case "block":
			res.Blockers++
		case "warn":
			res.Warnings++
		}
	}
	pass := func(id, title, msg string) { add(id, title, "pass", "", msg, "") }
	// addFix is `add` for a check that can hand the user a button as well as a
	// sentence. Kept separate so the sixty existing add() calls stay readable.
	addFix := func(id, title, status, node, msg, fix, action, label string, params map[string]string) {
		res.Checks = append(res.Checks, ContractCheck{
			ID: id, Title: title, Status: status, NodeID: node, Message: msg, Fix: fix,
			// Resolve through the shared vocabulary rather than storing whatever
			// was passed. A caller that has no special wording for its button
			// should get the vocabulary's default, not an empty string — which
			// renders as a button with nothing written on it. readiness.go's
			// finishItem already worked this way; this was the one path that did
			// not, so a check gained a silent blank button simply by not
			// repeating a label the vocabulary already knows.
			Action: action, ActionLabel: resolveFixLabel(action, label), ActionParams: params,
		})
		switch status {
		case "block":
			res.Blockers++
		case "warn":
			res.Warnings++
		}
	}

	if draft.IsAgent() {
		pass("agent.shape", "Reasoning-agent shape", "Reasoning-agent draft has no fixed graph to compile; contract checks against system prompt, tool allowlists, peer graph, and step budget run below.")
	} else {
		vr := Validate(draft)
		if vr.Ok {
			pass("graph.integrity", "Graph integrity", "The workflow graph compiles: node ids, entry, edges, ports, and output contracts are coherent.")
		} else {
			for _, e := range vr.Errors {
				if e.Source == ValidateSourceCompletion {
					// assessCompletionContractRules reports this one with a
					// remedy that actually matches it. Reporting it here too
					// gave the user the same sentence twice, once under a
					// heading about graph structure that was not the problem.
					continue
				}
				add("graph.integrity", "Graph integrity", "block", e.NodeID, e.Message, "Fix the broken graph structure before saving or running.")
			}
		}
		for _, w := range vr.Warnings {
			add("graph.warning", "Graph warning", "warn", w.NodeID, w.Message, "Review this warning; generated workflows should not rely on ambiguous graph shape.")
		}
	}

	pf := Preflight(draft, in)
	if pf.OK {
		pass("runtime.preflight", "Runtime readiness", "All required runtime setup, tool arguments, templates, data-flow references, and delivery channels passed preflight.")
	} else {
		for _, b := range pf.Blockers {
			add("runtime."+nonEmpty(b.Kind, "preflight"), "Runtime readiness", "block", b.NodeID, b.Message, b.Fix)
		}
	}
	for _, w := range pf.Warnings {
		add("runtime."+nonEmpty(w.Kind, "warning"), "Runtime warning", "warn", w.NodeID, w.Message, w.Fix)
	}

	assessNameCollision(draft, cat, addFix, pass)
	assessInboundInputUse(draft, add, pass)
	assessAuthoringRules(draft, opts, add, addFix, pass)
	res.OK = res.Blockers == 0
	res.Score = contractScore(res.Blockers, res.Warnings)
	res.Summary = contractSummary(res)
	return res
}

// assessInboundInputUse catches a workflow that is STARTED by a person sending
// something and then never reads what they sent.
//
// Such a graph runs, passes every structural check, and answers the same thing
// on every run regardless of the question — the build spec, frozen in at compile
// time. Asked "how is the weather in Buffalo Grove for the next 7 days", one
// replied with a research digest about how to build a weather bot, because that
// was the intent it had been compiled from and the user's message reached no
// node.
//
// Deliberately a WARNING, not a blocker. An interactive workflow that ignores
// its input is sometimes exactly right — "when someone messages me, post today's
// status" takes no argument — so refusing to save would block a legitimate
// design. But it is never right by ACCIDENT, and it is invisible until someone
// reads the node inputs, which is precisely when a warning earns its place.
//
// Agents are exempt: a reasoning agent receives the message through its loop
// rather than through a template reference, so absence proves nothing.
func assessInboundInputUse(draft Draft, add func(id, title, status, node, msg, fix string), pass func(id, title, msg string)) {
	if draft.IsAgent() || len(draft.Flow.Nodes) == 0 {
		return
	}
	// Channel and webhook only — NOT manual.
	//
	// A manual run is usually someone pressing Run to make the thing happen, with
	// nothing attached; warning there would fire on every perfectly good
	// one-button workflow. A channel message or an inbound webhook, by contrast,
	// ALWAYS carries a payload, so a graph that reads none of it is discarding
	// the only thing that distinguishes one run from another.
	switch strings.ToLower(strings.TrimSpace(draft.Trigger.Type)) {
	case "channel", "webhook":
	default:
		return
	}
	// Any reference to the inbound payload counts, under any of its aliases.
	refs := []string{".trigger.text", ".trigger.message", ".trigger.input", ".trigger.", ".input"}
	for _, n := range draft.Flow.Nodes {
		hay := n.Input + " " + n.Code + " " + n.Intent
		for _, r := range refs {
			if strings.Contains(hay, r) {
				pass("input.inbound", "Uses the incoming message",
					"This workflow reads what the person sent, so each run answers the request it was given.")
				return
			}
		}
	}
	add("input.inbound", "Uses the incoming message", "warn", draft.Flow.Entry,
		"This workflow is triggered by an incoming message but no step reads it, so every run will "+
			"produce the same result regardless of what the person asks.",
		"Reference the inbound text in the first step — for example {{ .trigger.text }} in the search "+
			"query or prompt — or switch the trigger to a schedule if the same result every time is intended.")
}

func assessAuthoringRules(draft Draft, opts contractOpts, add contractAdd, addFix contractAddFix, pass func(id, title, msg string)) {
	if draft.IsAgent() {
		pass("architecture.fit", "Architecture fit", "This draft is a reasoning agent, so Studio will not force it into a brittle fixed workflow graph.")
		assessReasoningAgentRules(draft, opts, add, pass)
		assessCompletionContractRules(draft, add, addFix, pass)
		return
	}

	nodeCount := len(draft.Flow.Nodes)
	switch {
	case nodeCount == 0:
		add("architecture.empty", "Architecture fit", "block", "", "This workflow has no runnable steps.", "Add at least one tool, Python, LLM, or agent step.")
	case nodeCount <= 5:
		pass("architecture.size", "Macro-workflow size", fmt.Sprintf("The workflow has %d node(s), which fits the simple high-level Macro-Workflow guideline.", nodeCount))
	case nodeCount <= 8:
		add("architecture.size", "Macro-workflow size", "warn", "", fmt.Sprintf("This workflow has %d nodes. Studio workflows should usually stay at 3-5 high-level steps.", nodeCount), "Steps that only reshape data can usually be one step: merge them into a single Custom Python block from the palette on the left. If the agent needs to choose tools as it goes, switch Mode to Auto at the top of the Build step.")
	case knownDeterministicMacroWorkflow(draft):
		add("architecture.size", "Macro-workflow size", "warn", "", fmt.Sprintf("This deterministic macro-workflow has %d high-level service steps. It is larger than the ideal visual graph, but it matches a known Soulacy pattern with explicit tool order and completion checks.", nodeCount), "Keep this as a workflow only when the ordering must be deterministic; otherwise convert it to an Auto/Plan-Execute agent.")
	default:
		add("architecture.size", "Macro-workflow size", "block", "", fmt.Sprintf("This workflow has %d nodes and is likely too brittle for a visual Macro-Workflow.", nodeCount), "Merge the small steps into fewer, larger ones on the canvas. If the work genuinely needs the agent to decide its own steps as it goes, switch Mode to Auto at the top of the Build step instead of drawing them all out.")
	}

	if risky := freeformHandoffWarnings(draft); len(risky) == 0 {
		pass("data.contracts", "Data contracts", "No obvious free-form agent/LLM output is wired directly into a structured tool call.")
	} else {
		for _, r := range risky {
			add("data.contracts", "Data contracts", "warn", r.nodeID, r.message, "An agent step produces prose, but the tool after it expects named fields — so the tool call arrives malformed or half-empty. Put a step in between that turns the text into fields: drag LLM Extract, or a Custom Python block, from the palette on the left. Alternatively wire that tool's input from an earlier step's named output instead of from the agent's text.")
		}
	}

	if bad := thinNewAgents(draft); len(bad) == 0 {
		pass("agents.prompts", "Helper-agent prompts", "Helper agents are either absent or have enough prompt detail to run independently.")
	} else {
		for _, a := range bad {
			// This is the one warning a user genuinely cannot act on without
			// knowing what a good agent prompt looks like — so hand them one.
			// The synthesized persona travels WITH the finding rather than
			// behind another round trip, so the button applies instantly.
			synth := SynthesizeAgent(a, agentNodeFor(draft, a), draft.Name)
			addFix("agents.prompts", "Helper-agent prompts", "warn", a,
				"Helper agent \""+a+"\" has a very short or missing system prompt, so it will improvise its role on every run.",
				"A helper agent's prompt is its whole character — it gets no other context. Studio can write a starter one from what this step does: role, how to behave, the output format expected of it, and what to do with empty input. Edit it afterwards; it is a floor, not a ceiling.",
				FixWriteHelperPrompt, "Write a starter prompt",
				map[string]string{"agent": a, "prompt": synth.SystemPrompt})
		}
	}
	assessCompletionContractRules(draft, add, addFix, pass)
}

func knownDeterministicMacroWorkflow(draft Draft) bool {
	if draft.IsAgent() || len(draft.Flow.Nodes) == 0 {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(draft.Intent + " " + draft.Name))
	if deterministicNotebookPodcastWorkflow(text) || knowledgeIngestionWorkflow(text) {
		return true
	}
	notebookOps := 0
	channelOps := 0
	for _, n := range draft.Flow.Nodes {
		tool := strings.ToLower(strings.TrimSpace(n.Tool))
		switch {
		case strings.Contains(tool, "notebooklm"):
			notebookOps++
		case tool == "channel.send":
			channelOps++
		}
	}
	return notebookOps >= 3 && channelOps <= 1
}

func assessCompletionContractRules(draft Draft, add contractAdd, addFix contractAddFix, pass func(id, title, msg string)) {
	errs, warns := completionContractValidateIssues(draft)
	if len(errs) == 0 && len(warns) == 0 {
		if requiresCompletionContract(draft) {
			pass("completion.contract", "Completion contract", "The draft has an explicit done-condition contract for multi-step work.")
		}
		return
	}
	for _, e := range errs {
		if strings.Contains(e.Message, "no routable output channel") {
			// The generic remedy ("add the missing operation(s), set a real
			// output route…") reads like three unrelated suggestions. This one
			// has a single cause and a single cure.
			addFix("completion.contract", "Completion contract", "block", e.NodeID, e.Message,
				"This agent is meant to deliver something, but its only channel is HTTP — that is how requests come IN, not where results go OUT. Tick a real destination (Telegram, Slack, email…) under Channels & delivery in the Build step's inspector. If none are listed, set one up on the Delivery page first; it appears here once configured.",
				FixOpenStudio, "Choose a destination", nil)
			continue
		}
		add("completion.contract", "Completion contract", "block", e.NodeID, e.Message, "The workflow stops before doing everything the description asked for. Add the missing step on the canvas — or, if the job needs the agent to work out its own steps, switch Mode to Auto at the top of the Build step.")
	}
	for _, w := range warns {
		add("completion.contract", "Completion contract", "warn", w.NodeID, w.Message, "Say what a finished run looks like for this agent, and give it somewhere to put the result — pick a destination under Channels & delivery in the Build step's inspector.")
	}
}

type handoffWarning struct {
	nodeID  string
	message string
}

func freeformHandoffWarnings(draft Draft) []handoffWarning {
	byID := map[string]struct {
		kind, tool, input string
	}{}
	for _, n := range draft.Flow.Nodes {
		byID[n.ID] = struct {
			kind, tool, input string
		}{kind: strings.ToLower(strings.TrimSpace(n.Kind)), tool: strings.TrimSpace(n.Tool), input: strings.TrimSpace(n.Input)}
	}
	var out []handoffWarning
	for _, e := range draft.Flow.Edges {
		src, ok1 := byID[e.From]
		dst, ok2 := byID[e.To]
		if !ok1 || !ok2 {
			continue
		}
		if dst.kind != "tool" || strings.TrimSpace(e.ToPort) != "" {
			continue
		}
		if src.kind != "agent" && src.kind != "llm" {
			continue
		}
		if strings.Contains(dst.input, "toJson") || strings.Contains(dst.input, "{{") && strings.HasPrefix(dst.input, "{") {
			continue
		}
		out = append(out, handoffWarning{
			nodeID:  e.To,
			message: "Step \"" + e.To + "\" receives free-form " + src.kind + " output before calling structured tool \"" + dst.tool + "\".",
		})
	}
	return out
}

func thinNewAgents(draft Draft) []string {
	var out []string
	for _, a := range draft.NewAgents {
		body := strings.TrimSpace(a.SystemPrompt)
		if len(strings.Fields(body)) < 18 {
			id := strings.TrimSpace(a.ID)
			if id == "" {
				id = strings.TrimSpace(a.Name)
			}
			if id == "" {
				id = "unnamed"
			}
			out = append(out, id)
		}
	}
	return out
}

func contractScore(blockers, warnings int) int {
	score := 100 - blockers*25 - warnings*5
	if score < 0 {
		return 0
	}
	return score
}

func contractSummary(r ContractResult) string {
	if r.Blockers == 0 && r.Warnings == 0 {
		return "Studio contract passed cleanly."
	}
	if r.Blockers == 0 {
		return fmt.Sprintf("Studio contract passed with %d warning(s).", r.Warnings)
	}
	return fmt.Sprintf("Studio contract blocked by %d issue(s), with %d warning(s).", r.Blockers, r.Warnings)
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return strings.TrimSpace(s)
}

// assessReasoningAgentRules runs the reasoning-agent-specific contract checks
// that used to be skipped entirely (contract.go treated `Draft.IsAgent()` as a
// blanket pass). Story 2 (Cohort B): the contract is now platform-wide, so
// react / plan_execute / auto agents get validated on the surfaces that make
// their runtime succeed — system prompt richness, tool allowlist sanity, peer
// graph coherence, step-budget realism, prompt hygiene, and channel/delivery
// wiring. Preflight still enforces the runtime blockers (missing tool, MCP
// disconnected, etc.); this function promotes the shape-level concerns into
// the scored contract view so operators see them before Save + Build.
//
// Checks in this first slice (Story 2 MVP):
//   - agent.system_prompt   — block on empty, warn on <40 words
//   - agent.tool_allowlist  — warn/block when a react/plan_execute agent has nothing to act on
//   - agent.peer_graph      — dangling agent__<id> references, thin peer prompts
//   - agent.prompt_hygiene  — SystemPrompt cites tools/skills/peers not in the allowlist
//   - agent.step_budget     — MaxTurns/StepTimeout/TotalTimeout realism
//   - agent.channel_delivery — channel.send in tools but no Channels configured
//
// The llm_fit / capability_scope / persona_consistency / builtin_scope checks
// from the design memo are deferred to a follow-up so this first slice ships
// without importing internal/agentvalidate primitives.
func assessReasoningAgentRules(draft Draft, opts contractOpts, add func(id, title, status, node, msg, fix string), pass func(id, title, msg string)) {
	prompt := strings.TrimSpace(draft.SystemPrompt)
	promptWords := len(strings.Fields(prompt))
	strategy := strings.ToLower(strings.TrimSpace(draft.Strategy))

	// 1. system_prompt — for a reasoning agent the prompt IS the agent spec.
	switch {
	case prompt == "":
		add("agent.system_prompt", "System prompt", "block", "",
			"This reasoning agent has an empty system prompt, so there is nothing to guide it at runtime.",
			"Write a system prompt covering the agent's role, allowed tools, output format, and any constraints.")
	case promptWords < 40:
		add("agent.system_prompt", "System prompt", "warn", "",
			fmt.Sprintf("System prompt is only %d word(s). Reasoning agents need a richer spec because the prompt replaces the graph.", promptWords),
			"Extend the prompt to cover role, allowed tools, output constraints, and refusal/safety rules; aim for 40+ words.")
	default:
		pass("agent.system_prompt", "System prompt", fmt.Sprintf("System prompt is %d words — enough for a reasoning agent to act on.", promptWords))
	}

	// 2. tool_allowlist — nothing to act on = a loop with no exit.
	hasTools := len(draft.Tools) > 0
	hasPeers := len(draft.NewAgents) > 0
	hasSkills := len(draft.Skills) > 0
	hasKB := len(draft.Knowledge) > 0
	switch {
	case !hasTools && !hasPeers && !hasSkills && !hasKB && strategy == "react":
		add("agent.tool_allowlist", "Tool allowlist", "block", "",
			"A ReAct agent has no tools, peer agents, skills, or knowledge bases to act on — the loop cannot make progress.",
			"An agent with no tools, helpers, skills or knowledge can only talk. Add at least one from the palette on the left, or attach a Skill or Knowledge base in the Build step's inspector.")
	case !hasTools && !hasPeers && !hasSkills && !hasKB:
		add("agent.tool_allowlist", "Tool allowlist", "warn", "",
			"This agent has no tools, peers, skills, or knowledge bases; runtime behaviour is limited to plain-LLM responses.",
			"Wire the agent to at least one tool or peer if it needs to take action.")
	default:
		pass("agent.tool_allowlist", "Tool allowlist", "The agent has at least one tool, peer, skill, or knowledge base to act on.")
	}

	// 3. peer_graph — extend thin-prompt to reasoning-agent peers, and flag
	// dangling agent__<id> references from the system prompt.
	if thin := thinNewAgents(draft); len(thin) > 0 {
		for _, id := range thin {
			add("agent.peer_graph", "Peer agents", "warn", id,
				"Peer agent \""+id+"\" has a very short or missing system prompt.",
				"Give each peer a self-contained role, tools it may use, and expected output.")
		}
	} else if hasPeers {
		pass("agent.peer_graph", "Peer agents", "Every peer agent has enough prompt detail to run independently.")
	}
	dangling := danglingPeerRefs(draft)
	for _, ref := range dangling {
		add("agent.peer_graph", "Peer agents", "warn", "",
			"System prompt references peer \""+ref+"\" but no such agent is declared in NewAgents.",
			"The prompt refers to a helper agent this workflow does not have. Either add an Agent step for it on the canvas, correct the spelling to match one that exists, or take the mention out of the prompt.")
	}

	// 4. prompt_hygiene — flag references to tools that aren't in the allowlist.
	if len(prompt) > 0 && hasTools {
		toolSet := map[string]bool{}
		for _, t := range draft.Tools {
			toolSet[strings.ToLower(strings.TrimSpace(t))] = true
		}
		promptLower := strings.ToLower(prompt)
		// Look at obvious `tool` / "use X" citations; conservative match — only
		// flag when the tool name appears near an action verb so casual mentions
		// don't fire.
		for _, verb := range []string{"use `", "call `", "invoke `"} {
			idx := 0
			for {
				pos := strings.Index(promptLower[idx:], verb)
				if pos < 0 {
					break
				}
				start := idx + pos + len(verb)
				end := strings.Index(promptLower[start:], "`")
				if end < 0 {
					break
				}
				name := promptLower[start : start+end]
				idx = start + end + 1
				if name == "" || toolSet[name] {
					continue
				}
				// Skip citations that map to a known peer, skill, or KB.
				if hasPeers {
					peerHit := false
					for _, p := range draft.NewAgents {
						if strings.EqualFold(strings.TrimSpace(p.ID), name) || strings.EqualFold(strings.TrimSpace(p.Name), name) {
							peerHit = true
							break
						}
					}
					if peerHit {
						continue
					}
				}
				add("agent.prompt_hygiene", "Prompt hygiene", "warn", "",
					"System prompt tells the agent to use tool \""+name+"\" but it is not in the allowlist.",
					"Add \""+name+"\" to Tools, or reword the prompt to reference an allowed tool.")
			}
		}
	}

	// 5. step_budget — realism on the reasoning loop caps.
	stepTimeoutDur := parseContractDuration(draft.StepTimeout)
	totalTimeoutDur := parseContractDuration(draft.TotalTimeout)
	runTimeoutDur := parseContractDuration(draft.RunTimeout)
	if strings.TrimSpace(draft.StepTimeout) != "" && stepTimeoutDur <= 0 {
		add("agent.step_budget", "Step budget", "warn", "",
			"step_timeout \""+draft.StepTimeout+"\" could not be parsed as a duration.",
			"Use Go duration syntax (e.g. \"30s\", \"2m\", \"1h30m\").")
	}
	if strings.TrimSpace(draft.TotalTimeout) != "" && totalTimeoutDur <= 0 {
		add("agent.step_budget", "Step budget", "warn", "",
			"total_timeout \""+draft.TotalTimeout+"\" could not be parsed as a duration.",
			"Use Go duration syntax (e.g. \"5m\", \"30m\", \"2h\").")
	}
	if strings.TrimSpace(draft.RunTimeout) != "" && runTimeoutDur <= 0 {
		add("agent.step_budget", "Step budget", "warn", "",
			"run_timeout \""+draft.RunTimeout+"\" could not be parsed as a duration.",
			"Use Go duration syntax (e.g. \"10m\", \"1h\").")
	}
	if strategy == "react" && draft.MaxTurns > 40 {
		add("agent.step_budget", "Step budget", "block", "",
			fmt.Sprintf("max_turns is %d — a ReAct loop this deep is nearly guaranteed to blow the token budget or spin.", draft.MaxTurns),
			"Set `max_turns` to 20 or fewer in the SOUL.yaml view (the `</>` tab above the canvas). It caps how many thinking steps a run may take; 15 is a good default. A higher number mostly lets a stuck agent keep spending.")
	}
	if strategy == "react" && draft.MaxTurns == 0 && totalTimeoutDur <= 0 && runTimeoutDur <= 0 {
		add("agent.step_budget", "Step budget", "warn", "",
			"ReAct loop has no max_turns and no total_timeout / run_timeout — a stuck loop cannot self-terminate.",
			"Nothing currently stops this agent if it gets stuck in a loop. In the SOUL.yaml view (the `</>` tab above the canvas) set at least one limit: `max_turns` (how many thinking steps), `total_timeout`, or `run_timeout`.")
	}
	if stepTimeoutDur > 0 && totalTimeoutDur > 0 && draft.MaxTurns > 0 {
		if totalTimeoutDur < time.Duration(draft.MaxTurns)*stepTimeoutDur {
			add("agent.step_budget", "Step budget", "warn", "",
				fmt.Sprintf("total_timeout (%s) is less than max_turns × step_timeout (%d × %s); the agent cannot complete a full loop.",
					totalTimeoutDur, draft.MaxTurns, stepTimeoutDur),
				"The time allowed per step, multiplied by the number of steps, is more than the overall limit — so a slow run gets cut off part-way through. In the SOUL.yaml view (the `</>` tab above the canvas) raise `total_timeout`, or lower `max_turns` or `step_timeout` until they add up.")
		}
	}

	// 6. channel_delivery — channel.send used but Channels not declared.
	if hasTools {
		usesChannelSend := false
		for _, t := range draft.Tools {
			if strings.EqualFold(strings.TrimSpace(t), "channel.send") {
				usesChannelSend = true
				break
			}
		}
		if usesChannelSend && len(draft.Channels) == 0 {
			add("agent.channel_delivery", "Channel delivery", "warn", "",
				"Agent has channel.send in its tool allowlist but declares no Channels — it will guess a route at runtime.",
				"Add the target channel id(s) to Channels so channel.send has an explicit default.")
		}
	}

	// 7. llm_fit — surface model choices that are known to trip a reasoning loop.
	// Runs on Draft alone; opts.def not required.
	assessAgentLLMFit(draft, add)

	// 8-10. Security / persona / builtin scope — need the fuller Definition
	// (Draft doesn't round-trip Policy / NonNegotiables / Builtins). When
	// callers passed WithAgentDefinition, these run; otherwise they skip.
	if opts.def != nil {
		assessAgentCapabilityScope(draft, opts.def, add)
		assessAgentPersonaConsistency(opts.def, add)
		assessAgentBuiltinScope(draft, opts.def, add)
	}
}

// assessAgentLLMFit turns the "wrong model for the job" heuristics from
// internal/agentvalidate into contract-scored checks. Model-only, no Definition
// needed.
func assessAgentLLMFit(draft Draft, add func(id, title, status, node, msg, fix string)) {
	provider := strings.ToLower(strings.TrimSpace(draft.LLM.Provider))
	model := strings.TrimSpace(draft.LLM.Model)
	strategy := strings.ToLower(strings.TrimSpace(draft.Strategy))
	if model == "" {
		return
	}
	if isEmbeddingModel(model) {
		add("agent.llm_fit", "Model fit", "block", "",
			"\""+model+"\" is an embedding model — it cannot drive a reasoning loop.",
			"Pick a chat/instruct model in the provider dropdown. Suggestions: "+strings.Join(reasoningModelSuggestions(provider), ", ")+".")
		return
	}
	if weakJSONModel(provider, model) {
		add("agent.llm_fit", "Model fit", "warn", "",
			"\""+model+"\" is known to produce unreliable JSON tool calls in a reasoning loop.",
			"Consider a stronger model. Suggestions: "+strings.Join(reasoningModelSuggestions(provider), ", ")+".")
	}
	if smallContextModel(provider, model) {
		steps := draft.MaxTurns
		if steps <= 4 {
			// heuristic ceiling — the default agent max_steps sits around 4-8
			// depending on strategy. Threshold intentionally matches
			// agentvalidate.assessReasoningLLM.
		}
		if draft.MaxTurns > 4 || (strategy == "plan_execute" && draft.MaxTurns == 0) {
			add("agent.llm_fit", "Model fit", "warn", "",
				"\""+model+"\" has a small context window; a reasoning loop with >4 turns tends to overflow it.",
				"Every step adds to the conversation, and this model cannot hold more than about four before it runs out of room. Either lower `max_turns` to 4 in the SOUL.yaml view (the `</>` tab), or use the \"Runs on\" picker in the toolbar to choose a model with a larger context window.")
		}
	}
	if provider == "groq" && draft.MaxTurns > 4 {
		add("agent.llm_fit", "Model fit", "warn", "",
			fmt.Sprintf("Groq's free tier throttles TPM aggressively; %d turns per run risks 429s mid-loop.", draft.MaxTurns),
			"Groq's free tier rate-limits runs this long, so it will fail partway. Either lower `max_turns` to 4 in the SOUL.yaml view (the `</>` tab), or use the \"Runs on\" picker in the toolbar to move this agent to another provider.")
	}
	if len(draft.LLM.AllowedProviders) > 0 {
		allowed := false
		for _, ap := range draft.LLM.AllowedProviders {
			if strings.EqualFold(strings.TrimSpace(ap), provider) {
				allowed = true
				break
			}
		}
		if !allowed && provider != "" {
			add("agent.llm_fit", "Model fit", "block", "",
				"Provider \""+provider+"\" is not in the agent's allowed_providers list ("+strings.Join(draft.LLM.AllowedProviders, ", ")+").",
				"Either add \""+provider+"\" to allowed_providers, or switch the agent's provider dropdown to one that is already allowed.")
		}
	}
}

// assessAgentCapabilityScope warns when a privileged / shell-capable agent
// runs unattended on a cron trigger (no human approver on failure) or when
// the tool policy is wide open with no allow-list narrowing.
func assessAgentCapabilityScope(draft Draft, def *agent.Definition, add func(id, title, status, node, msg, fix string)) {
	privileged := def.SystemTools || def.AllowShell || def.HasCapability("system")
	trigger := strings.ToLower(strings.TrimSpace(string(def.Trigger)))
	isScheduled := trigger == "cron"
	if def.Schedule != nil && strings.TrimSpace(def.Schedule.Cron) != "" {
		isScheduled = true
	}
	if privileged && isScheduled && !def.Unattended {
		add("agent.capability_scope", "Capability scope", "warn", "",
			"This agent has system-level capabilities and runs on a cron schedule, but is not marked Unattended — scheduled fires will silently stall waiting for a human approval that no one is there to give.",
			"A scheduled run has nobody to answer a confirmation prompt, so it will stop and fail. Either remove the privileged tools (the safer option), or tick Unattended in the Build step's inspector to auto-approve them — check which tools that covers under `confirm_tools:` in the SOUL.yaml view (the `</>` tab) before you do.")
	}
	pol := def.Policy
	polAllowShell := strings.EqualFold(strings.TrimSpace(pol.Shell), "allow")
	polAllowNetwork := strings.EqualFold(strings.TrimSpace(pol.Network), "allow")
	if polAllowShell && len(pol.DenyPaths) == 0 && !def.Unattended {
		add("agent.capability_scope", "Capability scope", "warn", "",
			"policy.shell = allow with no deny_paths — every shell tool call is unfiltered.",
			"As written, this agent can run any shell command without asking. In the SOUL.yaml view (the `</>` tab above the canvas) set `policy.shell: prompt` so it asks first, or list the paths it must never touch under `deny_paths:`.")
	}
	if polAllowNetwork && len(pol.AllowDomains) == 0 {
		add("agent.capability_scope", "Capability scope", "warn", "",
			"policy.network = allow with no allow_domains — the agent can reach any host on the internet.",
			"Set policy.network: prompt, or add an allow_domains list of the specific hosts the agent needs.")
	}
}

// assessAgentPersonaConsistency looks for contradictions between the operator's
// stated Non-Negotiables and the runtime configuration that would prevent them
// from being honoured. NonNegotiables lives at the top level of Definition
// (not under Persona) and is a nil-able pointer, as is OutputConstraints.
func assessAgentPersonaConsistency(def *agent.Definition, add func(id, title, status, node, msg, fix string)) {
	nn := def.NonNegotiables
	if nn == nil {
		return
	}
	if len(nn.MustNot) > 0 && strings.EqualFold(strings.TrimSpace(def.LLM.ToolChoice), "required") {
		add("agent.persona_consistency", "Persona consistency", "warn", "",
			"The agent has MustNot rules but LLM.tool_choice is \"required\" — the first turn is forced to call a tool even if a MustNot rule would refuse.",
			"Change tool_choice to \"auto\" so the agent can honour refusals, or drop the MustNot rules that conflict with mandatory tool use.")
	}
	if nn.OutputConstraints != nil {
		format := strings.ToLower(strings.TrimSpace(nn.OutputConstraints.Format))
		if format == "json" && strings.TrimSpace(def.LLM.ResponseFormat) == "" && len(def.LLM.OutputSchema) == 0 {
			add("agent.persona_consistency", "Persona consistency", "warn", "",
				"Non-Negotiables set output format to JSON, but LLM.response_format and output_schema are both empty — the constraint is aspirational, not enforced.",
				"The prompt asks for JSON but nothing makes the model produce it, so you get prose that only sometimes parses. In the SOUL.yaml view (the `</>` tab above the canvas) set `llm.response_format: json_object`, or describe the shape you want under `output_schema:`.")
		}
	}
}

// assessAgentBuiltinScope catches misconfigurations where the operator opted
// out of every tool surface or lists builtins that need setup they haven't
// done (kb_search without any Knowledge, read_skill without any Skills).
func assessAgentBuiltinScope(draft Draft, def *agent.Definition, add func(id, title, status, node, msg, fix string)) {
	// Total opt-out: Builtins explicitly empty and no other surface.
	mcpToolsLen := 0
	if def.MCPTools != nil {
		mcpToolsLen = len(*def.MCPTools)
	}
	if def.Builtins != nil && len(*def.Builtins) == 0 &&
		mcpToolsLen == 0 && len(def.Skills) == 0 && len(def.Agents) == 0 && len(def.Knowledge) == 0 {
		add("agent.builtin_scope", "Builtin scope", "block", "",
			"The agent explicitly opts out of every builtin and has no MCP tools, skills, peer agents, or knowledge bases — there is nothing for a reasoning loop to call.",
			"Give it something to work with: drag a tool or a Custom Python block from the palette on the left, attach a Skill or Knowledge base in the Build step's inspector, or add an Agent step that hands work to a helper.")
		return
	}
	// Setup mismatches: a builtin listed without its dependencies.
	activeBuiltins := map[string]bool{}
	if def.Builtins != nil {
		for _, b := range *def.Builtins {
			activeBuiltins[strings.ToLower(strings.TrimSpace(b))] = true
		}
	}
	// If the *Draft* lists these tools it counts too (Draft.Tools is the
	// authoring-time allowlist, def.Builtins the compiled form).
	for _, t := range draft.Tools {
		activeBuiltins[strings.ToLower(strings.TrimSpace(t))] = true
	}
	if activeBuiltins["kb_search"] && len(def.Knowledge) == 0 {
		add("agent.builtin_scope", "Builtin scope", "warn", "",
			"kb_search is in the tool allowlist but no Knowledge bases are attached — every call will return \"no knowledge configured\".",
			"This agent can search a knowledge base but has none attached, so every search comes back empty. Pick one under Knowledge in the Build step's inspector, or remove the `kb_search` tool from the step that uses it.")
	}
	if (activeBuiltins["read_skill"] || activeBuiltins["read_skill_file"]) && len(def.Skills) == 0 {
		add("agent.builtin_scope", "Builtin scope", "warn", "",
			"read_skill is in the tool allowlist but no Skills are attached — the agent has nothing to read.",
			"This agent can read a skill but has none attached, so there is nothing to read. Pick one under Skills in the Build step's inspector, or remove the `read_skill` tool from the step that uses it.")
	}
	if activeBuiltins["kb_write"] && len(def.Knowledge) == 0 {
		add("agent.builtin_scope", "Builtin scope", "warn", "",
			"kb_write is in the tool allowlist but no Knowledge bases are attached — writes will have nowhere to land.",
			"This agent can save into a knowledge base but has none attached, so the write has nowhere to go. Pick one under Knowledge in the Build step's inspector, or remove the `kb_write` tool from the step that uses it.")
	}
}

// danglingPeerRefs finds `agent__<id>` (or `agent:<id>`) mentions in the
// system prompt whose target is not declared in NewAgents. Case-insensitive,
// conservative — only flags explicit tool-name references, not casual peer
// mentions.
func danglingPeerRefs(draft Draft) []string {
	prompt := strings.ToLower(draft.SystemPrompt)
	if prompt == "" {
		return nil
	}
	known := map[string]bool{}
	for _, p := range draft.NewAgents {
		id := strings.ToLower(strings.TrimSpace(p.ID))
		if id != "" {
			known[id] = true
		}
	}
	var out []string
	// Scan for `agent__` prefix (the canonical tool-name form) — capture the
	// following identifier chars.
	for _, prefix := range []string{"agent__", "agent:"} {
		idx := 0
		for {
			pos := strings.Index(prompt[idx:], prefix)
			if pos < 0 {
				break
			}
			start := idx + pos + len(prefix)
			// Consume identifier chars: [a-z0-9_-].
			end := start
			for end < len(prompt) {
				c := prompt[end]
				if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
					break
				}
				end++
			}
			if end > start {
				name := prompt[start:end]
				if !known[name] {
					out = append(out, name)
				}
			}
			idx = end
		}
	}
	return dedupeStrings(out)
}

// parseContractDuration returns 0 on empty or unparseable input so callers can
// treat "invalid" as "unset" without an extra bool.
func parseContractDuration(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0
	}
	return d
}

// dedupeStrings is defined in buildloop.go and shared across the studio pkg.

// assessNameCollision catches a NEW draft whose name would take over an agent
// that already exists.
//
// ToAgentDefinition derives the id from Draft.ID when a saved agent was opened
// for editing, and from slug(Draft.Name) otherwise. The save path then does:
//
//	if existing := s.loader.Get(def.ID); existing != nil { … update in place }
//
// which is exactly right for a re-save and silent data loss for a new draft
// that happens to slug onto someone else's id. Nothing warned about it.
//
// Seen live: describing a scheduled stock briefing produced a draft the builder
// named "Stock Advisor" — the name of an agent already deployed on that
// install. Saving would have written over a working agent, with no prompt, no
// diff, and no mention on the Save step, which listed only tool-argument
// blockers.
//
// The empty Draft.ID is the whole signal, so this fires only for drafts that
// have never been saved. Re-saving an agent you opened is not a collision, and
// must not be reported as one.
//
// slug() here is the same function ToAgentDefinition uses — deliberately, not
// a second copy of the rule. A check that derived the id even slightly
// differently from the save path would fire on names that are fine and stay
// quiet on the ones that are not.
func assessNameCollision(draft Draft, cat Catalog, addFix func(id, title, status, node, msg, fix, action, label string, params map[string]string), pass func(id, title, msg string)) {
	if strings.TrimSpace(draft.ID) != "" {
		return // an existing agent opened for editing — saving over it is the point
	}
	id := slug(draft.Name)
	if id == "" {
		return // the empty-name case is ToAgentDefinition's error to raise
	}
	for _, existing := range cat.Agents {
		if !strings.EqualFold(strings.TrimSpace(existing), id) {
			continue
		}
		addFix("identity.collision", "Name collision", "block", "",
			"An agent called \""+draft.Name+"\" already exists, and saving this would replace it rather than add a new one.",
			"Give this workflow a different name in the Save step — the existing \""+id+"\" agent keeps running untouched. "+
				"If you did mean to change that agent, open it from Deployed and edit it there instead, so you can see what you are changing.",
			FixRenameAgent, "", map[string]string{"id": id, "name": draft.Name})
		return
	}
	pass("identity.collision", "Name collision", "This name does not belong to an agent you already have.")
}
