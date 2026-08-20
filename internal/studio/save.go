// save.go — Studio's "save" step (Story S1.x, Wave 2). It converts a draft
// workflow into a Soulacy agent Definition so the GUI's Save action can
// persist the result of compile→test as a real (but DISABLED) agent.
//
// The conversion lives here (not in the gateway) so it is unit-testable
// without an HTTP server. Persistence wiring (loader.Upsert into an agent
// dir) lives in the gateway handler; ToAgentDefinition only does the
// pure Draft -> agent.Definition mapping and always sets Enabled=false.
package studio

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/agentprompt"
	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// slugRE collapses any run of non-alphanumeric chars into a single dash so
// a human-readable name becomes a stable, filesystem-safe agent id.
var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// llmConfigFor derives the saved agent's LLM block from the draft. The draft's
// LLM is populated by FromAgentDefinition on load, so an existing agent's
// provider/model/etc. survive a Studio round-trip instead of being reset. For a
// freshly generated draft (no temperature set yet) it falls back to the historic
// Studio default of 0.7 so behaviour is unchanged for new agents.
func llmConfigFor(draft Draft) agent.LLMConfig {
	llm := draft.LLM
	if llm.Temperature == 0 {
		llm.Temperature = 0.7
	}
	return llm
}

// StudioPrivilegeAckLabel is the agent-definition label key under which
// Studio records that the USER acknowledged, at save time, that this
// workflow is Privileged-tier and bound to a (non-web) channel.
//
// This is an informational acknowledgment record ONLY — it is deliberately
// NOT the operator's binding consent. By design (see internal/app/channels.go
// bindingDecision), the authoritative `accept_privileged_exposure` flag MUST
// live on the config.yaml channel binding, because the operator deploying an
// agent to a public channel is the one accepting the risk, not the agent
// author. Studio saves the agent DISABLED; the operator still grants channel
// exposure at deploy time. We use a distinct key so this record can never be
// mistaken for — or silently promoted into — the binding flag.
const StudioPrivilegeAckLabel = "studio.privilege_acknowledged"

// ToAgentDefinition converts a Studio Draft into a disabled agent.Definition.
// It carries the workflow's name, trigger, channels, and graph into the
// agent's fields, mapping Draft.Flow onto the agent's WorkflowSpec graph
// form (nodes/edges/entry, per pkg/agent/workflow.go). Enabled is always
// false — Studio saves are staged, not live.
//
// acceptPrivilegedExposure records the USER's save-time acknowledgment: when
// true and the draft binds at least one channel, it is stamped on the agent's
// Labels under StudioPrivilegeAckLabel as an informational record. It does NOT
// grant the channel binding — the operator must still set
// accept_privileged_exposure on the config.yaml binding at deploy time
// (internal/app/channels.go). The agent is saved disabled regardless.
// DraftAgentID is the agent id a draft will be saved under.
//
// Exported so a caller can find out WHICH agent a save is about before doing
// the expensive work of compiling the draft into one. The gateway needs that
// to check a concurrency precondition first: refusing a stale save only helps
// if the refusal comes before the contract and consent gates, because a caller
// told 422 goes away, fixes their graph, retries — and the retry succeeds,
// silently discarding the edit the 409 existed to protect.
//
// It calls the same derivation ToAgentDefinition does rather than repeating
// it. A second copy of this rule would be checked against one agent and write
// another the day somebody changes how names are slugged.
func DraftAgentID(draft Draft) string {
	id := strings.TrimSpace(draft.ID)
	if id == "" {
		id = slug(draft.Name)
	}
	return id
}

