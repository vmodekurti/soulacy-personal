// load_agent.go — the reverse of save.go's ToAgentDefinition: turn a saved
// agent.Definition (one that carries a workflow graph) back into a Studio Draft
// so it can be re-opened, edited on the canvas, and re-saved. Powers Studio's
// "My Workflows" → Edit. Pure mapping (no I/O) so it is unit-testable.
package studio

import (
	"strings"

	"github.com/soulacy/soulacy/pkg/agent"
)

// HasWorkflow reports whether an agent definition carries a graph workflow
// (i.e. it was authored in Studio, or is otherwise editable as a flow). Agents
// without a Workflow are skipped by the "My Workflows" list.
func HasWorkflow(def agent.Definition) bool {
	return def.Workflow != nil && len(def.Workflow.Nodes) > 0
}

// FromAgentDefinition maps a saved agent.Definition back into a Studio Draft.
// It is the inverse of ToAgentDefinition for the fields Studio owns: name,
// trigger (+ cron), channels, and the flow graph. Fields the agent gained
// outside Studio (system prompt, labels, …) are intentionally dropped — Studio
// edits the WORKFLOW, and a re-save regenerates those.
func FromAgentDefinition(def agent.Definition) Draft {
	d := Draft{
		ID:           def.ID,
		Name:         def.Name,
		Intent:       def.StudioIntent,
		Refined:      def.StudioRefined,
		RawIntent:    def.StudioRawIntent,
		DeliveryMode: def.StudioDeliveryMode,
		Trigger:      Trigger{Type: studioTriggerType(def)},
		Channels:     append([]string(nil), def.Channels...),
		// Preserve the agent's LLM config (provider/model/temperature/...) and the
		// whole-run timeout so a Studio round-trip is lossless. Applies to BOTH the
		// ReAct and workflow branches below since they share this construction.
		LLM:        def.LLM,
		RunTimeout: def.RunTimeout,
		// Read back on BOTH branches. The agent branch already carried MaxTurns;
		// the workflow branch carried neither it nor Memory, so opening a
		// workflow and re-saving reset both to Studio's constants.
		MaxTurns:     def.MaxTurns,
		Memory:       cloneMemoryPolicy(def.Memory),
		ConfirmTools: append([]string(nil), def.ConfirmTools...),
		Security:     cloneSecurityConfig(def.Security),
	}
	if d.Name == "" {
		d.Name = def.ID
	}
	if def.Schedule != nil && strings.TrimSpace(def.Schedule.Cron) != "" {
		d.Trigger.Config = map[string]any{"cron": def.Schedule.Cron}
		if def.Schedule.Output != nil {
			d.Output = &ScheduleOutput{
				Channel:  def.Schedule.Output.Channel,
				To:       def.Schedule.Output.To,
				BotName:  def.Schedule.Output.BotName,
				Template: def.Schedule.Output.Template,
			}
		}
	}

	// A reasoning agent (ReAct/Plan-Execute) has NO workflow graph — its substance
	// is the strategy + system prompt + tool/skill allowlist. Preserve those so the
	// round-trip is LOSSLESS: re-opening or switching to Canvas keeps it an agent
	// (IsAgent stays true → no `workflow:` block is added on save) and the mode
	// toggle reflects the real strategy. Without this the agent silently degraded
	// into an (empty) workflow.
	if strat := strings.ToLower(strings.TrimSpace(def.Reasoning.Strategy)); isAgentStrategy(strat) {
		d.Strategy = strat
		d.SystemPrompt = def.SystemPrompt
		// Without this the Build step's contract panel opened empty for every
		// saved agent, which read as "you never filled this in" rather than
		// "this was never stored".
		d.Policy = AgentPolicyFromReasoning(def.Reasoning.Contract)
		d.Unattended = def.Unattended
		d.Tools = agentToolList(def)
		d.Skills = append([]string(nil), def.Skills...)
		d.Knowledge = append([]string(nil), def.Knowledge...)
		// Preserve the reasoning-loop budgets so a re-save doesn't reset values the
		// user tuned in SOUL.yaml back to Studio defaults.
		d.StepTimeout = def.Reasoning.StepTimeout
		d.TotalTimeout = def.Reasoning.TotalTimeout
		d.MaxSteps = def.Reasoning.MaxSteps
		d.MaxPlanSteps = def.Reasoning.MaxPlanSteps
		for _, id := range def.Agents {
			if id = strings.TrimSpace(id); id != "" {
				d.NewAgents = append(d.NewAgents, NewAgent{ID: id})
			}
		}
		return d
	}

	// Workflow agent: preserve the fields the canvas OWNS but didn't used to round
	// trip — attached knowledge bases, the unattended opt-in, peer agents, and the
	// node-execution budget — so opening in canvas and re-saving doesn't silently
	// wipe them.
	d.Knowledge = append([]string(nil), def.Knowledge...)
	d.Unattended = def.Unattended
	for _, id := range def.Agents {
		if id = strings.TrimSpace(id); id != "" {
			d.NewAgents = append(d.NewAgents, NewAgent{ID: id})
		}
	}
	if def.Workflow != nil {
		d.Flow = Flow{
			Nodes:             append(def.Workflow.Nodes[:0:0], def.Workflow.Nodes...),
			Edges:             append(def.Workflow.Edges[:0:0], def.Workflow.Edges...),
			Entry:             def.Workflow.Entry,
			Output:            def.Workflow.Output,
			MaxNodeExecutions: def.Workflow.MaxNodeExecutions,
		}
	}
	return d
}

// cloneMemoryPolicy copies a definition's memory policy for the draft, so the
// draft never aliases the loader's live definition. A zero policy reads as
// "nothing was set" and is returned as nil, which is what makes ToAgentDefinition
// apply Studio's default rather than persisting an all-zero block.
func cloneMemoryPolicy(m agent.MemoryPolicy) *agent.MemoryPolicy {
	if m.MaxTokens == 0 && len(m.ReadScopes) == 0 && len(m.WriteScopes) == 0 {
		return nil
	}
	out := m
	out.ReadScopes = append([]string(nil), m.ReadScopes...)
	out.WriteScopes = append([]string(nil), m.WriteScopes...)
	return &out
}

// agentToolList reassembles the draft's flat tool allowlist (builtin + mcp__
// names) from the definition's split Builtins/MCPTools — the inverse of how
// toReActAgentDefinition splits draft.Tools on save.
func agentToolList(def agent.Definition) []string {
	var out []string
	if def.Builtins != nil {
		out = append(out, *def.Builtins...)
	}
	if def.MCPTools != nil {
		out = append(out, *def.MCPTools...)
	}
	return out
}

// triggerTypeFromKind is the inverse of mapTrigger: agent TriggerKind → the
// Studio trigger.type vocabulary (schedule | channel | webhook | manual).
func triggerTypeFromKind(k agent.TriggerKind) string {
	switch k {
	case agent.TriggerCron:
		return "schedule"
	case agent.TriggerChannel:
		return "channel"
	case agent.TriggerWebhook:
		return "webhook"
	case agent.TriggerInternal:
		return "manual"
	default:
		return "manual"
	}
}

func studioTriggerType(def agent.Definition) string {
	if mode := normalizeStudioTriggerMode(def.StudioTriggerMode); mode != "" {
		return mode
	}
	return triggerTypeFromKind(def.Trigger)
}
