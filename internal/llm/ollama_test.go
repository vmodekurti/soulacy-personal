package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestOllamaCompleteSerializesOptionsToolsAndParsesToolCalls(t *testing.T) {
	var got map[string]any
	provider := NewOllamaProvider("http://ollama.test", "llama3", "10m", map[string]any{"num_ctx": 8192})
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("path = %q, want /api/chat", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return jsonResponse(200, `{
			"message": {
				"role": "assistant",
				"content": "",
				"tool_calls": [
					{"function": {"name": "lookup", "arguments": {"city": "Chicago"}}}
				]
			},
			"prompt_eval_count": 11,
			"eval_count": 3
		}`), nil
	})

	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Model:          "qwen",
		Temperature:    0.4,
		MaxTokens:      123,
		ResponseFormat: "json_schema",
		JSONSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ok": map[string]any{"type": "boolean"},
			},
		},
		ToolChoice: "lookup",
		Messages: []ChatMessage{
			{Role: "system", Content: "be useful"},
			{Role: "user", Content: "weather"},
			{
				Role: "assistant",
				ToolCalls: []message.ToolCall{{
					ID:        "call_1",
					Name:      "lookup",
					Arguments: map[string]any{"city": "Chicago"},
				}},
			},
			{Role: "tool", Name: "lookup", Content: "rain", ToolCallID: "call_1"},
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

	if got["model"] != "qwen" {
		t.Fatalf("model = %v, want qwen", got["model"])
	}
	if got["keep_alive"] != "10m" {
		t.Fatalf("keep_alive = %v, want 10m", got["keep_alive"])
	}
	options := got["options"].(map[string]any)
	if options["num_ctx"].(float64) != 8192 || options["num_batch"].(float64) != 128 {
		t.Fatalf("options = %#v", options)
	}
	if options["temperature"].(float64) != 0.4 || options["num_predict"].(float64) != 123 {
		t.Fatalf("runtime options = %#v", options)
	}
	if _, ok := got["format"].(map[string]any); !ok {
		t.Fatalf("format should be schema map, got %#v", got["format"])
	}
	toolChoice := got["tool_choice"].(map[string]any)
	if toolChoice["function"].(map[string]any)["name"] != "lookup" {
		t.Fatalf("tool_choice = %#v", toolChoice)
	}
	messages := got["messages"].([]any)
	if messages[2].(map[string]any)["tool_calls"] == nil {
		t.Fatalf("assistant tool_calls not serialized: %#v", messages[2])
	}
	if messages[3].(map[string]any)["tool_name"] != "lookup" {
		t.Fatalf("tool message missing tool_name: %#v", messages[3])
	}

	if resp.InputTokens != 11 || resp.OutputTokens != 3 {
		t.Fatalf("usage = %d/%d, want 11/3", resp.InputTokens, resp.OutputTokens)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "lookup" {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].Arguments["city"] != "Chicago" {
		t.Fatalf("tool args = %+v", resp.ToolCalls[0].Arguments)
	}
}

func TestOllamaCompleteStreamsTextWhenNoTools(t *testing.T) {
	var stream bool
	provider := NewOllamaProvider("http://ollama.test", "llama3", "", nil)
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		stream = body["stream"].(bool)
		return textResponse(200,
			`{"message":{"content":"hello"},"done":false}`+"\n"+
				`{"message":{"content":" world"},"done":false}`+"\n"+
				`{"done":true,"prompt_eval_count":9,"eval_count":2}`+"\n",
		), nil
	})

	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Stream:   true,
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	var out string
	for token := range resp.Stream {
		out += token
	}
	if !stream {
		t.Fatal("request stream flag was not true")
	}
	if out != "hello world" {
		t.Fatalf("stream output = %q, want hello world", out)
	}
	if resp.InputTokens != 9 || resp.OutputTokens != 2 || resp.TotalTokens != 11 {
		t.Fatalf("stream usage = %d/%d/%d, want 9/2/11", resp.InputTokens, resp.OutputTokens, resp.TotalTokens)
	}
}

