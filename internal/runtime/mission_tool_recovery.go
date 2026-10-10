package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

type requiredToolRecoveryResult struct {
	Call   *message.ToolCall
	Calls  int
	Tokens int
}

// parseJSONLoose accepts a bare JSON value, a fenced value, or JSON following
// brief provider chatter.
func parseJSONLoose(s string) (any, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	first := -1
	for i, r := range s {
		if r == '{' || r == '[' {
			first = i
			break
		}
	}
	if first > 0 {
		s = s[first:]
	}
	var value any
	if err := json.Unmarshal([]byte(s), &value); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return value, nil
}

// recoverRequiredMissionTool handles providers that accept a named tool_choice
// but return prose or an empty message instead of a function call. It asks for
// arguments only, validates them locally, and returns a normal ToolCall so all
// authorization, confirmation, audit, and dispatch checks remain authoritative.
func (e *Engine) recoverRequiredMissionTool(
	ctx context.Context,
	def *agent.Definition,
	msg message.Message,
	req llm.CompletionRequest,
	toolName string,
	ignoredContent string,
	maxTokens int,
) requiredToolRecoveryResult {
	var schema *llm.ToolSchema
	for i := range req.Tools {
		if normalizeToolCallName(req.Tools[i].Name) == normalizeToolCallName(toolName) {
			schema = &req.Tools[i]
			break
		}
	}
	if schema == nil {
		return requiredToolRecoveryResult{}
	}

	schemaJSON, _ := json.Marshal(schema.Parameters)
	msgs := append([]llm.ChatMessage(nil), req.Messages...)
	if strings.TrimSpace(ignoredContent) != "" {
		msgs = append(msgs, llm.ChatMessage{Role: "assistant", Content: ignoredContent})
	}
	msgs = append(msgs, llm.ChatMessage{Role: "system", Content: fmt.Sprintf(
		"The required function call was not emitted. Return only one JSON object containing the arguments for function %q. Use only facts and identifiers already present in the conversation. Do not explain, use markdown, invent credentials, or perform the task in prose. Argument schema: %s",
		schema.Name, schemaJSON,
	)})

	model := def.LLM.Model
	modelLabel := model
	if modelLabel == "" {
		modelLabel = "(provider default)"
	}
	providerLabel := def.LLM.Provider
	if providerLabel == "" {
		providerLabel = "(default)"
	}
	e.sink.Emit(message.Event{
		Type: "llm.call", AgentID: msg.AgentID, SessionID: msg.SessionID,
		Payload: map[string]any{
			"provider": providerLabel, "model": modelLabel,
			"turn": "required-tool-arguments", "required_tool": schema.Name,
		},
		Timestamp: time.Now().UTC(),
	})

	started := time.Now()
	recoveryCtx, cancel := context.WithTimeout(ctx, e.effectiveLLMTimeout())
	defer cancel()
	resp, err := e.llmRouter.Complete(recoveryCtx, def.LLM.Provider, llm.CompletionRequest{
		Model:             model,
		Messages:          msgs,
		Temperature:       0,
		MaxTokens:         maxTokens,
		ResponseFormat:    "json_schema",
		JSONSchema:        schema.Parameters,
		JSONSchemaLenient: true,
		ReasoningEffort:   def.LLM.ReasoningEffort,
	})
	result := requiredToolRecoveryResult{Calls: 1}
	metrics.LLMCallDuration.WithLabelValues(providerLabel, modelLabel).Observe(time.Since(started).Seconds())
	if err != nil || resp == nil {
		metrics.LLMCallsTotal.WithLabelValues(providerLabel, modelLabel, "error").Inc()
		detail := "provider returned no response"
		if err != nil {
			detail = err.Error()
		}
		e.emitRequiredToolRecoveryWarning(msg, schema.Name, detail)
		return result
	}

	metrics.LLMCallsTotal.WithLabelValues(providerLabel, modelLabel, "success").Inc()
	if resp.InputTokens > 0 {
		metrics.LLMInputTokens.WithLabelValues(providerLabel, modelLabel).Add(float64(resp.InputTokens))
	}
	if resp.OutputTokens > 0 {
		metrics.LLMOutputTokens.WithLabelValues(providerLabel, modelLabel).Add(float64(resp.OutputTokens))
	}
	e.recordUsage(ctx, msg.AgentID, msg.SessionID, providerLabel, modelLabel, resp.InputTokens, resp.OutputTokens)
	result.Tokens = resp.InputTokens + resp.OutputTokens + resp.ReasoningTokens + resp.ToolUsePromptTokens
	e.sink.Emit(message.Event{
		Type: "llm.result", AgentID: msg.AgentID, SessionID: msg.SessionID,
		Payload: map[string]any{
			"model": modelLabel, "input_tokens": resp.InputTokens,
			"output_tokens": resp.OutputTokens, "duration_ms": time.Since(started).Milliseconds(),
			"tool_calls": 0, "stage": "required-tool-arguments",
		},
		Timestamp: time.Now().UTC(),
	})

	parsed, parseErr := parseJSONLoose(resp.Content)
	args, ok := parsed.(map[string]any)
	if parseErr != nil || !ok {
		detail := "response was not a JSON object"
		if parseErr != nil {
			detail = parseErr.Error()
		}
		e.emitRequiredToolRecoveryWarning(msg, schema.Name, detail)
		return result
	}
	if err := validateRecoveredToolArguments(schema.Parameters, args); err != nil {
		e.emitRequiredToolRecoveryWarning(msg, schema.Name, err.Error())
		return result
	}
	result.Call = &message.ToolCall{ID: "mission-recovery-" + uuidShort(), Name: schema.Name, Arguments: args}
	return result
}

