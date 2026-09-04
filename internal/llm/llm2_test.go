// llm2_test.go — second wave of coverage tests for internal/llm.
// Targets remaining uncovered paths after anthropic_test.go, gemini_test.go,
// ollama_test.go, and router_test.go. Uses fake http.RoundTripper —
// no httptest.NewServer.
package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// AnthropicProvider — additional paths
// ---------------------------------------------------------------------------

// TestAnthropicIDReturnsAnthropic verifies the Provider ID contract.
func TestAnthropicIDReturnsAnthropic(t *testing.T) {
	p := NewAnthropicProvider("", "", "")
	if p.ID() != "anthropic" {
		t.Errorf("ID = %q, want anthropic", p.ID())
	}
}

// TestAnthropicProviderDefaults verifies that NewAnthropicProvider fills in
// the default base URL and model when empty strings are supplied.
func TestAnthropicProviderDefaults(t *testing.T) {
	p := NewAnthropicProvider("", "", "")
	if p.baseURL != "https://api.anthropic.com" {
		t.Errorf("baseURL = %q, want https://api.anthropic.com", p.baseURL)
	}
	if p.model != "claude-3-5-sonnet-latest" {
		t.Errorf("model = %q, want claude-3-5-sonnet-latest", p.model)
	}
}

// TestAnthropicWithCachingSetsCachingFlag verifies that the caching constructor
// enables promptCaching.
func TestAnthropicWithCachingSetsCachingFlag(t *testing.T) {
	p := NewAnthropicProviderWithCaching("", "", "")
	if !p.promptCaching {
		t.Error("NewAnthropicProviderWithCaching should set promptCaching=true")
	}
}

// TestAnthropicExtendedThinkingDefaultsBudget verifies that a zero thinkingBudget
// is replaced with the sensible default (8192) when extendedThinking is true.
func TestAnthropicExtendedThinkingDefaultsBudget(t *testing.T) {
	p := NewAnthropicProviderWithOptions("", "", "", false, true, 0)
	if p.thinkingBudget != 8192 {
		t.Errorf("thinkingBudget = %d, want 8192 (default)", p.thinkingBudget)
	}
}

// TestAnthropicExtendedThinkingExplicitBudget verifies that an explicit budget
// is preserved and NOT overridden to 8192.
func TestAnthropicExtendedThinkingExplicitBudget(t *testing.T) {
	p := NewAnthropicProviderWithOptions("", "", "", false, true, 4096)
	if p.thinkingBudget != 4096 {
		t.Errorf("thinkingBudget = %d, want 4096", p.thinkingBudget)
	}
}

// TestAnthropicCompleteHTTPErrorReturned verifies that a non-2xx response is
// surfaced as an error containing the HTTP status.
func TestAnthropicCompleteHTTPErrorReturned(t *testing.T) {
	p := NewAnthropicProvider("http://anthropic.test", "key", "m")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(400, `{"error":{"message":"bad request"}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error on HTTP 400")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention status 400, got: %v", err)
	}
}

// TestAnthropicCompleteNoCachingNoBeta verifies that when promptCaching is
// false the anthropic-beta header is NOT sent (empty or absent).
func TestAnthropicCompleteNoCachingNoBeta(t *testing.T) {
	var gotBeta string
	p := NewAnthropicProvider("http://anthropic.test", "key", "m")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		gotBeta = r.Header.Get("anthropic-beta")
		return jsonResponse(200, `{"content":[],"usage":{}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotBeta != "" {
		t.Errorf("anthropic-beta should be empty when no features enabled, got %q", gotBeta)
	}
}

// TestAnthropicCompleteNoSystemPrompt verifies that when there are no system
// messages the request body does not contain a "system" key.
func TestAnthropicCompleteNoSystemPrompt(t *testing.T) {
	var got map[string]any
	p := NewAnthropicProvider("http://anthropic.test", "key", "m")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(200, `{"content":[],"usage":{}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, ok := got["system"]; ok {
		t.Error("request body should not contain 'system' when there is no system message")
	}
}

// TestAnthropicCompleteUsesRequestModelOverride verifies that the model
// specified in CompletionRequest overrides the provider's default.
func TestAnthropicCompleteUsesRequestModelOverride(t *testing.T) {
	var got map[string]any
	p := NewAnthropicProvider("http://anthropic.test", "key", "default-model")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(200, `{"content":[],"usage":{}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Model:    "override-model",
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got["model"] != "override-model" {
		t.Errorf("model = %v, want override-model", got["model"])
	}
}

// TestAnthropicModelsNoAPIKeyReturnsBakedIn verifies that Models() returns
// the baked-in list when no API key is configured (no HTTP call is made).
func TestAnthropicModelsNoAPIKeyReturnsBakedIn(t *testing.T) {
	p := NewAnthropicProvider("http://anthropic.test", "", "m")
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) == 0 {
		t.Error("Models() should return non-empty baked-in list when no API key")
	}
}

// TestAnthropicModelsHTTPErrorSurfacesError verifies that once an API key is
// configured, a non-2xx or transport error from /v1/models is returned to the
// caller rather than being silently masked with the baked-in list.
//
// This was the E4 (Cohort E — Providers page failure handling) change: the
// old behaviour swallowed 401/403/500/network errors and reported "Reachable ✓"
// with a stale model list, so the operator could not tell a rotated key or
// down provider from a working one.
func TestAnthropicModelsHTTPErrorSurfacesError(t *testing.T) {
	p := NewAnthropicProvider("http://anthropic.test", "key", "m")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(500, `{"error":"internal"}`), nil
	})
	if _, err := p.Models(context.Background()); err == nil {
		t.Fatal("Models on 500 must surface an error, got nil (silent fallback regressed)")
	}
}

// TestAnthropicModelsAuthErrorSurfacesError verifies that a 401 (rotated /
// revoked key) is returned to the caller so llm.ClassifyProviderError can
// classify it as "bad key" for the Providers page.
func TestAnthropicModelsAuthErrorSurfacesError(t *testing.T) {
	p := NewAnthropicProvider("http://anthropic.test", "key", "m")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`), nil
	})
	if _, err := p.Models(context.Background()); err == nil {
		t.Fatal("Models on 401 must surface an error so bad-key is diagnosable")
	}
}

// TestAnthropicModelsLiveListReturnsIDs verifies that when /v1/models returns
// a valid response the IDs are extracted and returned.
func TestAnthropicModelsLiveListReturnsIDs(t *testing.T) {
	p := NewAnthropicProvider("http://anthropic.test", "key", "m")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		return jsonResponse(200, `{"data":[{"id":"claude-one"},{"id":"claude-two"}]}`), nil
	})
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d: %v", len(models), models)
	}
	if models[0] != "claude-one" || models[1] != "claude-two" {
		t.Errorf("models = %v", models)
	}
}

// TestAnthropicModelsEmptyDataReturnsBakedIn verifies that an empty data array
// in the /v1/models response falls back to the baked-in list.
func TestAnthropicModelsEmptyDataReturnsBakedIn(t *testing.T) {
	p := NewAnthropicProvider("http://anthropic.test", "key", "m")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"data":[]}`), nil
	})
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) == 0 {
		t.Error("empty data should fall back to baked-in list")
	}
}