func TestOpenAICompleteSerializesToolStateAndParsesToolCalls(t *testing.T) {
	parallel := false
	var got map[string]any
	var auth, org string
	provider := NewOpenAIProviderWithOptions("openai", "http://openai.test", "sk-test", "gpt-default", "org-1", &parallel)
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		org = r.Header.Get("OpenAI-Organization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return jsonResponse(200, `{
			"choices": [{
				"message": {
					"content": "",
					"tool_calls": [{
						"id": "call_openai_1",
						"thought_signature": "sig-from-router",
						"function": {
							"name": "lookup",
							"arguments": "{\"city\":\"Chicago\"}"
						}
					}]
				}
			}],
			"usage": {"prompt_tokens": 9, "completion_tokens": 4}
		}`), nil
	})

	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Model:          "gpt-test",
		Temperature:    0.1,
		MaxTokens:      200,
		ToolChoice:     "required",
		ResponseFormat: "json_schema",
		JSONSchema:     map[string]any{"type": "object"},
		Messages: []ChatMessage{
			{Role: "user", Content: "hi"},
			{
				Role: "assistant",
				ToolCalls: []message.ToolCall{{
					ID:               "call_1",
					Name:             "lookup",
					Arguments:        map[string]any{"city": "Chicago"},
					ThoughtSignature: "sig-prev",
				}},
			},
			{Role: "tool", ToolCallID: "call_1", Name: "lookup", Content: "rain"},
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

	if auth != "Bearer sk-test" || org != "org-1" {
		t.Fatalf("headers auth=%q org=%q", auth, org)
	}
	if got["model"] != "gpt-test" || got["parallel_tool_calls"] != false {
		t.Fatalf("model/parallel = %#v", got)
	}
	if got["stream"] != false {
		t.Fatalf("stream = %#v, want false", got["stream"])
	}
	if got["tool_choice"] != "required" {
		t.Fatalf("tool_choice = %#v", got["tool_choice"])
	}
	if got["response_format"].(map[string]any)["type"] != "json_schema" {
		t.Fatalf("response_format = %#v", got["response_format"])
	}
	messages := got["messages"].([]any)
	toolCalls := messages[1].(map[string]any)["tool_calls"].([]any)
	if toolCalls == nil {
		t.Fatalf("assistant tool_calls not serialized: %#v", messages[1])
	}
	serializedCall := toolCalls[0].(map[string]any)
	if serializedCall["thought_signature"] != "sig-prev" || serializedCall["thoughtSignature"] != "sig-prev" {
		t.Fatalf("assistant tool_call missing thought signature: %#v", serializedCall)
	}
	if messages[2].(map[string]any)["tool_call_id"] != "call_1" {
		t.Fatalf("tool message missing tool_call_id: %#v", messages[2])
	}

	if resp.InputTokens != 9 || resp.OutputTokens != 4 {
		t.Fatalf("usage = %d/%d, want 9/4", resp.InputTokens, resp.OutputTokens)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_openai_1" || resp.ToolCalls[0].Name != "lookup" {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].Arguments["city"] != "Chicago" {
		t.Fatalf("tool args = %+v", resp.ToolCalls[0].Arguments)
	}
	if resp.ToolCalls[0].ThoughtSignature != "sig-from-router" {
		t.Fatalf("thought signature = %q", resp.ToolCalls[0].ThoughtSignature)
	}
}

func TestOpenAICompleteStreamsSSEWhenNoTools(t *testing.T) {
	var accept string
	provider := NewOpenAIProvider("openai", "http://openai.test", "", "gpt")
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		accept = r.Header.Get("Accept")
		return textResponse(200,
			`data: {"choices":[{"delta":{"content":"one"}}]}`+"\n\n"+
				`data: {"choices":[{"delta":{"content":" two"}}]}`+"\n\n"+
				`data: {"choices":[],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10,"completion_tokens_details":{"reasoning_tokens":1}}}`+"\n\n"+
				`data: [DONE]`+"\n",
		), nil
	})

	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Stream:   true,
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	var out string
	for token := range resp.Stream {
		out += token
	}
	if accept != "text/event-stream" {
		t.Fatalf("Accept = %q, want text/event-stream", accept)
	}
	if out != "one two" {
		t.Fatalf("stream output = %q, want one two", out)
	}
	if resp.InputTokens != 8 || resp.OutputTokens != 2 || resp.TotalTokens != 10 || resp.ReasoningTokens != 1 {
		t.Fatalf("stream usage = %+v", resp)
	}
}

func TestOpenAICompleteSerializesGeminiToolCalls(t *testing.T) {
	var got map[string]any
	provider := NewOpenAIProvider("openroute", "http://openroute.test/v1", "sk-test", "gemini/gemini-3.1-pro-preview")
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return jsonResponse(200, `{
			"choices": [{"message": {"content": "ok"}}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 1}
		}`), nil
	})

	_, err := provider.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "weather"}},
		Tools: []ToolSchema{{
			Name:        "lookup",
			Description: "Lookup weather",
			Parameters:  map[string]any{"type": "object"},
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got["parallel_tool_calls"] != false {
		t.Fatalf("parallel_tool_calls = %#v, want false for Gemini tool use", got["parallel_tool_calls"])
	}
}

func TestOpenAICompleteKeepsOneGeminiToolCall(t *testing.T) {
	provider := NewOpenAIProvider("openroute", "http://openroute.test/v1", "sk-test", "gemini/gemini-3.1-pro-preview")
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{
			"choices": [{
				"message": {
					"content": "",
					"tool_calls": [
						{"id": "call_1", "function": {"name": "forecast", "arguments": "{\"city\":\"Chicago\"}"}},
						{"id": "call_2", "function": {"name": "alerts", "arguments": "{\"city\":\"Chicago\"}"}}
					]
				}
			}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 1}
		}`), nil
	})

	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "weather"}},
		Tools: []ToolSchema{{
			Name:        "forecast",
			Description: "Lookup forecast",
			Parameters:  map[string]any{"type": "object"},
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool calls = %+v, want only the first Gemini tool call", resp.ToolCalls)
	}
}

