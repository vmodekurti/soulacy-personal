// anthropic.go — Anthropic Messages API provider (Claude).
//
// Anthropic's /v1/messages is shaped differently from OpenAI's chat/completions:
//
//   - The system prompt is a top-level `system` field, not a message with
//     role:"system".
//   - Roles are only "user" and "assistant" — tool results are encoded as a
//     user-role message containing a `tool_result` content block keyed by id.
//   - Tool calls arrive as `tool_use` content blocks inside an assistant
//     message (alongside any text blocks).
//   - Streaming uses the `messages` event stream; we use the non-streaming
//     mode here for simplicity (the engine doesn't stream yet).
//
// Spec: https://docs.anthropic.com/en/api/messages
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

// AnthropicProvider talks to api.anthropic.com (or a compatible base URL).
type AnthropicProvider struct {
	baseURL          string // default https://api.anthropic.com
	apiKey           string
	model            string
	version          string // anthropic-version header (default "2023-06-01")
	promptCaching    bool   // marks system prompt and tools for caching (90% off cache hits)
	extendedThinking bool   // Claude 3.7+ extended thinking (beta)
	thinkingBudget   int    // token budget for extended thinking (default 8192)
	client           *http.Client
}

func NewAnthropicProvider(baseURL, apiKey, model string) *AnthropicProvider {
	return newAnthropicProvider(baseURL, apiKey, model, false, false, 0)
}

func NewAnthropicProviderWithCaching(baseURL, apiKey, model string) *AnthropicProvider {
	return newAnthropicProvider(baseURL, apiKey, model, true, false, 0)
}

// NewAnthropicProviderWithOptions creates an AnthropicProvider with all settings.
func NewAnthropicProviderWithOptions(baseURL, apiKey, model string, promptCaching, extendedThinking bool, thinkingBudget int) *AnthropicProvider {
	return newAnthropicProvider(baseURL, apiKey, model, promptCaching, extendedThinking, thinkingBudget)
}

func newAnthropicProvider(baseURL, apiKey, model string, promptCaching, extendedThinking bool, thinkingBudget int) *AnthropicProvider {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	if model == "" {
		model = "claude-3-5-sonnet-latest"
	}
	if extendedThinking && thinkingBudget <= 0 {
		thinkingBudget = 8192 // sensible default
	}
	return &AnthropicProvider{
		baseURL:          baseURL,
		apiKey:           apiKey,
		model:            model,
		version:          "2023-06-01",
		promptCaching:    promptCaching,
		extendedThinking: extendedThinking,
		thinkingBudget:   thinkingBudget,
		client:           SharedHTTPClient(180 * time.Second),
	}
}

func (p *AnthropicProvider) ID() string { return "anthropic" }