// TestDefaultIfZeroReturnsDefault covers the defaultIfZero helper used to
// fill max_tokens when the caller passes 0.
func TestDefaultIfZeroReturnsDefault(t *testing.T) {
	if defaultIfZero(0, 4096) != 4096 {
		t.Error("defaultIfZero(0, 4096) should return 4096")
	}
	if defaultIfZero(-1, 4096) != 4096 {
		t.Error("defaultIfZero(-1, 4096) should return 4096")
	}
	if defaultIfZero(1024, 4096) != 1024 {
		t.Error("defaultIfZero(1024, 4096) should return 1024")
	}
}

// ---------------------------------------------------------------------------
// OllamaProvider — additional paths
// ---------------------------------------------------------------------------

// TestOllamaIDReturnsOllama verifies the Provider ID contract.
func TestOllamaIDReturnsOllama(t *testing.T) {
	p := NewOllamaProvider("http://localhost:11434", "", "", nil)
	if p.ID() != "ollama" {
		t.Errorf("ID = %q, want ollama", p.ID())
	}
}

// TestOllamaDefaultKeepAliveApplied verifies that an empty keepAlive is
// replaced with DefaultOllamaKeepAlive ("30m").
func TestOllamaDefaultKeepAliveApplied(t *testing.T) {
	p := NewOllamaProvider("http://localhost:11434", "llama3", "", nil)
	if p.keepAlive != DefaultOllamaKeepAlive {
		t.Errorf("keepAlive = %q, want %q", p.keepAlive, DefaultOllamaKeepAlive)
	}
}

// TestOllamaCompleteNonOKStatusReturnsError verifies that a non-200 status
// code from the Ollama API is surfaced as an error.
func TestOllamaCompleteNonOKStatusReturnsError(t *testing.T) {
	p := NewOllamaProvider("http://ollama.test", "llama3", "", nil)
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(503, `{"error":"overloaded"}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error on HTTP 503")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error should mention status 503, got: %v", err)
	}
}

// TestOllamaCompleteStreamNonOKReturnsError verifies that streaming mode
// also propagates a non-200 status as an error.
func TestOllamaCompleteStreamNonOKReturnsError(t *testing.T) {
	p := NewOllamaProvider("http://ollama.test", "llama3", "", nil)
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(429, `{"error":"rate limited"}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Stream:   true,
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error on HTTP 429 in stream mode")
	}
}