func ToAgentDefinition(draft Draft, acceptPrivilegedExposure bool) (agent.Definition, error) {
	// Prefer the existing agent id (set when an saved agent was opened for
	// editing) so a re-save UPDATES that agent rather than creating a new one
	// under a freshly slugged name. Fall back to the name slug for new drafts.
	id := DraftAgentID(draft)
	if id == "" {
		return agent.Definition{}, fmt.Errorf("studio: cannot derive an agent id from an empty workflow name")
	}

	// Canvas is authoritative (Phase A): if the user wired trigger/exit blocks,
	// project them onto Trigger/Channels/Entry before the definition is built.
	DeriveEndpoints(&draft)

	// Every executable node needs an output var or its result is dropped and
	// downstream wires read null. Guarantee it even for hand-edited canvases.
	ensureOutputVars(&draft.Flow)

	// ReAct / Plan-Execute agent: NOT a fixed workflow. The engine runs the
	// reasoning loop over an allowlist of tools/skills/peers — there is NO
	// workflow block (one would override reasoning.strategy). Map the draft's
	// agent form onto a strategy-based Definition.
	if draft.IsAgent() {
		return toReActAgentDefinition(draft, id, acceptPrivilegedExposure)
	}

	// Normalize python node code: unwrap any {"code":"..."} JSON envelope so the
	// saved agent stores runnable Python, not a JSON string (which would fail at
	// run time with "name 'run' is not defined"). Then classify capabilities.
	for i := range draft.Flow.Nodes {
		unwrapNodeCode(&draft.Flow.Nodes[i])
	}
	classifyFlowNodes(&draft.Flow)

	def := agent.Definition{
		ID:                 id,
		Name:               draft.Name,
		Description:        describeWorkflowShort(draft),
		Trigger:            mapTrigger(draft.Trigger.Type),
		Channels:           append([]string(nil), draft.Channels...),
		SystemPrompt:       buildSystemPrompt(draft),
		StudioIntent:       strings.TrimSpace(draft.Intent),
		StudioRefined:      draft.Refined,
		StudioRawIntent:    strings.TrimSpace(draft.RawIntent),
		StudioDeliveryMode: normalizeStudioDeliveryMode(draft.DeliveryMode),
		StudioTriggerMode:  normalizeStudioTriggerMode(draft.Trigger.Type),
		// Disabled by construction: a Studio save stages an agent for the
		// operator to review and enable.
		Enabled:      false,
		MaxTurns:     maxTurnsOr(draft.MaxTurns, 15),
		Memory:       memoryOr(draft.Memory),
		LLM:          llmConfigFor(draft),
		RunTimeout:   strings.TrimSpace(draft.RunTimeout),
		ConfirmTools: dedupeNonEmpty(draft.ConfirmTools),
		Security:     cloneSecurityConfig(draft.Security),
	}

	// Only attach a workflow block when there's an actual graph. A 0-node draft
	// would otherwise serialize as `workflow: {}`, which is invalid ("flow: no
	// nodes declared") and confusing in SOUL.yaml. An empty draft stays a bare
	// (incomplete) agent the user can fill in or convert to a reasoning agent.
	if len(draft.Flow.Nodes) > 0 {
		def.Workflow = &agent.WorkflowSpec{
			Nodes:             draft.Flow.Nodes,
			Edges:             draft.Flow.Edges,
			Entry:             draft.Flow.Entry,
			Output:            draft.Flow.Output,
			MaxNodeExecutions: draft.Flow.MaxNodeExecutions,
		}
	}

	// Carry the business-outcome contract onto the saved agent (P0-4). Without
	// this the draft's assertions die at save and production judges the agent by
	// node errors alone — the gap that let a workflow deliver an empty brief and
	// record a successful run.
	if oc := draft.Outcome.ToAgentContract(); oc != nil {
		def.Outcome = oc
	}

	// Project the flow's capability surface onto the Definition so the tier
	// classifier (internal/tier) sees what the workflow can actually DO. A
	// flow tool node naming `shell_exec`/`write_file` must classify the
	// agent Privileged exactly as if it had been listed in `builtins:`; a
	// flow agent node naming a peer feeds transitive peer detection.
	builtins, mcpTools := flowTools(draft.Flow)
	if len(builtins) > 0 {
		def.Builtins = &builtins
	}
	if flowNeedsSystemCapability(draft.Flow) && acceptPrivilegedExposure {
		def.Capabilities = appendCapability(def.Capabilities, "system")
	}
	if len(mcpTools) > 0 {
		def.MCPTools = &mcpTools
	}
	if peers := flowPeers(draft.Flow); len(peers) > 0 {
		def.Agents = peers
	}

	// Enable the skills the flow actually loads (read_skill nodes). Without this
	// the engine treats skills as disabled and the read_skill node can't resolve.
	if skills := usedSkills(draft.Flow); len(skills) > 0 {
		def.Skills = skills
	}
	// Attach any knowledge bases the draft chose so the agent can use them.
	if len(draft.Knowledge) > 0 {
		seen := map[string]bool{}
		for _, kb := range draft.Knowledge {
			kb = strings.TrimSpace(kb)
			if kb != "" && !seen[kb] {
				seen[kb] = true
				def.Knowledge = append(def.Knowledge, kb)
			}
		}
	}

	// Unattended execution opt-in (Story #14): carry through so a scheduled agent
	// with privileged steps can complete without a human to approve them.
	def.Unattended = draft.Unattended

	// Interface-aware design (Stories #11/#12): record where this agent should
	// appear so cron-only agents don't clutter Chat. Derived deterministically
	// from the trigger + channels; a scheduled agent is schedule-only unless it
	// also targets channels.
	def.Surfaces = studioSurfaces(draft)
	if strings.EqualFold(strings.TrimSpace(draft.Trigger.Type), "chat") {
		def.Channels = nil
	}

	// Schedule triggers carry their cron into the agent Schedule block so
	// the scheduler can register the (disabled) agent unchanged.
	if def.Trigger == agent.TriggerCron {
		if cron, ok := draft.Trigger.Config["cron"].(string); ok && strings.TrimSpace(cron) != "" {
			def.Schedule = &agent.Schedule{Cron: cron}
			if out := scheduleOutputFromDraft(draft.Output); out != nil {
				def.Schedule.Output = out
			}
		}
	}

	// Record the user's save-time acknowledgment on the Definition. Only
	// meaningful when a channel is actually bound (no binding → no privileged
	// exposure to acknowledge), so we gate on Channels to avoid a stray label.
	// This is informational only — it does not grant the channel binding.
	if acceptPrivilegedExposure && len(def.Channels) > 0 {
		if def.Labels == nil {
			def.Labels = map[string]string{}
		}
		def.Labels[StudioPrivilegeAckLabel] = "true"
	}

	return def, nil
}

