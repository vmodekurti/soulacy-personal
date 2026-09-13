package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/pkg/agent"
)

// ModelPreparation describes execution mechanics, never a rewritten user goal
// or an unmeasured intelligence score. The same preparation powers run events
// and the read-only preview shown by both clients.
type ModelPreparation struct {
	AgentID         string           `json:"agent_id"`
	Profile         llm.ModelProfile `json:"profile"`
	Strategy        string           `json:"strategy"`
	Approach        []string         `json:"approach"`
	Warnings        []string         `json:"warnings"`
	GoalPreserved   bool             `json:"goal_preserved"`
	MaxOutputTokens int              `json:"max_output_tokens"`
	BlockedReason   string           `json:"blocked_reason,omitempty"`
}

// resolveExecutionModel runs BEFORE allowlist checks and metadata discovery.
// An explicit reasoning strategy retains the existing global reasoner fallback;
// auto never silently switches to a different provider/model to gain features.
func (e *Engine) resolveExecutionModel(def *agent.Definition) error {
	if def.Workflow != nil {
		return nil
	} // Nodes/peers select models; tool-only workflows need none.
	strategy := strings.ToLower(strings.TrimSpace(def.Reasoning.Strategy))
	if def.Workflow == nil && strategy != "" && strategy != "auto" {
		resolved := e.reasoningDef(def)
		def.LLM = resolved.LLM
	}
	if e.llmRouter == nil {
		return nil
	} // SDK embedders can supply their own backend.
	provider := def.LLM.Provider
	if provider == "" {
		provider = e.llmRouter.DefaultProvider()
	}
	if !providerAllowed(def.LLM.AllowedProviders, provider) {
		return fmt.Errorf("llm provider %q not in allowed_providers %v", provider, def.LLM.AllowedProviders)
	}
	if e.reasoningBackendFactory != nil && e.llmRouter.Provider(def.LLM.Provider) == nil {
		return nil
	}
	provider, model, err := e.llmRouter.ResolveModel(provider, def.LLM.Model)
	if err != nil {
		return err
	}
	def.LLM.Provider, def.LLM.Model = provider, model
	return nil
}

// PrepareAgentModel is a metadata-only preview. No configuration, history,
// learning, inference, tools or budgets are changed by viewing it.
func (e *Engine) PrepareAgentModel(ctx context.Context, def *agent.Definition) (ModelPreparation, error) {
	if def == nil {
		return ModelPreparation{}, fmt.Errorf("agent not found")
	}
	cp := def.Clone()
	if cp.Kind == "router" {
		return ModelPreparation{AgentID: cp.ID, Profile: llm.UnknownModelProfile("", ""), Strategy: "router", Approach: []string{"Routes messages without a model; the receiving agent prepares its own model."}, Warnings: []string{}, GoalPreserved: true}, nil
	}
	if cp.Workflow != nil {
		return workflowModelPreparation(cp), nil
	}
	if err := e.resolveExecutionModel(cp); err != nil {
		return ModelPreparation{}, err
	}
	if !providerAllowed(cp.LLM.AllowedProviders, cp.LLM.Provider) || !providerAllowed(cp.LLM.AllowedModels, cp.LLM.Model) {
		return ModelPreparation{}, fmt.Errorf("selected provider/model is outside this agent's allowlists")
	}
	return e.prepareModelDefinition(ctx, cp)
}