// TestOllamaCompleteUsesDefaultModelWhenEmpty verifies that when the request's
// Model field is empty the provider's configured default is sent.
func TestOllamaCompleteUsesDefaultModelWhenEmpty(t *testing.T) {
	var got map[string]any
	p := NewOllamaProvider("http://ollama.test", "default-model", "", nil)
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(200, `{"message":{"role":"assistant","content":"ok"}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Model:    "",
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got["model"] != "default-model" {
		t.Errorf("model = %v, want default-model", got["model"])
	}
}

// TestOllamaCompleteJSONFormatSentAsString verifies that ResponseFormat "json"
// sets format to the string "json" (not an object/map).
func TestOllamaCompleteJSONFormatSentAsString(t *testing.T) {
	var got map[string]any
	p := NewOllamaProvider("http://ollama.test", "llama3", "", nil)
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(200, `{"message":{"role":"assistant","content":"{}"}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		ResponseFormat: "json",
		Messages:       []ChatMessage{{Role: "user", Content: "go"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got["format"] != "json" {
		t.Errorf("format = %#v, want \"json\"", got["format"])
	}
}

// TestOllamaCompleteJSONSchemaFallsBackToJSONStringWhenNilSchema verifies that
// ResponseFormat "json_schema" with a nil JSONSchema falls back to "json".
func TestOllamaCompleteJSONSchemaFallsBackToJSONStringWhenNilSchema(t *testing.T) {
	var got map[string]any
	p := NewOllamaProvider("http://ollama.test", "llama3", "", nil)
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(200, `{"message":{"role":"assistant","content":"{}"}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		ResponseFormat: "json_schema",
		JSONSchema:     nil,
		Messages:       []ChatMessage{{Role: "user", Content: "go"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got["format"] != "json" {
		t.Errorf("format = %#v, want \"json\" fallback", got["format"])
	}
}

// TestOllamaCompleteToolChoiceAutoSentAsString verifies that "auto" is sent as
// a plain string (not an object) under the OpenAI-style tool_choice field.
func TestOllamaCompleteToolChoiceAutoSentAsString(t *testing.T) {
	var got map[string]any
	p := NewOllamaProvider("http://ollama.test", "llama3", "", nil)
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(200, `{"message":{"role":"assistant","content":"ok"}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		ToolChoice: "auto",
		Messages:   []ChatMessage{{Role: "user", Content: "hi"}},
		Tools:      []ToolSchema{{Name: "t", Parameters: map[string]any{}}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %#v, want \"auto\"", got["tool_choice"])
	}
}

// TestOllamaCompleteStreamWithToolsDoesNotStream verifies that when both
// Stream=true and tools are present the request falls through to the
// non-streaming path (stream:false in the request body).
func TestOllamaCompleteStreamWithToolsDoesNotStream(t *testing.T) {
	var got map[string]any
	p := NewOllamaProvider("http://ollama.test", "llama3", "", nil)
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(200, `{"message":{"role":"assistant","content":"ok"}}`), nil
	})
	resp, err := p.Complete(context.Background(), CompletionRequest{
		Stream:   true,
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
		Tools:    []ToolSchema{{Name: "t", Parameters: map[string]any{}}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// Non-streaming path returns nil Stream channel.
	if resp.Stream != nil {
		t.Error("Stream channel should be nil when tools are present (non-streaming path)")
	}
	if got["stream"] == true {
		t.Error("stream:true should not be sent when tools are present")
	}
}

// TestOllamaModelsReturnsParsedNames verifies that /api/tags is called and
// model names are extracted from the response.
func TestOllamaModelsReturnsParsedNames(t *testing.T) {
	p := NewOllamaProvider("http://ollama.test", "llama3", "", nil)
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/tags" {
			t.Fatalf("path = %q, want /api/tags", r.URL.Path)
		}
		return jsonResponse(200, `{"models":[{"name":"llama3"},{"name":"mistral"}]}`), nil
	})
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d: %v", len(models), models)
	}
	if models[0] != "llama3" || models[1] != "mistral" {
		t.Errorf("models = %v", models)
	}
}

// TestOllamaModelsEmptyList verifies that an empty models list is returned as
// an empty slice (not nil, not an error).
func TestOllamaModelsEmptyList(t *testing.T) {
	p := NewOllamaProvider("http://ollama.test", "llama3", "", nil)
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"models":[]}`), nil
	})
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("expected 0 models, got %v", models)
	}
}

// ---------------------------------------------------------------------------
// OpenAIProvider — additional paths
// ---------------------------------------------------------------------------

// TestOpenAIIDReturnsConfiguredID verifies that the ID is preserved.
func TestOpenAIIDReturnsConfiguredID(t *testing.T) {
	p := NewOpenAIProvider("groq", "http://api.groq.com", "key", "llama3")
	if p.ID() != "groq" {
		t.Errorf("ID = %q, want groq", p.ID())
	}
}

// TestOpenAICompleteHTTPErrorReturned verifies a non-2xx status is an error.
func TestOpenAICompleteHTTPErrorReturned(t *testing.T) {
	p := NewOpenAIProvider("openai", "http://openai.test", "key", "gpt")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(401, `{"error":{"message":"Unauthorized"}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error on HTTP 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401, got: %v", err)
	}
}

// TestOpenAICompleteEmptyChoicesReturnsError verifies that an empty choices
// array in the response body surfaces as an error.
func TestOpenAICompleteEmptyChoicesReturnsError(t *testing.T) {
	p := NewOpenAIProvider("openai", "http://openai.test", "key", "gpt")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"choices":[],"usage":{}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error when choices is empty")
	}
	if !strings.Contains(err.Error(), "empty choices") {
		t.Errorf("error should mention 'empty choices', got: %v", err)
	}
}

// TestOpenAICompleteUsesDefaultModelWhenEmpty verifies that the provider's
// model is used when CompletionRequest.Model is empty.
func TestOpenAICompleteUsesDefaultModelWhenEmpty(t *testing.T) {
	var got map[string]any
	p := NewOpenAIProvider("openai", "http://openai.test", "key", "gpt-default")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(200, `{"choices":[{"message":{"content":"ok"}}],"usage":{}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got["model"] != "gpt-default" {
		t.Errorf("model = %v, want gpt-default", got["model"])
	}
}

// TestOpenAICompleteNoOrganizationHeader verifies that the OpenAI-Organization
// header is absent when organization is empty.
func TestOpenAICompleteNoOrganizationHeader(t *testing.T) {
	var gotOrg string
	p := NewOpenAIProvider("openai", "http://openai.test", "key", "gpt")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		gotOrg = r.Header.Get("OpenAI-Organization")
		return jsonResponse(200, `{"choices":[{"message":{"content":"ok"}}],"usage":{}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotOrg != "" {
		t.Errorf("OpenAI-Organization header should be absent, got %q", gotOrg)
	}
}

// TestOpenAICompleteNoAuthHeaderWhenKeyEmpty verifies that the Authorization
// header is absent when the API key is an empty string.
func TestOpenAICompleteNoAuthHeaderWhenKeyEmpty(t *testing.T) {
	var gotAuth string
	p := NewOpenAIProvider("openai", "http://openai.test", "", "gpt")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		return jsonResponse(200, `{"choices":[{"message":{"content":"ok"}}],"usage":{}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization header should be absent when no API key, got %q", gotAuth)
	}
}

// TestOpenAICompleteJSONObjectFormatSent verifies that ResponseFormat "json"
// produces {"type":"json_object"} in the request body.
func TestOpenAICompleteJSONObjectFormatSent(t *testing.T) {
	var got map[string]any
	p := NewOpenAIProvider("openai", "http://openai.test", "key", "gpt")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(200, `{"choices":[{"message":{"content":"{}"}}],"usage":{}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		ResponseFormat: "json",
		Messages:       []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	rf := got["response_format"].(map[string]any)
	if rf["type"] != "json_object" {
		t.Errorf("response_format.type = %v, want json_object", rf["type"])
	}
	if got["stream"] != false {
		t.Errorf("stream = %#v, want false", got["stream"])
	}
}

// TestOpenAICompleteJSONSchemaNilFallsBackToJSONObject verifies that
// ResponseFormat "json_schema" with a nil JSONSchema falls back to json_object.
func TestOpenAICompleteJSONSchemaNilFallsBackToJSONObject(t *testing.T) {
	var got map[string]any
	p := NewOpenAIProvider("openai", "http://openai.test", "key", "gpt")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(200, `{"choices":[{"message":{"content":"{}"}}],"usage":{}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		ResponseFormat: "json_schema",
		JSONSchema:     nil,
		Messages:       []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	rf := got["response_format"].(map[string]any)
	if rf["type"] != "json_object" {
		t.Errorf("response_format.type = %v, want json_object fallback", rf["type"])
	}
}

// TestOpenAICompleteJSONSchemaLenientSetsStrictFalse verifies that
// JSONSchemaLenient makes the schema a best-effort guide (strict:false) instead
// of a hard strict-mode contract — needed for schemas with freeform sub-objects.
func TestOpenAICompleteJSONSchemaLenientSetsStrictFalse(t *testing.T) {
	var got map[string]any
	p := NewOpenAIProvider("openai", "http://openai.test", "key", "gpt")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(200, `{"choices":[{"message":{"content":"{}"}}],"usage":{}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		ResponseFormat:    "json_schema",
		JSONSchema:        map[string]any{"type": "object"},
		JSONSchemaLenient: true,
		Messages:          []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	rf := got["response_format"].(map[string]any)
	js := rf["json_schema"].(map[string]any)
	if js["strict"] != false {
		t.Errorf("strict = %v, want false when lenient", js["strict"])
	}

	// Default (non-lenient) stays strict:true.
	_, _ = p.Complete(context.Background(), CompletionRequest{
		ResponseFormat: "json_schema",
		JSONSchema:     map[string]any{"type": "object"},
		Messages:       []ChatMessage{{Role: "user", Content: "hi"}},
	})
	js = got["response_format"].(map[string]any)["json_schema"].(map[string]any)
	if js["strict"] != true {
		t.Errorf("strict = %v, want true by default", js["strict"])
	}
}

// TestOpenAICompleteStreamWithToolsDoesNotStream verifies that stream+tools
// takes the non-streaming path (same as Ollama).
func TestOpenAICompleteStreamWithToolsDoesNotStream(t *testing.T) {
	var got map[string]any
	p := NewOpenAIProvider("openai", "http://openai.test", "key", "gpt")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(200, `{"choices":[{"message":{"content":"ok"}}],"usage":{}}`), nil
	})
	resp, err := p.Complete(context.Background(), CompletionRequest{
		Stream:   true,
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
		Tools:    []ToolSchema{{Name: "t", Parameters: map[string]any{}}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Stream != nil {
		t.Error("Stream channel should be nil when tools are present")
	}
}

// TestOpenAICompleteRetryReplaysBody verifies transient retry attempts send a
// fresh request body instead of reusing a drained reader.
func TestOpenAICompleteRetryReplaysBody(t *testing.T) {
	attempts := 0
	var second map[string]any
	p := NewOpenAIProvider("openai", "http://openai.test", "key", "gpt")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			_, _ = io.ReadAll(r.Body)
			return jsonResponse(502, `bad gateway`), nil
		}
		if err := json.NewDecoder(r.Body).Decode(&second); err != nil {
			t.Fatalf("decode retry body: %v", err)
		}
		return jsonResponse(200, `{"choices":[{"message":{"content":"ok"}}],"usage":{}}`), nil
	})
	resp, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
		Tools:    []ToolSchema{{Name: "t", Parameters: map[string]any{}}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q, want ok", resp.Content)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if second["model"] != "gpt" || len(second["messages"].([]any)) != 1 {
		t.Fatalf("retry body missing expected fields: %#v", second)
	}
}

func TestOpenAICompleteRetriesWithoutUnsupportedOptionalParam(t *testing.T) {
	attempts := 0
	var retried map[string]any
	p := NewOpenAIProvider("openroute", "http://openroute.test/v1", "key", "router-model")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		attempts++
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if attempts == 1 {
			if _, ok := got["top_p"]; !ok {
				t.Fatalf("first request should include top_p: %#v", got)
			}
			return jsonResponse(400, `{"error":{"type":"invalid_request_error","message":"top_p is not supported for this model"}}`), nil
		}
		retried = got
		return jsonResponse(200, `{"choices":[{"message":{"content":"ok"}}],"usage":{}}`), nil
	})
	resp, err := p.Complete(context.Background(), CompletionRequest{
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
	if _, ok := retried["top_p"]; ok {
		t.Fatalf("retry should omit unsupported top_p: %#v", retried)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q, want ok", resp.Content)
	}
}

func TestOpenAICompleteRetriesMaxTokensAsMaxCompletionTokens(t *testing.T) {
	attempts := 0
	var retried map[string]any
	p := NewOpenAIProvider("openroute", "http://openroute.test/v1", "key", "reasoning-model")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		attempts++
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if attempts == 1 {
			if got["max_tokens"] == nil {
				t.Fatalf("first request should include max_tokens: %#v", got)
			}
			return jsonResponse(400, `{"error":{"type":"invalid_request_error","message":"max_tokens is not supported for this model. Use max_completion_tokens instead."}}`), nil
		}
		retried = got
		return jsonResponse(200, `{"choices":[{"message":{"content":"ok"}}],"usage":{}}`), nil
	})
	resp, err := p.Complete(context.Background(), CompletionRequest{
		Messages:  []ChatMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 512,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if _, ok := retried["max_tokens"]; ok {
		t.Fatalf("retry should omit max_tokens: %#v", retried)
	}
	if got := retried["max_completion_tokens"]; got != float64(512) {
		t.Fatalf("max_completion_tokens = %#v, want 512", got)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q, want ok", resp.Content)
	}
}

func TestOpenAICompleteRetriesWithoutUnsupportedParallelToolCalls(t *testing.T) {
	attempts := 0
	var retried map[string]any
	p := NewOpenAIProvider("openroute", "http://openroute.test/v1", "key", "gemini/gemini-pro")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		attempts++
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if attempts == 1 {
			if _, ok := got["parallel_tool_calls"]; !ok {
				t.Fatalf("first request should include parallel_tool_calls: %#v", got)
			}
			return jsonResponse(400, `{"error":{"message":"Unknown parameter: parallel_tool_calls","type":"invalid_request_error"}}`), nil
		}
		retried = got
		return jsonResponse(200, `{"choices":[{"message":{"content":"ok"}}],"usage":{}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
		Tools:    []ToolSchema{{Name: "lookup", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if _, ok := retried["parallel_tool_calls"]; ok {
		t.Fatalf("retry should omit unsupported parallel_tool_calls: %#v", retried)
	}
}

func TestOpenAICompleteRetriesWithoutUnsupportedToolChoice(t *testing.T) {
	attempts := 0
	var retried map[string]any
	p := NewOpenAIProvider("openroute", "http://openroute.test/v1", "key", "router-model")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		attempts++
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if attempts == 1 {
			if _, ok := got["tool_choice"]; !ok {
				t.Fatalf("first request should include tool_choice: %#v", got)
			}
			return jsonResponse(400, `{"error":{"message":"Unsupported parameter: tool_choice","type":"invalid_request_error"}}`), nil
		}
		retried = got
		return jsonResponse(200, `{"choices":[{"message":{"content":"ok"}}],"usage":{}}`), nil
	})
	resp, err := p.Complete(context.Background(), CompletionRequest{
		Messages:   []ChatMessage{{Role: "user", Content: "hi"}},
		Tools:      []ToolSchema{{Name: "lookup", Parameters: map[string]any{"type": "object"}}},
		ToolChoice: "required",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if _, ok := retried["tool_choice"]; ok {
		t.Fatalf("retry should omit unsupported tool_choice: %#v", retried)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q, want ok", resp.Content)
	}
}

// TestOpenAIModelsReturnsListFromServer verifies that /models is called and
// IDs are extracted.
func TestOpenAIModelsReturnsListFromServer(t *testing.T) {
	p := NewOpenAIProvider("openai", "http://openai.test", "key", "gpt")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q, want /models", r.URL.Path)
		}
		return jsonResponse(200, `{"data":[{"id":"gpt-4"},{"id":"gpt-3.5-turbo"}]}`), nil
	})
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
}

// TestOpenAIModelsHTTPErrorReturnsError verifies that a non-2xx /models
// response is surfaced as an error (unlike Anthropic/Gemini which fall back).
func TestOpenAIModelsHTTPErrorReturnsError(t *testing.T) {
	p := NewOpenAIProvider("openai", "http://openai.test", "key", "gpt")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(403, `{"error":"forbidden"}`), nil
	})
	_, err := p.Models(context.Background())
	if err == nil {
		t.Fatal("expected error on /models 403")
	}
}

// TestOpenAIModelsEmptyDataReturnsDefaultModel verifies that empty data[] in
// the /models response returns the provider's configured default model.
func TestOpenAIModelsEmptyDataReturnsDefaultModel(t *testing.T) {
	p := NewOpenAIProvider("openai", "http://openai.test", "key", "my-default")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"data":[]}`), nil
	})
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 || models[0] != "my-default" {
		t.Errorf("expected [my-default], got %v", models)
	}
}

// ---------------------------------------------------------------------------
// GeminiProvider — additional paths
// ---------------------------------------------------------------------------

// TestGeminiIDReturnsGoogle verifies the Provider ID.
func TestGeminiIDReturnsGoogle(t *testing.T) {
	p := NewGeminiProvider("", "", "")
	if p.ID() != "google" {
		t.Errorf("ID = %q, want google", p.ID())
	}
}

// TestGeminiProviderDefaultBaseURL verifies that the base URL defaults to the
// Google API when empty.
func TestGeminiProviderDefaultBaseURL(t *testing.T) {
	p := NewGeminiProvider("", "", "")
	if p.baseURL != "https://generativelanguage.googleapis.com" {
		t.Errorf("baseURL = %q", p.baseURL)
	}
}

// TestGeminiProviderDefaultModel verifies the default model is set.
func TestGeminiProviderDefaultModel(t *testing.T) {
	p := NewGeminiProvider("", "", "")
	if p.model != "gemini-2.5-pro" {
		t.Errorf("model = %q, want gemini-2.5-pro", p.model)
	}
}

// TestGeminiCompleteHTTPErrorReturned verifies that a non-2xx response is an error.
func TestGeminiCompleteHTTPErrorReturned(t *testing.T) {
	p := NewGeminiProvider("http://google.test", "key", "gemini-flash")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(400, `{"error":{"message":"invalid key"}}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error on HTTP 400")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention 400, got: %v", err)
	}
}

// TestGeminiCompleteEmptyBodyErrorReturned verifies the special empty-body
// message path (HTTP 400+ with empty body).
func TestGeminiCompleteEmptyBodyErrorReturned(t *testing.T) {
	p := NewGeminiProvider("http://google.test", "key", "gemini-flash")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(413, ``), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error on HTTP 413 empty body")
	}
	if !strings.Contains(err.Error(), "413") {
		t.Errorf("error should mention 413, got: %v", err)
	}
}

// TestGeminiCompleteNoCandidatesReturnsEmptyContent verifies that when the
// response has no candidates a zero-value CompletionResponse is returned (no error).
func TestGeminiCompleteNoCandidatesReturnsEmptyContent(t *testing.T) {
	p := NewGeminiProvider("http://google.test", "key", "gemini-flash")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"candidates":[],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":0}}`), nil
	})
	resp, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "" {
		t.Errorf("content = %q, want empty (no candidates)", resp.Content)
	}
	if resp.InputTokens != 5 {
		t.Errorf("InputTokens = %d, want 5", resp.InputTokens)
	}
}

// TestGeminiCompleteStrictSafetySettings verifies that safetyLevel "strict"
// injects BLOCK_LOW_AND_ABOVE settings.
func TestGeminiCompleteStrictSafetySettings(t *testing.T) {
	var got map[string]any
	p := NewGeminiProviderWithOptions("http://google.test", "key", "gemini-flash", 0, "strict")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(200, `{"candidates":[]}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	ss, ok := got["safetySettings"].([]any)
	if !ok || len(ss) == 0 {
		t.Fatal("safetySettings should be non-empty for strict level")
	}
	for _, s := range ss {
		setting := s.(map[string]any)
		if setting["threshold"] != "BLOCK_LOW_AND_ABOVE" {
			t.Errorf("threshold = %v, want BLOCK_LOW_AND_ABOVE", setting["threshold"])
		}
	}
}

// TestGeminiCompleteNoSafetySettingsForDefaultLevel verifies that the default
// safety level ("") does not inject any safetySettings key at all.
func TestGeminiCompleteNoSafetySettingsForDefaultLevel(t *testing.T) {
	var got map[string]any
	p := NewGeminiProviderWithOptions("http://google.test", "key", "gemini-flash", 0, "")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(200, `{"candidates":[]}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, ok := got["safetySettings"]; ok {
		t.Error("safetySettings should not be present for default safety level")
	}
}

// TestGeminiCompleteThinkingBudgetAutoMode verifies that thinkingBudget == -1
// produces thinkingMode:"AUTO" in the request.
func TestGeminiCompleteThinkingBudgetAutoMode(t *testing.T) {
	var got map[string]any
	p := NewGeminiProviderWithOptions("http://google.test", "key", "gemini-flash", -1, "")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(200, `{"candidates":[]}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	genCfg := got["generationConfig"].(map[string]any)
	tc, ok := genCfg["thinkingConfig"].(map[string]any)
	if !ok {
		t.Fatal("thinkingConfig missing for budget=-1")
	}
	if tc["thinkingMode"] != "AUTO" {
		t.Errorf("thinkingMode = %v, want AUTO", tc["thinkingMode"])
	}
}

// TestGeminiCompleteThinkingBudgetZeroNonProModelDisablesThinking verifies that
// budget=0 on a non-pro, non-thinking model explicitly sets thinkingBudget:0.
func TestGeminiCompleteThinkingBudgetZeroNonProModelDisablesThinking(t *testing.T) {
	var got map[string]any
	p := NewGeminiProviderWithOptions("http://google.test", "key", "gemini-flash", 0, "")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(200, `{"candidates":[]}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	genCfg := got["generationConfig"].(map[string]any)
	tc, ok := genCfg["thinkingConfig"].(map[string]any)
	if !ok {
		t.Fatal("thinkingConfig should be present for non-pro models with budget=0")
	}
	if int(tc["thinkingBudget"].(float64)) != 0 {
		t.Errorf("thinkingBudget = %v, want 0", tc["thinkingBudget"])
	}
}

// TestGeminiCompleteThinkingBudgetZeroProModelOmitsThinkingConfig verifies that
// budget=0 on a model whose name contains "pro" omits thinkingConfig entirely.
func TestGeminiCompleteThinkingBudgetZeroProModelOmitsThinkingConfig(t *testing.T) {
	var got map[string]any
	p := NewGeminiProviderWithOptions("http://google.test", "key", "gemini-pro", 0, "")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(200, `{"candidates":[]}`), nil
	})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	genCfg := got["generationConfig"].(map[string]any)
	if _, ok := genCfg["thinkingConfig"]; ok {
		t.Error("thinkingConfig should be omitted for 'pro' models with budget=0")
	}
}

// TestGeminiModelsHTTPErrorReturnsFallback verifies that a non-2xx /v1beta/models
// response returns the single default model without an error.
func TestGeminiModelsHTTPErrorReturnsFallback(t *testing.T) {
	p := NewGeminiProvider("http://google.test", "key", "my-fallback")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(500, `{}`), nil
	})
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models on 500 should not error, got: %v", err)
	}
	if len(models) != 1 || models[0] != "my-fallback" {
		t.Errorf("expected [my-fallback] fallback, got %v", models)
	}
}