// toReActAgentDefinition maps a ReAct/Plan-Execute agent Draft onto an
// agent.Definition that runs the reasoning loop (NO workflow block). The agent
// drives an allowlist of tools/skills/peers dynamically.
func toReActAgentDefinition(draft Draft, id string, acceptPrivilegedExposure bool) (agent.Definition, error) {
	strategy := strings.ToLower(strings.TrimSpace(draft.Strategy))
	reasoningCfg := reasoningConfigFor(draft, strategy)
	runTimeout := normalizedRunTimeout(draft.RunTimeout, reasoningCfg.TotalTimeout)

	def := agent.Definition{
		ID:                 id,
		Name:               draft.Name,
		Description:        reactDescription(draft),
		Trigger:            mapTrigger(draft.Trigger.Type),
		Channels:           append([]string(nil), draft.Channels...),
		SystemPrompt:       reactSystemPrompt(draft),
		StudioIntent:       strings.TrimSpace(draft.Intent),
		StudioRefined:      draft.Refined,
		StudioRawIntent:    strings.TrimSpace(draft.RawIntent),
		StudioDeliveryMode: normalizeStudioDeliveryMode(draft.DeliveryMode),
		StudioTriggerMode:  normalizeStudioTriggerMode(draft.Trigger.Type),
		Enabled:            false, // staged for review, like every Studio save
		MaxTurns:           maxTurnsOr(draft.MaxTurns, 15),
		Memory:             memoryOr(draft.Memory),
		LLM:                llmConfigFor(draft),
		RunTimeout:         runTimeout,
		// The reasoning loop — the whole point. No Workflow block. Studio sets
		// sensible reasoning timeouts up front (the engine's bare defaults of
		// 30s/step and 180s total are tuned for fast cloud calls and trip the
		// validator as "may be too short"); plan_execute runs more steps so it
		// gets the more generous budget. The user can still override in SOUL.yaml.
		Reasoning:    reasoningCfg,
		Unattended:   draft.Unattended,
		ConfirmTools: dedupeNonEmpty(draft.ConfirmTools),
		Security:     cloneSecurityConfig(draft.Security),
	}

	// Tool allowlist → builtins + MCP tools (split on the mcp__ prefix), so the
	// tier classifier and the engine offer exactly these.
	var builtins, mcpTools []string
	seen := map[string]bool{}
	for _, t := range draft.Tools {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		if strings.HasPrefix(t, "mcp__") {
			mcpTools = append(mcpTools, t)
		} else {
			builtins = append(builtins, t)
		}
	}
	if len(builtins) > 0 {
		def.Builtins = &builtins
	}
	if len(mcpTools) > 0 {
		def.MCPTools = &mcpTools
	}

	// Peers, skills, knowledge.
	if peers := dedupeNonEmpty(newAgentIDs(draft)); len(peers) > 0 {
		def.Agents = peers
	}
	if sk := dedupeNonEmpty(draft.Skills); len(sk) > 0 {
		def.Skills = sk
	}
	if kb := dedupeNonEmpty(draft.Knowledge); len(kb) > 0 {
		def.Knowledge = kb
	}

	def.Surfaces = studioSurfaces(draft)
	if strings.EqualFold(strings.TrimSpace(draft.Trigger.Type), "chat") {
		def.Channels = nil
	}
	if def.Trigger == agent.TriggerCron {
		if cron, ok := draft.Trigger.Config["cron"].(string); ok && strings.TrimSpace(cron) != "" {
			def.Schedule = &agent.Schedule{Cron: cron}
			if out := scheduleOutputFromDraft(draft.Output); out != nil {
				def.Schedule.Output = out
			}
		}
	}
	if acceptPrivilegedExposure && len(def.Channels) > 0 {
		def.Labels = map[string]string{StudioPrivilegeAckLabel: "true"}
	}
	return def, nil
}

// newAgentIDs returns the ids of peer agents the draft defines/references.
func newAgentIDs(draft Draft) []string {
	out := make([]string, 0, len(draft.NewAgents))
	for _, na := range draft.NewAgents {
		out = append(out, na.ID)
	}
	return out
}

