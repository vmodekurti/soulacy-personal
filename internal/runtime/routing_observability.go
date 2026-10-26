package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func (e *Engine) withRoutingObservability(ctx context.Context, def *agent.Definition, msg message.Message) context.Context {
	ctx = llm.WithRoutingHint(ctx, llm.RoutingHint{Mode: def.LLM.Routing, AllowedProviders: def.LLM.AllowedProviders, AllowedModels: def.LLM.AllowedModels})
	ctx = llm.WithRouteObserver(ctx, func(event llm.RouteEvent) {
		if e.sink != nil {
			e.sink.Emit(message.Event{Type: "llm.route." + event.Kind, AgentID: msg.AgentID, SessionID: msg.SessionID, Payload: routeEventPayload(event.Trace), Timestamp: time.Now().UTC()})
		}
	})
	return llm.WithCompletionObserver(ctx, func(event llm.CompletionEvent) {
		if e.sink == nil {
			return
		}
		capture, maxBytes := e.llmObservability()
		e.sink.Emit(message.Event{Type: "llm." + event.Kind, AgentID: msg.AgentID, SessionID: msg.SessionID, Payload: completionEventPayload(event, capture, maxBytes), Timestamp: time.Now().UTC()})
	})
}

func routingPhase(turn, toolCount int) string {
	if turn == 0 && toolCount > 0 {
		return "tool-planning"
	}
	if turn > 0 {
		return "tool-followup"
	}
	return "conversation"
}

func selectedRouteLabels(resp *llm.CompletionResponse, provider, model string) (string, string) {
	if resp != nil && resp.Route.SelectedProvider != "" {
		return resp.Route.SelectedProvider, resp.Route.SelectedModel
	}
	return provider, model
}

func selectedRouteProvider(resp *llm.CompletionResponse, fallback string) string {
	if resp != nil && resp.Route.SelectedProvider != "" {
		return resp.Route.SelectedProvider
	}
	return fallback
}

func selectedRouteModel(resp *llm.CompletionResponse, fallback string) string {
	if resp != nil && resp.Route.SelectedModel != "" {
		return resp.Route.SelectedModel
	}
	return fallback
}

func routeEventPayload(trace llm.RouteTrace) map[string]any {
	attempts := make([]map[string]any, 0, len(trace.Attempts))
	for _, attempt := range trace.Attempts {
		attempts = append(attempts, map[string]any{
			"provider": attempt.Provider, "model": attempt.Model, "outcome": attempt.Outcome,
			"duration_ms": attempt.Duration.Milliseconds(), "error_category": attempt.ErrorCategory,
			"error": attempt.Error, "http_attempts": attempt.HTTPAttempts,
			"request_ids": attempt.RequestIDs,
		})
	}
	return map[string]any{
		"route_id": trace.ID, "mode": trace.Mode, "task_class": trace.TaskClass,
		"phase": trace.Phase, "decision": trace.Decision, "candidates": trace.Candidates,
		"rejected": trace.Rejected, "selected_provider": trace.SelectedProvider,
		"selected_model": trace.SelectedModel, "attempts": attempts,
		"fallbacks": trace.Fallbacks, "degraded": trace.Degraded,
		"fallback_from": trace.FallbackFrom, "fallback_to": trace.FallbackTo,
		"fallback_reason": trace.FallbackReason,
	}
}

