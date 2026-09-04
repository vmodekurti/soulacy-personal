package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestAnthropicCompleteSerializesCachingThinkingAndParsesToolUse(t *testing.T) {
	var got map[string]any
	var beta, apiKey, version string
	provider := NewAnthropicProviderWithOptions("http://anthropic.test", "sk-ant", "claude-test", true, true, 2048)
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %q, want /v1/messages", r.URL.Path)
		}
		beta = r.Header.Get("anthropic-beta")
		apiKey = r.Header.Get("x-api-key")
		version = r.Header.Get("anthropic-version")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return jsonResponse(200, `{
			"content": [
				{"type": "text", "text": "checking"},
				{"type": "tool_use", "id": "toolu_1", "name": "lookup", "input": {"city": "Chicago"}}
			],
			"usage": {
				"input_tokens": 12,
				"output_tokens": 5,
				"cache_creation_input_tokens": 100,
				"cache_read_input_tokens": 50
			}
		}`), nil
	})

	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Temperature: 0.2,
		MaxTokens:   900,
		Messages: []ChatMessage{
			{Role: "system", Content: "system one"},
			{Role: "system", Content: "system two"},
			{Role: "user", Content: "hi"},
			{
				Role:    "assistant",
				Content: "need tool",
				ToolCalls: []message.ToolCall{{
					ID:        "toolu_prev",
					Name:      "lookup",
					Arguments: map[string]any{"city": "Chicago"},
				}},
			},
			{Role: "tool", ToolCallID: "toolu_prev", Name: "lookup", Content: "rain"},
		},
		Tools: []ToolSchema{{
			Name:        "lookup",
			Description: "Lookup weather",
			Parameters:  map[string]any{"type": "object"},
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if apiKey != "sk-ant" || version != "2023-06-01" {
		t.Fatalf("headers apiKey=%q version=%q", apiKey, version)
	}
	if !strings.Contains(beta, "prompt-caching-2024-07-31") || !strings.Contains(beta, "interleaved-thinking-2025-05-14") {
		t.Fatalf("anthropic-beta = %q", beta)
	}
	if got["temperature"].(float64) != 1 {
		t.Fatalf("extended thinking should force temperature 1, got %#v", got["temperature"])
	}
	thinking := got["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"].(float64) != 2048 {
		t.Fatalf("thinking = %#v", thinking)
	}
	system := got["system"].([]any)
	if system[0].(map[string]any)["cache_control"] == nil {
		t.Fatalf("system cache_control missing: %#v", system)
	}
	if !strings.Contains(system[0].(map[string]any)["text"].(string), "system one\n\nsystem two") {
		t.Fatalf("system prompt not merged: %#v", system)
	}
	tools := got["tools"].([]any)
	if tools[len(tools)-1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("tool cache_control missing: %#v", tools)
	}
	messages := got["messages"].([]any)
	toolResultMessage := messages[len(messages)-1].(map[string]any)
	if toolResultMessage["role"] != "user" {
		t.Fatalf("tool result role = %#v", toolResultMessage)
	}
	content := toolResultMessage["content"].([]any)
	if content[0].(map[string]any)["type"] != "tool_result" {
		t.Fatalf("tool result block = %#v", content)
	}

	if resp.Content != "checking" {
		t.Fatalf("content = %q, want checking", resp.Content)
	}
	if resp.InputTokens != 12 || resp.OutputTokens != 5 || resp.CacheCreationTokens != 100 || resp.CacheReadTokens != 50 {
		t.Fatalf("usage = %+v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "toolu_1" || resp.ToolCalls[0].Name != "lookup" {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].Arguments["city"] != "Chicago" {
		t.Fatalf("tool args = %+v", resp.ToolCalls[0].Arguments)
	}
}

func TestAnthropicRequestCanDisablePromptCachingPerCall(t *testing.T) {
	var body map[string]any
	var beta string
	provider := NewAnthropicProviderWithOptions("http://anthropic.test", "sk-ant", "claude-test", true, false, 0)
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		beta = r.Header.Get("anthropic-beta")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return jsonResponse(200, `{"content":[{"type":"text","text":"ok"}],"usage":{}}`), nil
	})
	_, err := provider.Complete(context.Background(), CompletionRequest{
		DisablePromptCaching: true, Messages: []ChatMessage{{Role: "system", Content: "secret"}},
		Tools: []ToolSchema{{Name: "tool", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(beta, "prompt-caching") {
		t.Fatalf("beta=%q", beta)
	}
	if _, ok := body["system"].(string); !ok {
		t.Fatalf("system should be uncached string: %#v", body["system"])
	}
	tools := body["tools"].([]any)
	if _, ok := tools[0].(map[string]any)["cache_control"]; ok {
		t.Fatalf("tools unexpectedly cached: %#v", tools)
	}
}

func TestAnthropicStructuredOutputReturnsForcedToolInputAsContent(t *testing.T) {
	provider := NewAnthropicProvider("http://anthropic.test", "sk-ant", "claude-test")
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got["tool_choice"].(map[string]any)["name"] != "respond" {
			t.Fatalf("tool_choice = %#v", got["tool_choice"])
		}
		return jsonResponse(200, `{
			"content": [
				{"type": "tool_use", "id": "toolu_schema", "name": "respond", "input": {"ok": true}}
			],
			"usage": {}
		}`), nil
	})

	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Messages:       []ChatMessage{{Role: "user", Content: "json"}},
		ResponseFormat: "json_schema",
		JSONSchema:     map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != `{"ok":true}` {
		t.Fatalf("content = %q, want forced tool JSON", resp.Content)
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("schema respond tool should not surface as tool call: %+v", resp.ToolCalls)
	}
}

func TestAnthropicCompleteSanitizesToolNamesAndMapsBack(t *testing.T) {
	var got map[string]any
	provider := NewAnthropicProvider("http://anthropic.test", "sk-ant", "claude-test")
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		tools := got["tools"].([]any)
		if tools[0].(map[string]any)["name"] != "channel_send" {
			t.Fatalf("tool name on wire = %#v, want channel_send", tools[0])
		}
		if got["tool_choice"].(map[string]any)["name"] != "channel_send" {
			t.Fatalf("tool_choice = %#v", got["tool_choice"])
		}
		messages := got["messages"].([]any)
		assistant := messages[1].(map[string]any)
		blocks := assistant["content"].([]any)
		if blocks[0].(map[string]any)["name"] != "channel_send" {
			t.Fatalf("assistant tool_use name = %#v", blocks[0])
		}
		return jsonResponse(200, `{
			"content": [
				{"type": "tool_use", "id": "toolu_2", "name": "channel_send", "input": {"channel": "telegram", "text": "ok"}}
			],
			"usage": {}
		}`), nil
	})

	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "send it"},
			{
				Role: "assistant",
				ToolCalls: []message.ToolCall{{
					ID:        "toolu_1",
					Name:      "channel.send",
					Arguments: map[string]any{"channel": "telegram", "text": "previous"},
				}},
			},
			{Role: "tool", ToolCallID: "toolu_1", Name: "channel.send", Content: "sent"},
		},
		Tools: []ToolSchema{{
			Name:        "channel.send",
			Description: "Send to a channel",
			Parameters:  map[string]any{"type": "object"},
		}},
		ToolChoice: "channel.send",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].Name != "channel.send" {
		t.Fatalf("tool name mapped back = %q", resp.ToolCalls[0].Name)
	}
	if resp.ToolCalls[0].Arguments["text"] != "ok" {
		t.Fatalf("tool args = %+v", resp.ToolCalls[0].Arguments)
	}
}