func dedupeNonEmpty(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func cloneSecurityConfig(in *agent.SecurityConfig) *agent.SecurityConfig {
	if in == nil {
		return nil
	}
	cp := *in
	return &cp
}

// reactSystemPrompt builds the agent's system prompt: the model-authored prompt
// (which should already carry the task + ordered approach) plus a strategy
// contract so the agent uses its tools methodically.
const legacyReactLoopGuidance = "Work the task by reasoning step by step: decide the next action, call ONE tool, read its result, then decide the next step from what actually happened — never assume a step succeeded. Loop until the goal is met. For lists, act on each item; for asynchronous jobs, poll status until ready before continuing. On a tool error, adapt or stop gracefully with a clear message."

// reactLoopGuidance is the standard ReAct operating instruction appended to a
// reasoning agent's system prompt. It is a fixed constant so we can detect it
// already being present (and dedupe stacked copies) on re-save.
const reactLoopGuidance = `## Reasoning Strategy Contract

Use ReAct only as an internal control loop. Do not reveal hidden chain-of-thought, scratchpad text, or ReAct JSON to the user.

Operating rules:
1. Observe the user request, available context, and the last tool result.
2. Decide the smallest useful next action.
3. Call exactly one tool with schema-valid arguments.
4. Read the actual result before deciding the next step; never assume a tool succeeded.
5. If a tool fails because a field is missing or misnamed, repair the obvious alias once, then continue. Do not retry the same failed tool call with identical arguments.
6. If the same tool/argument shape fails twice, switch approach or stop with a clear blocker and preserve partial results.
7. For lists, process each item deliberately. For async jobs, poll status until ready or until the configured retry limit is reached.
8. For channel delivery, use channel.status when the destination is unclear; channel.send uses text for the message body.
9. Final answers must be clean user-facing results, not raw tool JSON, internal notes, IDs alone, receipts alone, or stack traces.`

const planExecuteLoopGuidance = `## Reasoning Strategy Contract

Use Plan-Execute as an internal project loop. Do not reveal hidden chain-of-thought, scratchpad text, or plan JSON to the user.

Operating rules:
1. Build a compact numbered plan before acting. Each phase must have observable success criteria.
2. Execute phases in order and mark a phase complete only when tool output proves the criteria were met.
3. Keep loops bounded: process known item lists once unless a retry is justified, and set a clear max poll/retry count for async work.
4. If a phase fails, revise the plan at most once from the actual error. Do not retry the same failed tool call with identical arguments.
5. Preserve partial results and continue only when downstream phases still make sense.
6. For creation/delivery jobs, the run is not complete until the requested artifact is created, status is ready, and delivery is confirmed or a clear fallback is returned.
7. For channel delivery, use channel.status when the destination is unclear; channel.send uses text for the message body.
8. Final answers must summarize completed phases, skipped items, failed phase if any, and the best usable artifact/result.`

const autoToolCallingGuidance = `## Reasoning Strategy Contract

Use the model's native tool-calling ability. Do not emit Thought/Action JSON, ReAct transcripts, or a visible plan protocol.

Operating rules:
1. Select tools only when they help complete the user's goal; otherwise answer normally.
2. Validate required tool arguments before calling. Repair obvious field aliases once, but do not repeat identical failed calls.
3. Read tool results before acting on them, and preserve partial results if a later step fails.
4. For ordinary interactive replies, return the answer normally. Use channel.send only for explicit out-of-band delivery.
5. Final answers must be clean user-facing results, not raw tool JSON, internal notes, IDs alone, receipts alone, or stack traces.`

func reactSystemPrompt(draft Draft) string {
	var b strings.Builder
	if p := strings.TrimSpace(draft.SystemPrompt); p != "" {
		b.WriteString(agentprompt.EnsureShared(p))
	} else {
		name := strings.TrimSpace(draft.Name)
		if name == "" {
			name = "this agent"
		}
		fmt.Fprintf(&b, "You are %q, an autonomous agent built in Soulacy Studio.", name)
		p := b.String()
		b.Reset()
		b.WriteString(agentprompt.EnsureShared(p))
	}
	guidance := reasoningGuidanceForStrategy(draft.Strategy)
	prompt := stripReasoningGuidance(b.String())
	b.Reset()
	b.WriteString(prompt)
	// Append the strategy guidance only if the prompt doesn't already carry it.
	// The prompt round-trips through draft.SystemPrompt after a prior save, so
	// appending unconditionally stacked a new copy on every save.
	if guidance != "" && !strings.Contains(b.String(), guidance) {
		b.WriteString("\n\n")
		b.WriteString(guidance)
	}
	if contract := completionContractPrompt(draft); contract != "" && !strings.Contains(b.String(), completionContractHeading) {
		b.WriteString("\n\n")
		b.WriteString(contract)
	}
	// The operator's own contract, after the generic one so their words win on a
	// conflict. Guarded by its heading so a re-save does not stack copies — the
	// prompt round-trips through draft.SystemPrompt.
	if oc := ContractPrompt(draft.Policy); oc != "" && !strings.Contains(b.String(), operatorContractHeading) {
		b.WriteString("\n\n")
		b.WriteString(oc)
	}
	if t := strings.TrimSpace(draft.Intent); t != "" {
		if goal := "Goal: " + t; !strings.Contains(b.String(), goal) {
			b.WriteString("\n\n")
			b.WriteString(goal)
		}
	}
	// Self-heal prompts that already accumulated duplicate guidance paragraphs
	// from earlier saves: keep the first occurrence, drop the rest, tidy blanks.
	out := b.String()
	for _, para := range []string{reactLoopGuidance, planExecuteLoopGuidance, autoToolCallingGuidance} {
		out = dedupeParagraph(out, para)
	}
	return out
}

func reasoningGuidanceForStrategy(strategy string) string {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "plan_execute":
		return planExecuteLoopGuidance
	case "auto":
		return autoToolCallingGuidance
	default:
		return reactLoopGuidance
	}
}