// prepareModelDefinition only accepts the isolated per-run clone. Advice is
// host-authored, bounded and derived from typed evidence, never model templates
// or provider descriptions (which can contain prompt injection).
func (e *Engine) prepareModelDefinition(ctx context.Context, def *agent.Definition) (ModelPreparation, error) {
	if def.Workflow != nil {
		return workflowModelPreparation(def), nil
	}
	p := llm.UnknownModelProfile(def.LLM.Provider, def.LLM.Model)
	if e.llmRouter != nil && e.llmRouter.Provider(def.LLM.Provider) != nil {
		var err error
		p, err = e.llmRouter.DescribeModel(ctx, def.LLM.Provider, def.LLM.Model)
		if err != nil {
			return ModelPreparation{}, err
		}
	}
	result := ModelPreparation{AgentID: def.ID, Profile: p, GoalPreserved: true, Warnings: []string{}, Approach: []string{
		"Keep the original goal, completion checks, permissions and spending limits unchanged.",
		"Verify results against evidence; stop and explain blockers instead of claiming unverified success.",
	}}
	if p.Warning != "" {
		result.Warnings = append(result.Warnings, p.Warning)
	}
	if p.Chat == llm.SupportNo {
		result.BlockedReason = "The selected model does not support chat completion. Select a chat model before running this agent."
	}
	if p.OutputTokens > 0 && def.LLM.MaxTokens > p.OutputTokens {
		def.LLM.MaxTokens = p.OutputTokens
	}
	if p.ContextTokens > 0 && def.LLM.MaxTokens >= p.ContextTokens {
		def.LLM.MaxTokens = max(1, p.ContextTokens/4)
	}
	result.MaxOutputTokens = max(0, def.LLM.MaxTokens)
	window := llm.ProfileInputBudget(p, max(1024, def.LLM.MaxTokens))
	if window > 0 && window <= 16384 {
		result.Approach = append(result.Approach, "Work in concise milestones: take one bounded step, check its evidence, then continue. Keep only relevant context without dropping requirements.")
	} else if window > 16384 {
		result.Approach = append(result.Approach, "Use the available context to compare relevant evidence and cross-check requirements before finalizing; do not fill the window unnecessarily.")
	} else {
		result.Approach = append(result.Approach, "The context limit is unreported. Keep inputs focused and surface limit errors without silently deleting the task's requirements.")
	}
	if p.Reasoning == llm.SupportYes {
		result.Approach = append(result.Approach, "Use the model's reasoning capability to check assumptions and dependencies within existing limits; return conclusions and verification, not private reasoning traces.")
	}
	auto := strings.TrimSpace(def.Reasoning.Strategy) == "" || strings.EqualFold(strings.TrimSpace(def.Reasoning.Strategy), "auto")
	if p.NativeTools == llm.SupportNo && auto {
		// A protocol switch must not activate previously-unused phase defaults
		// with larger turn/output allowances than the classic loop had.
		turns := def.MaxTurns
		if turns <= 0 {
			turns = 10
		}
		turns = min(turns, e.turnsCeiling())
		if def.Reasoning.MaxSteps <= 0 || def.Reasoning.MaxSteps > turns {
			def.Reasoning.MaxSteps = turns
		}
		output := def.LLM.MaxTokens
		if output <= 0 {
			output = 1024
		}
		for _, phase := range []*agent.ReasoningPhaseConfig{&def.Reasoning.Think, &def.Reasoning.Plan, &def.Reasoning.Reflect} {
			if phase.MaxTokens <= 0 || phase.MaxTokens > output {
				phase.MaxTokens = output
			}
		}
		choice := strings.ToLower(strings.TrimSpace(def.LLM.ToolChoice))
		if (choice != "" && choice != "auto") || def.LLM.OutputSchema != nil || strings.TrimSpace(def.LLM.ResponseFormat) != "" {
			result.BlockedReason = "Automatic prompt-tool fallback cannot preserve this agent's forced-tool or structured-output contract. Select a native-tool model or use an explicit validated workflow; the contract has not been weakened."
		}
	}
	native := p.NativeTools != llm.SupportNo
	if cfg, selected := reasoning.LoopConfigFromDefinition(def, "", native); selected {
		result.Strategy = string(cfg.Strategy)
	} else {
		result.Strategy = "native_tools"
	}
	if p.NativeTools == llm.SupportNo && auto {
		result.Approach = append(result.Approach, "Use the guarded JSON step protocol because this model reports no native tool calling. Tool permissions and approval gates still apply.")
		if p.JSONMode == llm.SupportNo {
			result.BlockedReason = "The selected model cannot support the guarded JSON tool protocol. Select a compatible model; tool permissions and output requirements were not changed."
		}
	} else if p.NativeTools == llm.SupportYes {
		result.Approach = append(result.Approach, "Use structured tool arguments through the selected strategy and validate tool results before continuing.")
	}
	if p.NativeTools == llm.SupportUnknown {
		result.Warnings = append(result.Warnings, "Native tool support is unreported; the configured protocol is retained, not treated as verified.")
	}
	if def.Workflow == nil {
		def.SystemPrompt += "\n\n[Model-aware execution guidance — not a change to the user's goal]\n" + strings.Join(result.Approach, "\n")
	}
	return result, nil
}

func workflowModelPreparation(def *agent.Definition) ModelPreparation {
	return ModelPreparation{AgentID: def.ID, Profile: llm.UnknownModelProfile("", ""), Strategy: "workflow", GoalPreserved: true,
		Approach: []string{"Preserve the authored workflow, goal, permissions and spending limits.", "Each model call checks its selected model's capabilities and limits; delegated agents prepare their own execution approach."},
		Warnings: []string{"Models are selected by workflow steps. A tool-only workflow does not require a model."}}
}