func (e *Engine) emitRequiredToolRecoveryWarning(msg message.Message, toolName, detail string) {
	e.sink.Emit(message.Event{
		Type: "warn", AgentID: msg.AgentID, SessionID: msg.SessionID,
		Payload: map[string]any{
			"stage": "required-tool-recovery", "required_tool": toolName,
			"error": "could not derive schema-valid arguments: " + detail,
		},
		Timestamp: time.Now().UTC(),
	})
}

func validateRecoveredToolArguments(schema map[string]any, args map[string]any) error {
	if schema == nil {
		return nil
	}
	for _, raw := range schemaValues(schema["required"]) {
		name, _ := raw.(string)
		if name == "" {
			continue
		}
		if value, exists := args[name]; !exists || value == nil {
			return fmt.Errorf("missing required argument %q", name)
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	for name, value := range args {
		rawProperty, exists := properties[name]
		if !exists {
			if allow, specified := schema["additionalProperties"].(bool); specified && !allow {
				return fmt.Errorf("argument %q is not allowed", name)
			}
			continue
		}
		property, _ := rawProperty.(map[string]any)
		if err := validateRecoveredSchemaValue(property, value, name); err != nil {
			return err
		}
	}
	return nil
}

func validateRecoveredSchemaValue(schema map[string]any, value any, path string) error {
	if len(schema) == 0 {
		return nil
	}
	if variants := schemaValues(schema["oneOf"]); len(variants) > 0 {
		for _, raw := range variants {
			variant, _ := raw.(map[string]any)
			if validateRecoveredSchemaValue(variant, value, path) == nil {
				return nil
			}
		}
		return fmt.Errorf("argument %q does not match an allowed value", path)
	}
	if expected, exists := schema["const"]; exists && !reflect.DeepEqual(expected, value) {
		return fmt.Errorf("argument %q is not the allowed value", path)
	}
	if values := schemaValues(schema["enum"]); len(values) > 0 {
		matched := false
		for _, expected := range values {
			if reflect.DeepEqual(expected, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("argument %q is not in the allowed set", path)
		}
	}

	typeName, _ := schema["type"].(string)
	valid := true
	switch typeName {
	case "string":
		_, valid = value.(string)
	case "object":
		_, valid = value.(map[string]any)
	case "array":
		_, valid = value.([]any)
	case "boolean":
		_, valid = value.(bool)
	case "number":
		_, valid = value.(float64)
	case "integer":
		n, isNumber := value.(float64)
		valid = isNumber && math.Trunc(n) == n
	}
	if !valid {
		return fmt.Errorf("argument %q must be %s", path, typeName)
	}
	if object, ok := value.(map[string]any); ok {
		if err := validateRecoveredToolArguments(schema, object); err != nil {
			return fmt.Errorf("argument %q: %w", path, err)
		}
	}
	if items, ok := value.([]any); ok {
		itemSchema, _ := schema["items"].(map[string]any)
		for index, item := range items {
			if err := validateRecoveredSchemaValue(itemSchema, item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func schemaValues(value any) []any {
	if value == nil {
		return nil
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil
	}
	values := make([]any, rv.Len())
	for i := range values {
		values[i] = rv.Index(i).Interface()
	}
	return values
}

func requiredToolRecoveryTokenBudget(limit, used int, resp *llm.CompletionResponse) int {
	const capTokens = 2048
	if limit <= 0 {
		return capTokens
	}
	current := 0
	if resp != nil {
		current = resp.InputTokens + resp.OutputTokens + resp.ReasoningTokens + resp.ToolUsePromptTokens
	}
	remaining := limit - used - current
	if remaining <= 0 {
		return 0
	}
	if remaining < capTokens {
		return remaining
	}
	return capTokens
}