func stripReasoningGuidance(text string) string {
	for _, para := range []string{legacyReactLoopGuidance, reactLoopGuidance, planExecuteLoopGuidance, autoToolCallingGuidance} {
		text = strings.ReplaceAll(text, para, "")
	}
	return strings.TrimSpace(regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n"))
}

func scheduleOutputFromDraft(out *ScheduleOutput) *agent.ScheduleOutput {
	if out == nil {
		return nil
	}
	res := &agent.ScheduleOutput{
		Channel:  strings.TrimSpace(out.Channel),
		To:       strings.TrimSpace(out.To),
		BotName:  strings.TrimSpace(out.BotName),
		Template: strings.TrimSpace(out.Template),
	}
	if res.Channel == "" {
		return nil
	}
	if res.To == "" {
		return nil
	}
	return res
}

// dedupeParagraph keeps the first occurrence of para in text and removes every
// later one, then collapses any 3+ newline runs the removals leave behind into a
// single clean paragraph break.
func dedupeParagraph(text, para string) string {
	if para == "" || !strings.Contains(text, para) {
		return text
	}
	idx := strings.Index(text, para)
	head := text[:idx+len(para)]
	tail := strings.ReplaceAll(text[idx+len(para):], para, "")
	res := head + tail
	for strings.Contains(res, "\n\n\n") {
		res = strings.ReplaceAll(res, "\n\n\n", "\n\n")
	}
	return strings.TrimRight(res, "\n ")
}

// defaultReasoningConfig returns a reasoning config with sensible, explicit
// timeouts so a freshly-built agent validates clean (no "timeout not set"
// warnings) and survives slower providers. plan_execute typically runs more
// steps, so it gets a larger total budget. Values are deliberately generous but
// bounded; the user can override any of them in SOUL.yaml.
func defaultReasoningConfig(draft Draft, strategy string) agent.ReasoningConfig {
	maxSteps, maxPlanSteps := defaultReasoningBudgets(draft, strategy)
	cfg := agent.ReasoningConfig{
		Strategy:     strategy,
		MaxSteps:     maxSteps,
		MaxPlanSteps: maxPlanSteps,
		StepTimeout:  "120s",
		TotalTimeout: "600s",
	}
	if strings.EqualFold(strategy, "plan_execute") {
		cfg.TotalTimeout = "900s"
	}
	return cfg
}

// reasoningConfigFor builds the saved reasoning config, PRESERVING any timeouts
// the user already tuned (carried on the draft from a SOUL.yaml round-trip) and
// only filling Studio's sensible defaults where the draft left them empty — so a
// canvas re-save never silently resets hand-set budgets.
func reasoningConfigFor(draft Draft, strategy string) agent.ReasoningConfig {
	cfg := defaultReasoningConfig(draft, strategy)
	if t := strings.TrimSpace(draft.StepTimeout); t != "" {
		cfg.StepTimeout = t
	}
	if t := strings.TrimSpace(draft.TotalTimeout); t != "" {
		cfg.TotalTimeout = t
	}
	if draft.MaxSteps > 0 {
		cfg.MaxSteps = draft.MaxSteps
	}
	if draft.MaxPlanSteps > 0 {
		cfg.MaxPlanSteps = draft.MaxPlanSteps
	}
	// Carry the operator's contract onto the definition so it survives the save
	// and the SOUL.yaml round-trip. Only what the draft actually states is
	// written — merging the defaults in here would fill every agent's YAML with
	// a policy nobody chose.
	cfg.Contract = draft.Policy.ToReasoningContract()
	// Turning parallelism OFF is the one loop flag the engine already honours:
	// MaxParallelSteps of 1 is its documented strictly-serial walk.
	if pl := draft.Policy.GetPlan(); pl != nil && !pl.ParallelIndependentSteps {
		cfg.MaxParallelSteps = 1
	}
	return cfg
}

func defaultReasoningBudgets(draft Draft, strategy string) (int, int) {
	if strings.EqualFold(strategy, "plan_execute") {
		if highComplexityReasoningTask(draft) {
			return 24, 12
		}
		return 16, 8
	}
	if highComplexityReasoningTask(draft) {
		return 18, 8
	}
	return 8, 6
}

func highComplexityReasoningTask(draft Draft) bool {
	text := strings.ToLower(strings.Join([]string{
		draft.Intent,
		draft.RawIntent,
		draft.SystemPrompt,
		strings.Join(draft.Tools, " "),
	}, " "))
	if strings.Contains(text, "notebooklm") || strings.Contains(text, "notebook lm") ||
		strings.Contains(text, "podcast") || strings.Contains(text, "audio overview") {
		return true
	}
	hits := 0
	for _, word := range []string{
		"search", "find", "fetch", "read", "rank", "filter", "summarize",
		"create", "generate", "poll", "wait", "store", "ingest", "send", "deliver",
	} {
		if strings.Contains(text, word) {
			hits++
		}
	}
	if hits >= 4 {
		return true
	}
	mcpCount := 0
	for _, tool := range draft.Tools {
		if strings.HasPrefix(strings.TrimSpace(tool), "mcp__") {
			mcpCount++
		}
	}
	return len(draft.Tools) >= 8 || mcpCount >= 4
}

func normalizedRunTimeout(runTimeout, reasoningTotal string) string {
	runTimeout = strings.TrimSpace(runTimeout)
	total := parsePositiveDuration(reasoningTotal)
	run := parsePositiveDuration(runTimeout)
	if total <= 0 {
		if runTimeout == "" {
			return ""
		}
		return runTimeout
	}
	if run <= 0 || run < total {
		return total.String()
	}
	return runTimeout
}

func parsePositiveDuration(s string) time.Duration {
	if strings.TrimSpace(s) == "" {
		return 0
	}
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// maxTurnsOr returns v when positive, else the fallback — so a user-tuned
// max_turns survives the round-trip while an unset draft gets the default.
// memoryOr returns the draft's memory policy, or Studio's default when the
// draft carries none. Overwriting a policy the user tuned in SOUL.yaml with a
// constant is the same class of loss as resetting max_turns.
func memoryOr(m *agent.MemoryPolicy) agent.MemoryPolicy {
	if m == nil {
		return agent.MemoryPolicy{MaxTokens: 8000}
	}
	out := *m
	if out.MaxTokens <= 0 {
		out.MaxTokens = 8000
	}
	out.ReadScopes = append([]string(nil), m.ReadScopes...)
	out.WriteScopes = append([]string(nil), m.WriteScopes...)
	return out
}

func maxTurnsOr(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}

func reactDescription(draft Draft) string {
	mode := "ReAct"
	if strings.EqualFold(draft.Strategy, "plan_execute") {
		mode = "Plan-Execute"
	}
	out := fmt.Sprintf("Studio %s agent — %s", mode, triggerShort(draft.Trigger))
	if len(draft.Channels) > 0 {
		out += " → " + strings.Join(draft.Channels, ", ")
	}
	return out + "."
}

// flowTools collects the distinct, non-empty tool names from a flow's
// tool nodes, in first-seen order, separating them into builtins and MCP tools.
func flowTools(flow Flow) (builtins []string, mcpTools []string) {
	seen := map[string]bool{}
	for _, n := range flow.Nodes {
		tool := strings.TrimSpace(n.Tool)
		if tool == "" || seen[tool] {
			continue
		}
		seen[tool] = true
		if strings.HasPrefix(tool, "mcp__") {
			mcpTools = append(mcpTools, tool)
		} else {
			builtins = append(builtins, tool)
		}
	}
	return builtins, mcpTools
}

// flowPeers collects the distinct, non-empty peer agent ids referenced by a
// flow's agent nodes, in first-seen order, so they populate def.Agents and
// feed the tier classifier's transitive peer walk.
func flowPeers(flow Flow) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range flow.Nodes {
		a := strings.TrimSpace(n.Agent)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// mapTrigger translates Studio's trigger.type vocabulary (chat | schedule |
// channel | webhook | manual) onto the agent's TriggerKind. Chat and manual have no direct
// agent equivalent; it maps to TriggerInternal (programmatic activation).
// studioSurfaces derives the agent's interface surfaces from the draft's
// trigger + channels (Stories #11/#12). Cron-only agents stay schedule-only,
// but a scheduled agent that explicitly selects channels is also chat-visible:
// that is the supported "scheduled plus interactive" shape for digest bots,
// research librarians, and other agents that should run unattended but still
// answer when a human pings them or tests them manually.
func studioSurfaces(draft Draft) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		for _, e := range out {
			if e == s {
				return
			}
		}
		out = append(out, s)
	}
	switch strings.ToLower(strings.TrimSpace(draft.Trigger.Type)) {
	case "chat":
		add(agent.SurfaceChat)
	case "schedule", "cron", "oneshot":
		add(agent.SurfaceSchedule)
		for _, ch := range draft.Channels {
			add(ch) // a scheduled digest still delivers to its channel(s)
		}
		if len(draft.Channels) > 0 {
			add(agent.SurfaceChat)
		}
	case "channel":
		for _, ch := range draft.Channels {
			add(ch)
		}
		add(agent.SurfaceChat)
	case "webhook":
		add("webhook")
	default: // manual / internal
		add(agent.SurfaceChat)
		for _, ch := range draft.Channels {
			add(ch)
		}
	}
	return out
}

func mapTrigger(t string) agent.TriggerKind {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "schedule", "cron":
		return agent.TriggerCron
	case "channel":
		return agent.TriggerChannel
	case "webhook":
		return agent.TriggerWebhook
	case "chat", "manual", "internal":
		return agent.TriggerInternal
	default:
		// An unspecified/unknown trigger type must NOT default into the most
		// exposed kind (channel). Fall back to internal (manual/programmatic)
		// so an under-specified draft is conservative; the validate layer also
		// warns on unknown trigger types.
		return agent.TriggerInternal
	}
}