func completionEventPayload(event llm.CompletionEvent, capture bool, maxBytes int) map[string]any {
	payload := map[string]any{
		"call_id": event.ID, "requested_provider": event.RequestedProvider,
		"requested_model": event.RequestedModel, "message_count": len(event.Request.Messages),
		"estimated_input_tokens": llm.EstimateRequestTokens(event.Request), "tool_count": len(event.Request.Tools),
		"tool_names": toolSchemaNames(event.Request.Tools), "phase": event.Request.Routing.Phase,
		"task_class": event.Request.Routing.TaskClass, "response_format": event.Request.ResponseFormat,
		"max_output_tokens": event.Request.MaxTokens, "temperature": event.Request.Temperature,
		"top_p": event.Request.TopP, "reasoning_effort": event.Request.ReasoningEffort,
		"tool_choice": event.Request.ToolChoice, "operation": event.Request.Operation,
		"stream_requested": event.Request.Stream, "json_schema_present": event.Request.JSONSchema != nil,
		"prompt_sha256": hashObservedValue(event.Request),
		"duration_ms":   event.Duration.Milliseconds(), "content_captured": capture,
	}
	if capture {
		content, truncated := boundedObservedJSON(event.Request, maxBytes)
		payload["prompt"] = content
		payload["prompt_truncated"] = truncated
	}
	if event.Response != nil {
		payload["provider"] = selectedRouteProvider(event.Response, event.RequestedProvider)
		payload["model"] = selectedRouteModel(event.Response, event.RequestedModel)
		payload["http_attempts"] = event.Response.AttemptCount
		payload["provider_request_ids"] = event.Response.ProviderRequestIDs
		payload["stream_opened"] = event.Response.Stream != nil
		if event.Response.Stream == nil {
			// Streaming providers may populate usage until immediately before the
			// channel closes. The final llm.response.content event reads the result
			// after that synchronization point; observing any mutable result here
			// would race the provider.
			payload["response_chars"] = len(event.Response.Content)
			payload["tool_calls"] = len(event.Response.ToolCalls)
			payload["response_sha256"] = hashObservedValue(observedResponse(event.Response))
			payload["input_tokens"] = event.Response.InputTokens
			payload["output_tokens"] = event.Response.OutputTokens
			payload["cache_creation_tokens"] = event.Response.CacheCreationTokens
			payload["cache_read_tokens"] = event.Response.CacheReadTokens
			payload["reasoning_tokens"] = event.Response.ReasoningTokens
			payload["tool_use_prompt_tokens"] = event.Response.ToolUsePromptTokens
			payload["total_tokens"] = event.Response.TotalTokens
		}
		if capture && event.Response.Stream == nil {
			payload["response"], payload["response_truncated"] = boundedObservedJSON(observedResponse(event.Response), maxBytes)
		}
	}
	if event.Err != nil {
		payload["outcome"] = "error"
		payload["error"] = boundedObservedError(event.Err)
	} else if event.Kind == "completion" {
		if event.Response != nil && event.Response.Stream != nil {
			payload["outcome"] = "stream_open"
		} else {
			payload["outcome"] = "success"
		}
	}
	return payload
}

func hashObservedValue(value any) string {
	hash := sha256.New()
	_ = json.NewEncoder(hash).Encode(value)
	return hex.EncodeToString(hash.Sum(nil))
}

func boundedObservedJSON(value any, maxBytes int) (string, bool) {
	data, _ := json.Marshal(value)
	return boundedObservedString(string(data), maxBytes)
}

func boundedObservedString(value string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value, false
	}
	return value[:maxBytes], true
}

func boundedObservedError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	if len(value) > 2048 {
		return value[:2048] + "…"
	}
	return value
}

func observedResponse(resp *llm.CompletionResponse) any {
	return struct {
		Content   string             `json:"content"`
		ToolCalls []message.ToolCall `json:"tool_calls,omitempty"`
	}{Content: resp.Content, ToolCalls: resp.ToolCalls}
}

func completionContentPayload(resp *llm.CompletionResponse, capture bool, maxBytes int) map[string]any {
	payload := map[string]any{
		"call_id": resp.Route.ID, "provider": resp.Route.SelectedProvider, "model": resp.Route.SelectedModel,
		"response_chars": len(resp.Content), "response_sha256": hashObservedValue(observedResponse(resp)),
		"tool_calls":   len(resp.ToolCalls),
		"input_tokens": resp.InputTokens, "output_tokens": resp.OutputTokens,
		"cache_creation_tokens": resp.CacheCreationTokens, "cache_read_tokens": resp.CacheReadTokens,
		"reasoning_tokens": resp.ReasoningTokens, "tool_use_prompt_tokens": resp.ToolUsePromptTokens,
		"total_tokens": resp.TotalTokens, "content_captured": capture,
	}
	if capture {
		payload["response"], payload["response_truncated"] = boundedObservedJSON(observedResponse(resp), maxBytes)
	}
	return payload
}