func (p *AnthropicProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	cachePrompt := p.promptCaching && !req.DisablePromptCaching
	model := req.Model
	if model == "" {
		model = p.model
	}
	toolWireNames, toolOriginalNames := anthropicToolNameMaps(req.Tools)

	// Translate our flat ChatMessage list into Anthropic's
	// (system, [user/assistant content-block messages]) shape.
	var systemPrompt string
	msgs := make([]map[string]any, 0, len(req.Messages))

	flushPending := func(pending *map[string]any) {
		if pending != nil && *pending != nil {
			msgs = append(msgs, *pending)
			*pending = nil
		}
	}

	// We need to merge consecutive tool-result messages into a single user
	// message (Anthropic only allows one tool_result block per id, and groups
	// them together in one user message).
	var currentUser map[string]any

	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			if systemPrompt != "" {
				systemPrompt += "\n\n"
			}
			systemPrompt += m.Content
		case "user":
			flushPending(&currentUser)
			msgs = append(msgs, map[string]any{
				"role":    "user",
				"content": []map[string]any{{"type": "text", "text": m.Content}},
			})
		case "assistant":
			flushPending(&currentUser)
			blocks := []map[string]any{}
			if m.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				name := tc.Name
				if wire, ok := toolWireNames[tc.Name]; ok {
					name = wire
				}
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  name,
					"input": tc.Arguments,
				})
			}
			msgs = append(msgs, map[string]any{
				"role":    "assistant",
				"content": blocks,
			})
		case "tool":
			// tool results go into a single user-role message, batched together
			if currentUser == nil {
				currentUser = map[string]any{
					"role":    "user",
					"content": []map[string]any{},
				}
			}
			currentUser["content"] = append(currentUser["content"].([]map[string]any), map[string]any{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     m.Content,
			})
		}
	}
	flushPending(&currentUser)

	body := map[string]any{
		"model":      model,
		"messages":   msgs,
		"max_tokens": defaultIfZero(req.MaxTokens, 4096),
	}
	// Extended thinking requires temperature=1 (Anthropic API requirement).
	if p.extendedThinking {
		body["temperature"] = 1
		body["thinking"] = map[string]any{
			"type":          "enabled",
			"budget_tokens": p.thinkingBudget,
		}
	} else if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}
	if anthropicModelDisallowsSampling(model) && !p.extendedThinking {
		delete(body, "temperature")
	} else if req.TopP > 0 && body["temperature"] == nil {
		body["top_p"] = req.TopP
	}

	// System prompt — when caching is enabled, send as a content-block array
	// with cache_control on the last block so Anthropic caches it between turns
	// (90% discount on cache hits, 1.25× cost on the first write).
	if systemPrompt != "" {
		if cachePrompt {
			body["system"] = []map[string]any{
				{
					"type":          "text",
					"text":          systemPrompt,
					"cache_control": map[string]any{"type": "ephemeral"},
				},
			}
		} else {
			body["system"] = systemPrompt
		}
	}

	// Tools — when caching is enabled, mark the last tool definition so the
	// entire tool block is cached. Tool schemas rarely change between turns and
	// are often large (MCP tools, JSON schemas), making this a significant win.
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			name := t.Name
			if wire, ok := toolWireNames[t.Name]; ok {
				name = wire
			}
			tools[i] = map[string]any{
				"name":         name,
				"description":  t.Description,
				"input_schema": t.Parameters,
			}
		}
		if cachePrompt {
			tools[len(tools)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
		}
		body["tools"] = tools
		switch req.ToolChoice {
		case "required":
			body["tool_choice"] = map[string]any{"type": "any"}
		case "auto":
			body["tool_choice"] = map[string]any{"type": "auto"}
		case "none", "":
			// Omit: Anthropic defaults to auto; older API versions may not
			// support an explicit "none" tool_choice.
		default:
			name := anthropicSafeToolName(req.ToolChoice)
			if wire, ok := toolWireNames[req.ToolChoice]; ok {
				name = wire
			}
			body["tool_choice"] = map[string]any{"type": "tool", "name": name}
		}
	}

	// Structured outputs: Anthropic doesn't have a native response_format, but
	// the supported pattern is to declare a single tool that returns the schema
	// and force the model to call it. We expose this only when JSONSchema is
	// supplied; pure "json" mode falls back to a system-prompt nudge added by
	// the engine.
	if req.ResponseFormat == "json_schema" && req.JSONSchema != nil {
		structTool := map[string]any{
			"name":         "respond",
			"description":  "Return the final response in the required structured format.",
			"input_schema": req.JSONSchema,
		}
		// Append to existing tools so the model still sees the others.
		existing, _ := body["tools"].([]map[string]any)
		body["tools"] = append(existing, structTool)
		body["tool_choice"] = map[string]any{"type": "tool", "name": "respond"}
	}

	// Beta headers: combine as comma-separated list when multiple are needed.
	var betas []string
	if cachePrompt {
		betas = append(betas, "prompt-caching-2024-07-31")
	}
	if p.extendedThinking {
		betas = append(betas, "interleaved-thinking-2025-05-14")
	}

	strippedDeprecated := map[string]bool{}
	var resp *http.Response
	for {
		var err error
		resp, err = p.sendMessagesBody(ctx, body, betas)
		if err != nil {
			return nil, fmt.Errorf("anthropic: request failed: %w", err)
		}
		if resp.StatusCode < 300 {
			defer resp.Body.Close()
			break
		}
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		param := anthropicRetriableParam(bodyBytes)
		if param == "" || strippedDeprecated[param] {
			return nil, fmt.Errorf("anthropic: http %d: %s", resp.StatusCode, string(bodyBytes))
		}
		delete(body, param)
		strippedDeprecated[param] = true
	}

	var result struct {
		Content []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("anthropic: decode: %w", err)
	}

	r := &CompletionResponse{
		FinishReason:        result.StopReason,
		InputTokens:         result.Usage.InputTokens,
		OutputTokens:        result.Usage.OutputTokens,
		CacheCreationTokens: result.Usage.CacheCreationInputTokens,
		CacheReadTokens:     result.Usage.CacheReadInputTokens,
		TotalTokens:         result.Usage.InputTokens + result.Usage.OutputTokens,
		ProviderRequestID:   firstNonEmpty(resp.Header.Get("request-id"), resp.Header.Get("x-request-id")),
	}
	for _, block := range result.Content {
		switch block.Type {
		case "text":
			if r.Content != "" {
				r.Content += "\n"
			}
			r.Content += block.Text
		case "tool_use":
			// If this is the forced-schema tool, surface its input as JSON content
			// (the engine treats it as the structured final answer).
			if req.ResponseFormat == "json_schema" && block.Name == "respond" {
				if js, err := json.Marshal(block.Input); err == nil {
					r.Content = string(js)
				}
				continue
			}
			name := block.Name
			if original, ok := toolOriginalNames[block.Name]; ok {
				name = original
			}
			r.ToolCalls = append(r.ToolCalls, message.ToolCall{
				ID:        block.ID,
				Name:      name,
				Arguments: block.Input,
			})
		}
	}

	return r, nil
}

func (p *AnthropicProvider) sendMessagesBody(ctx context.Context, body map[string]any, betas []string) (*http.Response, error) {
	payload, _ := json.Marshal(body)
	httpReq, err := p.newMessagesRequest(ctx, payload, betas)
	if err != nil {
		return nil, err
	}
	return p.doMessages(ctx, httpReq)
}