// slug derives a stable, lowercase, dash-separated agent id from a name.
func slug(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = slugRE.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// systemBuiltins are the OS-level builtins whose presence means a workflow runs
// code/commands on the host (used to add a scope-of-action line to the prompt).
var systemBuiltins = map[string]bool{
	"shell_exec": true, "run_script": true, "write_file": true,
	"download_file": true, "install_library": true, "package_install": true,
}

func flowNeedsSystemCapability(f Flow) bool {
	for _, n := range f.Nodes {
		if systemBuiltins[strings.TrimSpace(n.Tool)] {
			return true
		}
		for _, cap := range n.Requires {
			if strings.TrimSpace(cap) == "system" {
				return true
			}
		}
	}
	return false
}

func appendCapability(caps []string, cap string) []string {
	cap = strings.TrimSpace(cap)
	if cap == "" {
		return caps
	}
	for _, existing := range caps {
		if strings.TrimSpace(existing) == cap {
			return caps
		}
	}
	return append(caps, cap)
}

// buildSystemPrompt derives a well-defined system prompt from the saved
// workflow: who the agent is, when it runs, the ordered steps it performs, where
// output goes, and the scope it must stay within. A Studio-saved agent should
// arrive with a real prompt — not a generic placeholder — so it reads clearly in
// the Agents list and grounds any LLM/agent steps it contains.
func buildSystemPrompt(draft Draft) string {
	var b strings.Builder
	if prompt := strings.TrimSpace(draft.SystemPrompt); prompt != "" {
		b.WriteString(agentprompt.EnsureShared(prompt))
		b.WriteString("\n\n")
	} else {
		name := strings.TrimSpace(draft.Name)
		if name == "" {
			name = "this workflow"
		}
		fmt.Fprintf(&b, "You are %q, an automation agent created in Soulacy Studio. ", name)
		p := b.String()
		b.Reset()
		b.WriteString(agentprompt.EnsureShared(p))
		b.WriteString("\n\n")
	}
	b.WriteString("You execute a fixed workflow graph: each step runs in order and its output feeds the next according to the edges. Follow the graph faithfully and do not invent steps or take actions outside it.\n\n")

	b.WriteString("Trigger: ")
	b.WriteString(describeTrigger(draft.Trigger))
	b.WriteString("\n\n")

	ids := orderedNodeIDs(draft.Flow)
	if len(ids) > 0 {
		byID := make(map[string]sdkr.FlowNode, len(draft.Flow.Nodes))
		for _, n := range draft.Flow.Nodes {
			byID[n.ID] = n
		}
		b.WriteString("Steps:\n")
		i := 1
		for _, id := range ids {
			n, ok := byID[id]
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "%d. %s\n", i, describeNode(n))
			i++
		}
		b.WriteString("\n")
	}

	if len(draft.Channels) > 0 {
		fmt.Fprintf(&b, "Output: deliver results to the following channel(s): %s.\n\n", strings.Join(draft.Channels, ", "))
	}
	if contract := completionContractPrompt(draft); contract != "" && !strings.Contains(b.String(), completionContractHeading) {
		b.WriteString(contract)
		b.WriteString("\n\n")
	}

	if flowHasHostExecution(draft.Flow) {
		b.WriteString("Some steps run code or system commands on the host. Operate strictly within the actions defined by the steps above; never take actions beyond them.\n\n")
	}

	b.WriteString("Be reliable and concise. On a step error, honor that step's on_error policy (retry, skip, or abort).")
	return b.String()
}

