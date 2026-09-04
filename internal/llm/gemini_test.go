package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestGeminiCompleteSerializesToolsSchemaSafetyAndParsesFunctionCalls(t *testing.T) {
	var got map[string]any
	var apiKey string
	provider := NewGeminiProviderWithOptions("http://google.test", "gm-key", "gemini-flash", 512, "off")
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1beta/models/gemini-flash:generateContent" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		apiKey = r.Header.Get("x-goog-api-key")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return jsonResponse(200, `{
			"candidates": [{
				"content": {
					"parts": [
						{"text": "visible"},
						{"text": "hidden", "thought": true},
						{
							"thoughtSignature": "sig-123",
							"functionCall": {"name": "lookup", "args": {"city": "Chicago"}}
						}
					]
				}
			}],
			"usageMetadata": {"promptTokenCount": 7, "candidatesTokenCount": 4}
		}`), nil
	})

	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Temperature:    0.3,
		MaxTokens:      256,
		ResponseFormat: "json_schema",
		JSONSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"ok": map[string]any{"type": "boolean", "unsupported": true},
			},
			"required": []any{"ok", "missing"},
		},
		Messages: []ChatMessage{
			{Role: "system", Content: "sys"},
			{Role: "assistant", Content: "model opens"},
			{Role: "user", Content: "hi"},
			{
				Role: "assistant",
				ToolCalls: []message.ToolCall{{
					Name:             "lookup",
					ThoughtSignature: "sig-prev",
					Arguments:        map[string]any{"city": "Chicago"},
				}},
			},
			{Role: "tool", Name: "lookup", Content: "rain"},
			{Role: "tool", Name: "other", Content: "wind"},
		},
		Tools: []ToolSchema{{
			Name:        "lookup",
			Description: "Lookup weather",
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"city": map[string]any{"type": "string", "unsupported": true},
				},
				"required": []any{"city", "missing"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if apiKey != "gm-key" {
		t.Fatalf("x-goog-api-key = %q", apiKey)
	}
	if got["systemInstruction"].(map[string]any)["parts"] == nil {
		t.Fatalf("systemInstruction missing: %#v", got)
	}
	genCfg := got["generationConfig"].(map[string]any)
	if genCfg["temperature"].(float64) != 0.3 || genCfg["maxOutputTokens"].(float64) != 256 {
		t.Fatalf("generationConfig = %#v", genCfg)
	}
	if genCfg["responseMimeType"] != "application/json" {
		t.Fatalf("responseMimeType = %#v", genCfg["responseMimeType"])
	}
	if genCfg["thinkingConfig"].(map[string]any)["thinkingBudget"].(float64) != 512 {
		t.Fatalf("thinkingConfig = %#v", genCfg["thinkingConfig"])
	}
	responseSchema := genCfg["responseSchema"].(map[string]any)
	if _, ok := responseSchema["additionalProperties"]; ok {
		t.Fatalf("Gemini schema was not sanitized: %#v", responseSchema)
	}
	required := responseSchema["required"].([]any)
	if len(required) != 1 || required[0] != "ok" {
		t.Fatalf("required not filtered: %#v", required)
	}
	if len(got["safetySettings"].([]any)) == 0 {
		t.Fatalf("safety settings missing for off mode")
	}
	tools := got["tools"].([]any)
	funcDecl := tools[0].(map[string]any)["functionDeclarations"].([]any)[0].(map[string]any)
	params := funcDecl["parameters"].(map[string]any)
	if _, ok := params["additionalProperties"]; ok {
		t.Fatalf("tool schema was not sanitized: %#v", params)
	}
	contents := got["contents"].([]any)
	if contents[0].(map[string]any)["role"] != "user" {
		t.Fatalf("contents should start with user after repair: %#v", contents)
	}
	last := contents[len(contents)-1].(map[string]any)
	parts := last["parts"].([]any)
	if len(parts) != 2 || parts[0].(map[string]any)["functionResponse"] == nil || parts[1].(map[string]any)["functionResponse"] == nil {
		t.Fatalf("consecutive tool results were not batched: %#v", parts)
	}

	if resp.Content != "visible" {
		t.Fatalf("content = %q, want visible", resp.Content)
	}
	if resp.InputTokens != 7 || resp.OutputTokens != 4 {
		t.Fatalf("usage = %d/%d", resp.InputTokens, resp.OutputTokens)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "lookup" {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].ThoughtSignature != "sig-123" {
		t.Fatalf("thought signature = %q", resp.ToolCalls[0].ThoughtSignature)
	}
	if resp.ToolCalls[0].Arguments["city"] != "Chicago" {
		t.Fatalf("args = %+v", resp.ToolCalls[0].Arguments)
	}
}

func TestGeminiCompleteRetriesWithoutUnsupportedGenerationConfig(t *testing.T) {
	attempts := 0
	var retried map[string]any
	provider := NewGeminiProviderWithOptions("http://google.test", "gm-key", "gemini-flash", 0, "")
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		attempts++
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if attempts == 1 {
			cfg := got["generationConfig"].(map[string]any)
			if _, ok := cfg["topP"]; !ok {
				t.Fatalf("first request should include topP: %#v", cfg)
			}
			return jsonResponse(400, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"topP is not supported for this model"}}`), nil
		}
		retried = got
		return jsonResponse(200, `{
			"candidates": [{"content": {"parts": [{"text": "ok"}]}}],
			"usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1}
		}`), nil
	})

	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Messages:    []ChatMessage{{Role: "user", Content: "hi"}},
		Temperature: 0.2,
		TopP:        0.9,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	cfg := retried["generationConfig"].(map[string]any)
	if _, ok := cfg["topP"]; ok {
		t.Fatalf("retry should omit unsupported topP: %#v", cfg)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q, want ok", resp.Content)
	}
}