func TestAnthropicCompleteRetriesWithoutDeprecatedSamplingParams(t *testing.T) {
	var calls int
	provider := NewAnthropicProvider("http://anthropic.test", "sk-ant", "claude-test")
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if calls == 1 {
			if _, ok := got["temperature"]; !ok {
				t.Fatal("first request should include temperature")
			}
			if _, ok := got["top_p"]; ok {
				t.Fatalf("temperature and top_p must not be sent together: %#v", got)
			}
			return jsonResponse(400, `{"type":"error","error":{"type":"invalid_request_error","message":"temperature is deprecated for this model."}}`), nil
		}
		if _, ok := got["temperature"]; ok {
			t.Fatalf("retry should omit deprecated temperature: %#v", got)
		}
		if _, ok := got["top_p"]; ok {
			t.Fatalf("retry should not restore top_p after preferring temperature: %#v", got)
		}
		return jsonResponse(200, `{
			"content": [{"type": "text", "text": "ok"}],
			"usage": {"input_tokens": 1, "output_tokens": 1}
		}`), nil
	})

	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Messages:    []ChatMessage{{Role: "user", Content: "hi"}},
		Temperature: 0.7,
		TopP:        0.9,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q", resp.Content)
	}
}

func TestAnthropicCompleteOmitsSamplingForClaudeFiveAliases(t *testing.T) {
	var calls int
	provider := NewAnthropicProvider("http://anthropic.test", "sk-ant", "claude-sonnet-5")
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, ok := got["temperature"]; ok {
			t.Fatalf("temperature should be omitted for sampling-free Anthropic models: %#v", got)
		}
		if _, ok := got["top_p"]; ok {
			t.Fatalf("top_p should be omitted for sampling-free Anthropic models: %#v", got)
		}
		return jsonResponse(200, `{
			"content": [{"type": "text", "text": "ok"}],
			"usage": {"input_tokens": 1, "output_tokens": 1}
		}`), nil
	})

	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Messages:    []ChatMessage{{Role: "user", Content: "hi"}},
		Temperature: 0.7,
		TopP:        0.9,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q", resp.Content)
	}
}