// describeWorkflowShort is the one-line agent Description: trigger + step count
// (+ channels), so the Agents list reads meaningfully.
func describeWorkflowShort(draft Draft) string {
	n := len(draft.Flow.Nodes)
	plural := "s"
	if n == 1 {
		plural = ""
	}
	out := fmt.Sprintf("Studio workflow — %s, %d step%s", triggerShort(draft.Trigger), n, plural)
	if len(draft.Channels) > 0 {
		out += " → " + strings.Join(draft.Channels, ", ")
	}
	return out + "."
}

func triggerShort(t Trigger) string {
	switch strings.ToLower(strings.TrimSpace(t.Type)) {
	case "schedule", "cron":
		if cron, ok := t.Config["cron"].(string); ok && strings.TrimSpace(cron) != "" {
			return "scheduled (" + cron + ")"
		}
		return "scheduled"
	case "channel":
		return "channel-triggered"
	case "webhook":
		return "webhook-triggered"
	default:
		return "manual"
	}
}

func describeTrigger(t Trigger) string {
	switch strings.ToLower(strings.TrimSpace(t.Type)) {
	case "schedule", "cron":
		if cron, ok := t.Config["cron"].(string); ok && strings.TrimSpace(cron) != "" {
			return fmt.Sprintf("runs on a schedule (cron %q).", cron)
		}
		return "runs on a schedule."
	case "channel":
		return "runs when a message arrives on a connected channel."
	case "chat":
		return "runs when a user sends a message in Soulacy GUI Chat."
	case "webhook":
		return "runs when its webhook endpoint is called."
	case "manual", "internal", "":
		return "runs when triggered manually or programmatically."
	default:
		return fmt.Sprintf("trigger type %q.", t.Type)
	}
}