// TestOllamaRecoversThinkingWhenContentEmpty reproduces the qwen3-coder bug:
// a reasoning model spends its whole turn inside its reasoning channel and
// returns EMPTY message.content while putting the final answer in
// message.thinking. The parser must surface that answer into Content so the
// engine doesn't discard a completed run.
func TestOllamaRecoversThinkingWhenContentEmpty(t *testing.T) {
	provider := NewOllamaProvider("http://ollama.test", "qwen3-coder", "", nil)
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{
			"message": {
				"role": "assistant",
				"content": "",
				"thinking": "Let me reason... <think>internal</think>\nThe final report: all systems nominal."
			},
			"prompt_eval_count": 100,
			"eval_count": 758
		}`), nil
	})

	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Model:    "qwen3-coder",
		Messages: []ChatMessage{{Role: "user", Content: "status?"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.OutputTokens != 758 {
		t.Fatalf("output tokens = %d, want 758", resp.OutputTokens)
	}
	if strings.TrimSpace(resp.Content) == "" {
		t.Fatal("content empty: thinking-field answer was not recovered")
	}
	if !strings.Contains(resp.Content, "all systems nominal") {
		t.Fatalf("content = %q, want recovered post-think answer", resp.Content)
	}
	if strings.Contains(resp.Content, "<think>") || strings.Contains(resp.Content, "internal") {
		t.Fatalf("content still contains think block: %q", resp.Content)
	}
}

// TestOllamaStripsInlineThinkBlock covers content that carries an inline
// <think>…</think> block followed by the real answer.
func TestOllamaStripsInlineThinkBlock(t *testing.T) {
	provider := NewOllamaProvider("http://ollama.test", "qwen3", "", nil)
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{
			"message": {"role": "assistant", "content": "<think>weighing options</think>\nFinal answer here."},
			"prompt_eval_count": 10,
			"eval_count": 20
		}`), nil
	})
	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "go"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "Final answer here." {
		t.Fatalf("content = %q, want %q", resp.Content, "Final answer here.")
	}
}

func TestStripThinkBlocks(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"<think>x</think>answer", "answer"},
		{"pre <think>x</think> post", "pre  post"},
		{"only <think>reasoning with no close", "only"},
		{"trailing </think>tail", "tail"},
		{"<think>a</think><think>b</think>done", "done"},
	}
	for _, c := range cases {
		if got := stripThinkBlocks(c.in); got != c.want {
			t.Errorf("stripThinkBlocks(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func clientWithRoundTripper(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: fn}
}

func jsonResponse(status int, body string) *http.Response {
	resp := textResponse(status, body)
	resp.Header.Set("Content-Type", "application/json")
	return resp
}

func textResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