func TestGeminiCompleteRetriesWithoutUnsupportedThinkingConfig(t *testing.T) {
	attempts := 0
	var retried map[string]any
	provider := NewGeminiProviderWithOptions("http://google.test", "gm-key", "gemini-flash", 0, "")
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		attempts++
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if attempts == 1 {
			cfg := got["generationConfig"].(map[string]any)
			if _, ok := cfg["thinkingConfig"]; !ok {
				t.Fatalf("first request should include thinkingConfig: %#v", cfg)
			}
			return jsonResponse(400, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"generationConfig.thinkingConfig.thinkingBudget is not supported"}}`), nil
		}
		retried = got
		return jsonResponse(200, `{
			"candidates": [{"content": {"parts": [{"text": "ok"}]}}],
			"usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1}
		}`), nil
	})

	_, err := provider.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	cfg := retried["generationConfig"].(map[string]any)
	if _, ok := cfg["thinkingConfig"]; ok {
		t.Fatalf("retry should omit unsupported thinkingConfig: %#v", cfg)
	}
}

func TestGeminiModelsFiltersGenerateContentAndStripsPrefix(t *testing.T) {
	provider := NewGeminiProvider("http://google.test", "gm-key", "fallback")
	provider.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1beta/models" {
			t.Fatalf("path = %q, want /v1beta/models", r.URL.Path)
		}
		return jsonResponse(200, `{
			"models": [
				{"name": "models/gemini-a", "supportedGenerationMethods": ["generateContent"]},
				{"name": "models/embed", "supportedGenerationMethods": ["embedContent"]},
				{"name": "gemini-b", "supportedGenerationMethods": ["generateContent", "countTokens"]}
			]
		}`), nil
	})

	models, err := provider.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 || models[0] != "gemini-a" || models[1] != "gemini-b" {
		t.Fatalf("models = %v", models)
	}
}

func TestSanitizeSchemaForGeminiPreservesPropertyNamesAndFiltersRequired(t *testing.T) {
	got := sanitizeSchemaForGemini(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"keep": map[string]any{"type": "string", "additionalProperties": false},
		},
		"required":             []any{"keep", "drop"},
		"additionalProperties": false,
	})

	props := got["properties"].(map[string]any)
	if _, ok := props["keep"]; !ok {
		t.Fatalf("property name was dropped: %#v", got)
	}
	if _, ok := props["keep"].(map[string]any)["additionalProperties"]; ok {
		t.Fatalf("nested unsupported keyword not stripped: %#v", props["keep"])
	}
	required := got["required"].([]any)
	if len(required) != 1 || required[0] != "keep" {
		t.Fatalf("required = %#v, want [keep]", required)
	}
	if _, ok := got["additionalProperties"]; ok {
		t.Fatalf("top-level unsupported keyword not stripped: %#v", got)
	}
}

func TestGeminiSafeToolName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "tool"},
		{"   ", "tool"},
		{"valid_name", "valid_name"},
		{"valid_Name_123", "valid_Name_123"},
		{"123_invalid_start", "_123_invalid_start"}, // Starts with number -> replaced with _
		{"-invalid-start", "ainvalid_start"},       // Starts with non-alphanum non-underscore -> replaced with a
		{"mcp__yahoo-finance__get_stock", "mcp__yahoo_finance__get_stock"}, // Hyphen replaced with _
		{"name.with.dots!", "name_with_dots_"}, // Special chars replaced with _
		{strings.Repeat("a", 100), strings.Repeat("a", 64)}, // Truncated to 64
	}

	for _, tt := range tests {
		got := geminiSafeToolName(tt.in)
		if got != tt.want {
			t.Errorf("geminiSafeToolName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBuildGeminiToolMaps(t *testing.T) {
	tools := []ToolSchema{
		{Name: "mcp__yahoo-finance__get"},
		{Name: "mcp__yahoo.finance__get"}, // would clash with above after sanitization
	}

	origToWire, wireToOrig := buildGeminiToolMaps(tools)

	if len(origToWire) != 2 {
		t.Fatalf("expected 2 mapped tools, got %d", len(origToWire))
	}

	wire1 := origToWire["mcp__yahoo-finance__get"]
	wire2 := origToWire["mcp__yahoo.finance__get"]

	if wire1 == wire2 {
		t.Errorf("expected distinct wire names, got both as %q", wire1)
	}

	if orig := wireToOrig[wire1]; orig != "mcp__yahoo-finance__get" {
		t.Errorf("expected wire1 to map back to original, got %q", orig)
	}
}