// describeNode renders one step as a concise imperative sentence.
func describeNode(n sdkr.FlowNode) string {
	var head string
	switch {
	case n.Kind == sdkr.FlowNodePython:
		if strings.TrimSpace(n.Tool) != "" {
			head = fmt.Sprintf("Run the %q Python tool", n.Tool)
		} else {
			head = "Run an inline Python step"
		}
	case strings.TrimSpace(n.Tool) != "":
		head = fmt.Sprintf("Call the %q tool", n.Tool)
	case strings.TrimSpace(n.Agent) != "":
		head = fmt.Sprintf("Hand off to the %q agent", n.Agent)
	case n.Kind == sdkr.FlowNodeBranch:
		head = "Branch based on a condition"
	default:
		head = "Run a step"
	}
	meta := []string{"id " + n.ID}
	if out := strings.TrimSpace(n.Output); out != "" {
		meta = append(meta, "output → "+out)
	}
	return fmt.Sprintf("%s (%s).", head, strings.Join(meta, "; "))
}

// orderedNodeIDs returns node ids in execution order: a BFS from the entry along
// the edges, with any unreached nodes appended in declared order. Deterministic.
func orderedNodeIDs(flow Flow) []string {
	if len(flow.Nodes) == 0 {
		return nil
	}
	exists := make(map[string]bool, len(flow.Nodes))
	for _, n := range flow.Nodes {
		exists[n.ID] = true
	}
	adj := map[string][]string{}
	for _, e := range flow.Edges {
		if e.To == "" || e.To == "end" {
			continue
		}
		adj[e.From] = append(adj[e.From], e.To)
	}
	entry := flow.Entry
	if entry == "" || !exists[entry] {
		entry = flow.Nodes[0].ID
	}
	var order []string
	seen := map[string]bool{}
	queue := []string{entry}
	seen[entry] = true
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if exists[cur] {
			order = append(order, cur)
		}
		for _, nx := range adj[cur] {
			if exists[nx] && !seen[nx] {
				seen[nx] = true
				queue = append(queue, nx)
			}
		}
	}
	for _, n := range flow.Nodes {
		if !seen[n.ID] {
			order = append(order, n.ID)
		}
	}
	return order
}

// flowHasHostExecution reports whether any node runs code/commands on the host
// (a python/code node, or a system builtin), to add a scope line to the prompt.
func flowHasHostExecution(flow Flow) bool {
	for _, n := range flow.Nodes {
		if n.Kind == sdkr.FlowNodePython || strings.TrimSpace(n.Code) != "" {
			return true
		}
		if systemBuiltins[strings.TrimSpace(n.Tool)] {
			return true
		}
	}
	return false
}