// TestGeminiModelsEmptyListReturnsFallback verifies that an empty models list
// in the response returns the default model.
func TestGeminiModelsEmptyListReturnsFallback(t *testing.T) {
	p := NewGeminiProvider("http://google.test", "key", "default-model")
	p.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"models":[]}`), nil
	})
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 || models[0] != "default-model" {
		t.Errorf("expected [default-model] for empty list, got %v", models)
	}
}

// ---------------------------------------------------------------------------
// sanitizeSchemaForGemini — additional paths
// ---------------------------------------------------------------------------

// TestSanitizeSchemaForGeminiNilReturnsNil verifies that nil input → nil output.
func TestSanitizeSchemaForGeminiNilReturnsNil(t *testing.T) {
	if out := sanitizeSchemaForGemini(nil); out != nil {
		t.Errorf("expected nil, got %v", out)
	}
}

// TestSanitizeSchemaForGeminiStripsUnknownTopLevelKeys verifies that keys not
// in the allow-list are removed from the top-level schema.
func TestSanitizeSchemaForGeminiStripsUnknownTopLevelKeys(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,     // not allowed
		"$schema":              "draft-7", // not allowed
		"description":          "kept",    // allowed
	}
	out := sanitizeSchemaForGemini(schema)
	if _, ok := out["additionalProperties"]; ok {
		t.Error("additionalProperties should be stripped")
	}
	if _, ok := out["$schema"]; ok {
		t.Error("$schema should be stripped")
	}
	if out["description"] != "kept" {
		t.Error("description should be preserved")
	}
}

// TestSanitizeSchemaForGeminiAllowedArrayField verifies that array-valued
// fields like "enum" are preserved and not stripped.
func TestSanitizeSchemaForGeminiAllowedArrayField(t *testing.T) {
	schema := map[string]any{
		"type": "string",
		"enum": []any{"a", "b", "c"},
	}
	out := sanitizeSchemaForGemini(schema)
	enum, ok := out["enum"].([]any)
	if !ok || len(enum) != 3 {
		t.Errorf("enum = %v, want [a b c]", out["enum"])
	}
}

// TestSanitizeSchemaForGeminiRequiredAllDroppedWhenNoneInProperties verifies
// that when none of the required entries exist in properties, the required key
// is removed entirely.
func TestSanitizeSchemaForGeminiRequiredAllDroppedWhenNoneInProperties(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"name": map[string]any{"type": "string"}},
		"required":   []any{"missing1", "missing2"},
	}
	out := sanitizeSchemaForGemini(schema)
	if _, ok := out["required"]; ok {
		t.Error("required should be absent when none of its entries are in properties")
	}
}

// TestSanitizeSchemaForGeminiItemsRecurses verifies that the "items" key
// (for array schemas) is recursively sanitized.
func TestSanitizeSchemaForGeminiItemsRecurses(t *testing.T) {
	schema := map[string]any{
		"type": "array",
		"items": map[string]any{
			"type":                 "string",
			"additionalProperties": false, // should be stripped in items
		},
	}
	out := sanitizeSchemaForGemini(schema)
	items, ok := out["items"].(map[string]any)
	if !ok {
		t.Fatal("items should be present")
	}
	if _, ok := items["additionalProperties"]; ok {
		t.Error("additionalProperties in items should be stripped")
	}
}

// ---------------------------------------------------------------------------
// OllamaEmbedder — ID
// ---------------------------------------------------------------------------

// TestOllamaEmbedderID verifies the embedder ID.
func TestOllamaEmbedderID(t *testing.T) {
	e := NewOllamaEmbedder("http://localhost:11434")
	if e.ID() != "ollama" {
		t.Errorf("ID = %q, want ollama", e.ID())
	}
}

// TestOllamaEmbedderEmptyTextsReturnsNil verifies that Embed on an empty
// slice returns nil, nil (no HTTP call).
func TestOllamaEmbedderEmptyTextsReturnsNil(t *testing.T) {
	e := NewOllamaEmbedder("http://localhost:11434")
	// Point the client at a fake to detect any accidental HTTP call.
	e.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		t.Fatal("HTTP call made for empty texts — should not happen")
		return nil, nil
	})
	vecs, err := e.Embed(context.Background(), "nomic-embed-text", []string{})
	if err != nil {
		t.Fatalf("Embed(empty): %v", err)
	}
	if vecs != nil {
		t.Errorf("expected nil, got %v", vecs)
	}
}

// TestOllamaEmbedderHTTPErrorReturnsError verifies that a 4xx status from the
// /api/embed endpoint is surfaced as an error.
func TestOllamaEmbedderHTTPErrorReturnsError(t *testing.T) {
	e := NewOllamaEmbedder("http://ollama.test")
	e.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(404, `{"error":"model not found"}`), nil
	})
	_, err := e.Embed(context.Background(), "bad-model", []string{"hello"})
	if err == nil {
		t.Fatal("expected error on HTTP 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404, got: %v", err)
	}
}

// TestOllamaEmbedderCountMismatchReturnsError verifies that when the response
// contains a different number of embeddings than requested texts, an error is
// returned.
func TestOllamaEmbedderCountMismatchReturnsError(t *testing.T) {
	e := NewOllamaEmbedder("http://ollama.test")
	e.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		// Return 1 embedding but 2 were requested.
		return jsonResponse(200, `{"embeddings":[[0.1,0.2]]}`), nil
	})
	_, err := e.Embed(context.Background(), "nomic", []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error on embedding count mismatch")
	}
}

// TestOllamaEmbedderDimCacheHit verifies that Dim returns the cached value
// without making an HTTP call when the dimension was already computed.
func TestOllamaEmbedderDimCacheHit(t *testing.T) {
	e := NewOllamaEmbedder("http://ollama.test")
	e.dimMu.Lock()
	e.dimCache["nomic"] = 768
	e.dimMu.Unlock()

	// Intercept any HTTP call — there must be none.
	e.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		t.Fatal("HTTP call made despite cached dim — should not happen")
		return nil, nil
	})

	d, err := e.Dim(context.Background(), "nomic")
	if err != nil {
		t.Fatalf("Dim: %v", err)
	}
	if d != 768 {
		t.Errorf("Dim = %d, want 768", d)
	}
}

// TestOllamaEmbedderEmbedWithAPIError verifies that an error in the JSON body
// ("error" field) is surfaced.
func TestOllamaEmbedderEmbedWithAPIError(t *testing.T) {
	e := NewOllamaEmbedder("http://ollama.test")
	e.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"error":"model 'xyz' not loaded"}`), nil
	})
	_, err := e.Embed(context.Background(), "xyz", []string{"hello"})
	if err == nil {
		t.Fatal("expected error from API error field")
	}
	if !strings.Contains(err.Error(), "xyz") {
		t.Errorf("error should mention model name, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// OpenAIEmbedder — basic paths
// ---------------------------------------------------------------------------

// TestOpenAIEmbedderID verifies the embedder ID.
func TestOpenAIEmbedderID(t *testing.T) {
	e := NewOpenAIEmbedder("", "")
	if e.ID() != "openai" {
		t.Errorf("ID = %q, want openai", e.ID())
	}
}

func TestOpenAICompatibleEmbedderPreservesProviderID(t *testing.T) {
	e := NewOpenAICompatibleEmbedder("openroute", "http://proxy.test/", "key")
	if e.ID() != "openroute" {
		t.Errorf("ID = %q, want openroute", e.ID())
	}
	if e.baseURL != "http://proxy.test" {
		t.Errorf("baseURL = %q, want trailing slash stripped", e.baseURL)
	}
}

// TestOpenAIEmbedderDefaultBaseURL verifies the default base URL.
func TestOpenAIEmbedderDefaultBaseURL(t *testing.T) {
	e := NewOpenAIEmbedder("", "")
	if e.baseURL != "https://api.openai.com" {
		t.Errorf("baseURL = %q, want https://api.openai.com", e.baseURL)
	}
}

// TestOpenAIEmbedderTrailingSlashStripped verifies that a trailing slash in the
// base URL is stripped so endpoint construction doesn't produce double slashes.
func TestOpenAIEmbedderTrailingSlashStripped(t *testing.T) {
	e := NewOpenAIEmbedder("http://custom.host/", "")
	if strings.HasSuffix(e.baseURL, "/") {
		t.Errorf("baseURL should have trailing slash stripped, got %q", e.baseURL)
	}
}

// TestOpenAIEmbedderEmptyTextsReturnsNil verifies that Embed on empty texts
// returns nil without making an HTTP call.
func TestOpenAIEmbedderEmptyTextsReturnsNil(t *testing.T) {
	e := NewOpenAIEmbedder("http://openai.test", "key")
	e.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		t.Fatal("HTTP call for empty texts — should not happen")
		return nil, nil
	})
	vecs, err := e.Embed(context.Background(), "text-embedding-3-small", []string{})
	if err != nil {
		t.Fatalf("Embed(empty): %v", err)
	}
	if vecs != nil {
		t.Errorf("expected nil, got %v", vecs)
	}
}