func (p *AnthropicProvider) newMessagesRequest(ctx context.Context, payload []byte, betas []string) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", p.version)
	if len(betas) > 0 {
		httpReq.Header.Set("anthropic-beta", strings.Join(betas, ","))
	}
	return httpReq, nil
}

func (p *AnthropicProvider) doMessages(ctx context.Context, req *http.Request) (*http.Response, error) {
	// Retry on transient errors (429 / 5xx / network). The request body is a
	// bytes.Reader so net/http auto-rewinds on retry.
	return DoWithRetry(ctx, p.client, req, RetryConfig{})
}

// Models queries Anthropic's /v1/models endpoint when an API key is set,
// falling back to a baked-in current list on error. (PRODUCTION_AUDIT →
// LOW/LLM: previously the list was always the baked-in one and would drift
// behind Anthropic's actual offering — this picks up new models automatically
// for keys that have access.)
//
// The baked-in list stays as a safety net for offline/mocked deployments
// and for the moment in startup before the request completes.
var anthropicBakedInModels = []string{
	"claude-opus-4-5",
	"claude-sonnet-4-5",
	"claude-haiku-4-5",
	"claude-3-7-sonnet-latest",
	"claude-3-5-sonnet-latest",
	"claude-3-5-haiku-latest",
	"claude-3-opus-latest",
}

// Models queries Anthropic's /v1/models endpoint when a key is configured, or
// returns the baked-in fallback list when no key is set.
//
// E4 (Cohort E — Providers page failure handling): previously this method
// swallowed every non-2xx response and every transport error into a silent
// fallback, which meant Providers → Test Connection reported "Reachable ✓"
// even with a rotated key or a wrong base URL. That masked the exact failure
// mode E4 exists to surface. Now:
//
//   - No key configured (local/test setups): return the baked-in fallback so
//     the model picker still shows options, no error.
//   - Key set, request fails: return the raw provider error so
//     llm.ClassifyProviderError can rewrite it into a friendly reason (bad
//     key, network, region blocked, etc.) — the operator finds out.
//
// Transport-level errors and 5xx responses do NOT fall back to the baked-in
// list — the caller needs to know the account/key is unreachable.
func (p *AnthropicProvider) Models(ctx context.Context) ([]string, error) {
	if p.apiKey == "" {
		return anthropicBakedInModels, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("anthropic: build /models request: %w", err)
	}
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", p.version)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: /models request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic: /models returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		// Decode failures against a 2xx are a shape mismatch (e.g. Anthropic
		// changed the response envelope). Fall back to the baked-in list so the
		// picker still works, but log-warn via the error path so it's visible
		// in the Providers doctor.
		return anthropicBakedInModels, nil
	}
	if len(out.Data) == 0 {
		return anthropicBakedInModels, nil
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

func defaultIfZero(v, dflt int) int {
	if v <= 0 {
		return dflt
	}
	return v
}

func anthropicToolNameMaps(tools []ToolSchema) (map[string]string, map[string]string) {
	origToWire := map[string]string{}
	wireToOrig := map[string]string{}
	used := map[string]int{}
	for _, t := range tools {
		wire := anthropicSafeToolName(t.Name)
		if n := used[wire]; n > 0 {
			used[wire] = n + 1
			suffix := fmt.Sprintf("_%d", n+1)
			maxBase := 128 - len(suffix)
			if maxBase < 1 {
				maxBase = 1
			}
			if len(wire) > maxBase {
				wire = wire[:maxBase]
			}
			wire += suffix
		} else {
			used[wire] = 1
		}
		origToWire[t.Name] = wire
		wireToOrig[wire] = t.Name
	}
	return origToWire, wireToOrig
}

func anthropicSafeToolName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "tool"
	}
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 128 {
			break
		}
	}
	out := strings.Trim(b.String(), "_-")
	if out == "" {
		return "tool"
	}
	if len(out) > 128 {
		out = out[:128]
	}
	return out
}

func anthropicRetriableParam(body []byte) string {
	msg := strings.ToLower(string(body))
	if !(strings.Contains(msg, "deprecated") ||
		strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "unsupported") ||
		strings.Contains(msg, "unrecognized") ||
		strings.Contains(msg, "unknown") ||
		strings.Contains(msg, "cannot both") ||
		strings.Contains(msg, "invalid_request_error") ||
		strings.Contains(msg, "bad_request")) {
		return ""
	}
	for _, name := range []string{"top_p", "temperature"} {
		if strings.Contains(msg, name) {
			return name
		}
	}
	return ""
}

func anthropicModelDisallowsSampling(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	// Anthropic has started deprecating sampling controls on newer Claude
	// families. Avoid sending them proactively for forward-versioned aliases so
	// users don't see a recoverable 400 before the retry path strips the field.
	for _, marker := range []string{
		"claude-sonnet-5",
		"claude-opus-5",
		"claude-haiku-5",
		"claude-5",
	} {
		if strings.Contains(m, marker) {
			return true
		}
	}
	return false
}
