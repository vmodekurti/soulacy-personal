package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/message"
	sdkhooks "github.com/soulacy/soulacy/sdk/hooks"
)

// SetLifecycleHooks installs the stable SDK hook chain used by this engine.
// Hooks add policy; they cannot bypass Soulacy's existing security controls.
func (e *Engine) SetLifecycleHooks(registry *sdkhooks.Registry) {
	e.lifecycleHooks = registry
}

func (e *Engine) runLifecycleHook(ctx context.Context, stage sdkhooks.Stage, agentID, sessionID string, data map[string]any) error {
	if e == nil || e.lifecycleHooks == nil {
		return nil
	}
	meta := llm.CallMetadataFromContext(ctx)
	event := sdkhooks.Event{
		Stage: stage, RunID: meta.RunID, AgentID: agentID, SessionID: sessionID,
		Timestamp: time.Now().UTC(), Data: data,
	}
	decision, err := e.lifecycleHooks.Run(ctx, event)
	payload := map[string]any{"stage": stage, "denied": decision.Deny}
	if decision.Reason != "" {
		payload["reason"] = decision.Reason
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	if e.sink != nil {
		e.sink.Emit(message.Event{Type: "lifecycle." + string(stage), AgentID: agentID, SessionID: sessionID, Payload: payload, Timestamp: time.Now().UTC()})
	}
	if err != nil {
		return err
	}
	if decision.Deny {
		return fmt.Errorf("lifecycle hook denied %s: %s", stage, decision.Reason)
	}
	return nil
}

func lifecycleAfterData(outcome string, err error) map[string]any {
	data := map[string]any{"outcome": outcome}
	if err != nil {
		data["error"] = boundedObservedError(err)
	}
	return data
}

func llmLifecycleAfterData(resp *llm.CompletionResponse, provider, model string, started time.Time, err error) map[string]any {
	after := lifecycleAfterData("success", err)
	after["duration_ms"] = time.Since(started).Milliseconds()
	if resp != nil {
		after["provider"] = selectedRouteProvider(resp, provider)
		after["model"] = selectedRouteModel(resp, model)
		after["input_tokens"] = resp.InputTokens
		after["output_tokens"] = resp.OutputTokens
		after["tool_calls"] = len(resp.ToolCalls)
	}
	if err != nil {
		after["outcome"] = "error"
	}
	return after
}

// completeLLM is the single lifecycle boundary around runtime-owned model
// calls. The router still owns capability checks, health routing, retries,
// accounting, and completion observability.
func (e *Engine) completeLLM(ctx context.Context, provider string, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	// Avoid touching response fields when no lifecycle policy is installed.
	// Streaming providers are allowed to finish populating usage until their
	// channel closes, so even an otherwise-unused read here would race them.
	if e.lifecycleHooks == nil {
		return e.llmRouter.Complete(ctx, provider, req)
	}

	meta := llm.CallMetadataFromContext(ctx)
	data := map[string]any{
		"provider": provider, "model": req.Model, "phase": req.Routing.Phase,
		"message_count": len(req.Messages), "tool_count": len(req.Tools),
		"estimated_input_tokens": llm.EstimateRequestTokens(req),
	}
	if err := e.runLifecycleHook(ctx, sdkhooks.LLMBefore, meta.AgentID, meta.SessionID, data); err != nil {
		return nil, err
	}
	started := time.Now()
	resp, err := e.llmRouter.Complete(ctx, provider, req)
	if err == nil && resp != nil && resp.Stream != nil {
		// A streaming response is complete only after the provider channel closes.
		// Relay it so the after hook observes final usage without racing the router,
		// while still draining the provider after cancellation for correct billing.
		inner := resp.Stream
		relayed := make(chan string, 64)
		resp.Stream = relayed
		go func() {
			forward := true
			for token := range inner {
				if forward {
					select {
					case relayed <- token:
					case <-ctx.Done():
						forward = false
					}
				}
			}
			streamErr := ctx.Err()
			after := llmLifecycleAfterData(resp, provider, req.Model, started, streamErr)
			close(relayed)
			_ = e.runLifecycleHook(context.WithoutCancel(ctx), sdkhooks.LLMAfter, meta.AgentID, meta.SessionID, after)
		}()
		return resp, nil
	}

	after := llmLifecycleAfterData(resp, provider, req.Model, started, err)
	_ = e.runLifecycleHook(context.WithoutCancel(ctx), sdkhooks.LLMAfter, meta.AgentID, meta.SessionID, after)
	return resp, err
}