// TestOpenAIEmbedderHTTPErrorReturnsError verifies a non-2xx status is an error.
func TestOpenAIEmbedderHTTPErrorReturnsError(t *testing.T) {
	e := NewOpenAIEmbedder("http://openai.test", "key")
	e.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(401, `{"error":{"message":"invalid key"}}`), nil
	})
	_, err := e.Embed(context.Background(), "text-embedding-3-small", []string{"hi"})
	if err == nil {
		t.Fatal("expected error on HTTP 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401, got: %v", err)
	}
}

// TestOpenAIEmbedderAPIErrorFieldSurfaced verifies that the JSON "error" field
// in the response body is propagated as an error.
func TestOpenAIEmbedderAPIErrorFieldSurfaced(t *testing.T) {
	e := NewOpenAIEmbedder("http://openai.test", "key")
	e.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"error":{"message":"quota exceeded"}}`), nil
	})
	_, err := e.Embed(context.Background(), "text-embedding-3-small", []string{"hi"})
	if err == nil {
		t.Fatal("expected error from API error field")
	}
	if !strings.Contains(err.Error(), "quota exceeded") {
		t.Errorf("error should mention 'quota exceeded', got: %v", err)
	}
}

// TestOpenAIEmbedderCountMismatchReturnsError verifies that when fewer
// embeddings are returned than requested texts, an error is produced.
func TestOpenAIEmbedderCountMismatchReturnsError(t *testing.T) {
	e := NewOpenAIEmbedder("http://openai.test", "key")
	e.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"data":[{"embedding":[0.1,0.2]}]}`), nil
	})
	_, err := e.Embed(context.Background(), "text-embedding-3-small", []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error on count mismatch")
	}
}