func TestAnthropicCompleteUsesTopPWhenTemperatureUnsetAndRetriesIfDeprecated(t *testing.T) {
	var calls int
	provider := NewAnthropicProvider("http://anthropic.test", "sk-ant", "claude-test")
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if calls == 1 {
			if _, ok := got["temperature"]; ok {
				t.Fatalf("temperature should be omitted when only top_p is tuned: %#v", got)
			}
			if _, ok := got["top_p"]; !ok {
				t.Fatal("first request should include top_p")
			}
			return jsonResponse(400, `{"type":"error","error":{"type":"invalid_request_error","message":"top_p is deprecated for this model."}}`), nil
		}
		if _, ok := got["temperature"]; ok {
			t.Fatalf("retry should still omit temperature: %#v", got)
		}
		if _, ok := got["top_p"]; ok {
			t.Fatalf("retry should omit deprecated top_p: %#v", got)
		}
		return jsonResponse(200, `{
			"content": [{"type": "text", "text": "ok"}],
			"usage": {"input_tokens": 1, "output_tokens": 1}
		}`), nil
	})

	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
		TopP:     0.9,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q", resp.Content)
	}
}

func TestAnthropicCompleteRetriesWithoutUnsupportedTopP(t *testing.T) {
	var calls int
	provider := NewAnthropicProvider("http://anthropic.test", "sk-ant", "claude-test")
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if calls == 1 {
			if _, ok := got["top_p"]; !ok {
				t.Fatal("first request should include top_p")
			}
			return jsonResponse(400, `{"type":"error","error":{"type":"invalid_request_error","message":"top_p is not supported for this model."}}`), nil
		}
		if _, ok := got["top_p"]; ok {
			t.Fatalf("retry should omit unsupported top_p: %#v", got)
		}
		return jsonResponse(200, `{
			"content": [{"type": "text", "text": "ok"}],
			"usage": {"input_tokens": 1, "output_tokens": 1}
		}`), nil
	})

	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
		TopP:     0.9,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q", resp.Content)
	}
}

func TestAnthropicRetriableParamPrefersTopPForCannotBoth(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"temperature and top_p cannot both be specified for this model. Please use only one."}}`)
	if got := anthropicRetriableParam(body); got != "top_p" {
		t.Fatalf("anthropicRetriableParam = %q, want top_p", got)
	}
}
