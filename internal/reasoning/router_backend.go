// router_backend.go — a provider-agnostic LLMBackend.
//
// The native backends (anthropic/openai/ollama) each hand-write the HTTP call
// for their provider, so the reasoning loop only worked for that handful of
// providers. RouterBackend instead delegates the actual completion to a
// Completer — in production the gateway's llm.Router, which already speaks to
// EVERY configured provider (google/gemini, ollama_cloud, grok, mistral, …).
// The prompt scaffolding and JSON parsing are identical to the native
// backends (shared helpers in this package), so behaviour is consistent.
package reasoning

import (
	"context"
	"fmt"
	"strings"
)

// Completer is the minimal seam RouterBackend needs: turn a system+user prompt
// into one text completion. The provider is bound by the adapter that supplies
// this (so the reasoning loop need not know provider specifics).
type Completer interface {
	Complete(ctx context.Context, model, system, user string, params PhaseParams) (string, error)
}

// RouterBackend implements LLMBackend by delegating to a Completer.
type RouterBackend struct {
	comp Completer
	// ThinkModel is used for Think(); PlanReflectModel for Plan()/Reflect().
	// Both default to the agent's configured model.
	ThinkModel       string
	PlanReflectModel string
	ThinkParams      PhaseParams
	PlanParams       PhaseParams
	ReflectParams    PhaseParams
}

// NewRouterBackend builds a RouterBackend that uses model for every phase.
func NewRouterBackend(comp Completer, model string) *RouterBackend {
	return &RouterBackend{comp: comp, ThinkModel: model, PlanReflectModel: model}
}

// ─── Think ────────────────────────────────────────────────────────────────────

func (b *RouterBackend) Think(ctx context.Context, req ThinkRequest) (ThinkResponse, error) {
	system := req.SystemPrompt
	if len(req.ToolNames) > 0 {
		system += fmt.Sprintf("\n\nAvailable tools: %s", strings.Join(req.ToolNames, ", "))
	}
	system += openaiThinkInstructions

	user := fmt.Sprintf("Task: %s\n\n%s", req.TaskInput, formatStepHistory(req.StepHistory))

	params := b.ThinkParams
	if params.MaxTokens <= 0 {
		params.MaxTokens = 1024
	}
	raw, err := b.comp.Complete(ctx, b.ThinkModel, system, user, params)
	if err != nil {
		return ThinkResponse{}, fmt.Errorf("reasoning/router: Think: %w", err)
	}
	var resp ThinkResponse
	if err := unmarshalJSON(raw, &resp); err != nil {
		if recovered, ok := recoverThinkResponseFromRaw(raw, req.ToolNames); ok {
			return recovered, nil
		}
		return ThinkResponse{}, fmt.Errorf("reasoning/router: Think: parse %q: %w", truncate(raw, 120), err)
	}
	return resp, nil
}

// ─── Plan ─────────────────────────────────────────────────────────────────────

func (b *RouterBackend) Plan(ctx context.Context, systemPrompt, taskInput string, maxSteps int) (Plan, error) {
	system := systemPrompt + fmt.Sprintf(openaiPlanInstructions, maxSteps)
	params := b.PlanParams
	if params.MaxTokens <= 0 {
		params.MaxTokens = 1024
	}
	raw, err := b.comp.Complete(ctx, b.PlanReflectModel, system, "Task: "+taskInput, params)
	if err != nil {
		return Plan{}, fmt.Errorf("reasoning/router: Plan: %w", err)
	}
	var plan Plan
	if err := unmarshalJSON(raw, &plan); err != nil {
		return Plan{}, fmt.Errorf("reasoning/router: Plan: parse %q: %w", truncate(raw, 120), err)
	}
	return plan, nil
}

// ─── Reflect ──────────────────────────────────────────────────────────────────

func (b *RouterBackend) Reflect(ctx context.Context, req ReflectRequest) (ReflectResponse, error) {
	system := req.SystemPrompt
	if req.OutputFormat != "" {
		system += "\n\nOutput format: " + req.OutputFormat
	}
	system += openaiReflectInstructions

	user := fmt.Sprintf("Task: %s\n\nStep trace:\n%s\n\nProduce the final answer.",
		req.TaskInput, formatStepHistory(req.Steps))

	params := b.ReflectParams
	if params.MaxTokens <= 0 {
		params.MaxTokens = 2048
	}
	raw, err := b.comp.Complete(ctx, b.PlanReflectModel, system, user, params)
	if err != nil {
		return ReflectResponse{}, fmt.Errorf("reasoning/router: Reflect: %w", err)
	}
	var resp ReflectResponse
	if err := unmarshalJSON(raw, &resp); err != nil {
		// Non-JSON output: use the whole text as the answer rather than dropping
		// it (same tolerance as the native backends — a plain answer beats an
		// empty one).
		return ReflectResponse{Output: strings.TrimSpace(raw)}, nil
	}
	return resp, nil
}