// TestOpenAIEmbedderDimCacheHit verifies that Dim uses the cache and avoids
// an HTTP roundtrip when the dimension is already known.
func TestOpenAIEmbedderDimCacheHit(t *testing.T) {
	e := NewOpenAIEmbedder("http://openai.test", "key")
	e.dimMu.Lock()
	e.dimCache["text-embedding-3-small"] = 1536
	e.dimMu.Unlock()

	e.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		t.Fatal("HTTP call despite cached dim")
		return nil, nil
	})

	d, err := e.Dim(context.Background(), "text-embedding-3-small")
	if err != nil {
		t.Fatalf("Dim: %v", err)
	}
	if d != 1536 {
		t.Errorf("Dim = %d, want 1536", d)
	}
}

// TestOpenAIEmbedderSuccessfulEmbedReturnsCachedDim verifies that a successful
// Embed call populates the dim cache so a subsequent Dim call doesn't probe.
func TestOpenAIEmbedderSuccessfulEmbedReturnsCachedDim(t *testing.T) {
	e := NewOpenAIEmbedder("http://openai.test", "key")
	calls := 0
	e.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(200, `{"data":[{"embedding":[0.1,0.2,0.3]}]}`), nil
	})

	// First call: actual embed.
	_, err := e.Embed(context.Background(), "my-model", []string{"probe"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	// Second call via Dim: should use cache, no extra HTTP call.
	e.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		t.Fatal("HTTP call despite prior Embed caching the dim")
		return nil, nil
	})
	d, err := e.Dim(context.Background(), "my-model")
	if err != nil {
		t.Fatalf("Dim after Embed: %v", err)
	}
	if d != 3 {
		t.Errorf("Dim = %d, want 3", d)
	}
}

// ---------------------------------------------------------------------------
// GoogleEmbedder — basic paths
// ---------------------------------------------------------------------------

func TestGoogleCompatibleEmbedderPreservesProviderID(t *testing.T) {
	e := NewGoogleCompatibleEmbedder("gemini", "http://google.test/", "key")
	if e.ID() != "gemini" {
		t.Errorf("ID = %q, want gemini", e.ID())
	}
	if e.baseURL != "http://google.test" {
		t.Errorf("baseURL = %q, want trailing slash stripped", e.baseURL)
	}
}

func TestGoogleEmbedderSuccessfulEmbedReturnsCachedDim(t *testing.T) {
	e := NewGoogleEmbedder("http://google.test", "key")
	calls := 0
	e.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/v1beta/models/gemini-embedding-001:embedContent" {
			t.Errorf("path = %q, want Gemini embedContent endpoint", r.URL.Path)
		}
		if r.URL.Query().Get("key") != "key" {
			t.Errorf("missing key query param")
		}
		return jsonResponse(200, `{"embedding":{"values":[0.1,0.2,0.3,0.4]}}`), nil
	})

	vecs, err := e.Embed(context.Background(), "", []string{"hello"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 4 {
		t.Fatalf("vecs = %#v, want one 4-dim vector", vecs)
	}

	e.client = clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		t.Fatal("HTTP call despite prior Embed caching the dim")
		return nil, nil
	})
	d, err := e.Dim(context.Background(), "gemini-embedding-001")
	if err != nil {
		t.Fatalf("Dim after Embed: %v", err)
	}
	if d != 4 {
		t.Errorf("Dim = %d, want 4", d)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

// ---------------------------------------------------------------------------
// cloneOptions / ollamaOptionsWithDefaults helpers
// ---------------------------------------------------------------------------

// TestCloneOptionsIsDeepCopy verifies that modifying the clone does not affect
// the original.
func TestCloneOptionsIsDeepCopy(t *testing.T) {
	orig := map[string]any{"a": 1, "b": "x"}
	clone := cloneOptions(orig)
	clone["a"] = 99
	clone["c"] = "new"

	if orig["a"] != 1 {
		t.Error("cloneOptions should not mutate original map")
	}
	if _, ok := orig["c"]; ok {
		t.Error("cloneOptions clone should not leak new keys back to original")
	}
}

// TestCloneOptionsEmptyMap verifies that cloning an empty map returns an empty
// (non-nil) map.
func TestCloneOptionsEmptyMap(t *testing.T) {
	clone := cloneOptions(map[string]any{})
	if clone == nil {
		t.Error("cloneOptions(empty) should return non-nil map")
	}
	if len(clone) != 0 {
		t.Errorf("cloneOptions(empty) = %v, want empty", clone)
	}
}

// TestOllamaOptionsWithDefaultsOverridesDefault verifies that caller-supplied
// values override the defaults (e.g. num_ctx=4096 can be overridden to 8192).
func TestOllamaOptionsWithDefaultsOverridesDefault(t *testing.T) {
	out := ollamaOptionsWithDefaults(map[string]any{"num_ctx": 8192})
	if out["num_ctx"] != 8192 {
		t.Errorf("num_ctx = %v, want 8192 (override)", out["num_ctx"])
	}
}

// TestOllamaOptionsWithDefaultsKeepsDefaultWhenNotOverridden verifies that
// default values are present when not overridden.
func TestOllamaOptionsWithDefaultsKeepsDefaultWhenNotOverridden(t *testing.T) {
	out := ollamaOptionsWithDefaults(map[string]any{})
	// Assert against the declared default rather than a copy of its value: this
	// test is about the default being APPLIED, and pinning the number here meant
	// changing the default looked like a regression.
	if out["num_ctx"] != DefaultOllamaOptions["num_ctx"] {
		t.Errorf("num_ctx = %v, want %v (default)", out["num_ctx"], DefaultOllamaOptions["num_ctx"])
	}
	if out["num_batch"] != DefaultOllamaOptions["num_batch"] {
		t.Errorf("num_batch = %v, want %v (default)", out["num_batch"], DefaultOllamaOptions["num_batch"])
	}
}

// The default must be able to hold a Soulacy agent. At 4096 it could not: the
// shared Operating Contract alone is ~835 tokens before any role prompt, tool
// schema or tool result, and real runs were measured at 9.5k–10.8k prompt
// tokens — every one of them silently truncated by Ollama and answered from a
// mangled prompt, surfacing as "(no final response produced)".
func TestDefaultContextWindowFitsAnAgentPrompt(t *testing.T) {
	got, ok := DefaultOllamaOptions["num_ctx"].(int)
	if !ok {
		t.Fatalf("num_ctx default is %T, want int", DefaultOllamaOptions["num_ctx"])
	}
	const observedWorkingSet = 10823 // measured from a real failing run
	if got < observedWorkingSet {
		t.Errorf("default num_ctx = %d, which cannot hold an observed %d-token agent prompt; "+
			"Ollama truncates silently, so this fails with an empty answer and no stated cause",
			got, observedWorkingSet)
	}
}

// ---------------------------------------------------------------------------
// toolCallFromFunc / toolCallWithID helpers
// ---------------------------------------------------------------------------

// TestToolCallFromFuncGeneratesUniqueIDs verifies that successive calls to
// toolCallFromFunc produce different IDs (monotonically increasing seq).
func TestToolCallFromFuncGeneratesUniqueIDs(t *testing.T) {
	tc1 := toolCallFromFunc("weather", map[string]any{"city": "A"})
	tc2 := toolCallFromFunc("weather", map[string]any{"city": "B"})
	if tc1.ID == tc2.ID {
		t.Errorf("toolCallFromFunc should produce unique IDs: %q == %q", tc1.ID, tc2.ID)
	}
}

// TestToolCallFromFuncIDContainsFuncName verifies that the generated ID embeds
// the function name for debuggability.
func TestToolCallFromFuncIDContainsFuncName(t *testing.T) {
	tc := toolCallFromFunc("my_tool", nil)
	if !strings.Contains(tc.ID, "my_tool") {
		t.Errorf("ID %q should contain function name 'my_tool'", tc.ID)
	}
}

// TestToolCallWithIDPreservesAllFields verifies that toolCallWithID faithfully
// stores the supplied id, name, and args.
func TestToolCallWithIDPreservesAllFields(t *testing.T) {
	args := map[string]any{"key": "value"}
	tc := toolCallWithID("call_abc", "search", args)
	if tc.ID != "call_abc" {
		t.Errorf("ID = %q, want call_abc", tc.ID)
	}
	if tc.Name != "search" {
		t.Errorf("Name = %q, want search", tc.Name)
	}
	if tc.Arguments["key"] != "value" {
		t.Errorf("Arguments = %v", tc.Arguments)
	}
}
