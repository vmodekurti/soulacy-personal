package llm

import (
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/cfgmap"
	sdkllm "github.com/soulacy/soulacy/sdk/llm"
	"github.com/soulacy/soulacy/sdk/registry"
)

// Registry self-registration for the built-in LLM providers (Story E10).
// The host resolves llm.providers config entries through
// registry.NewProvider; these factories replace the hardcoded constructor
// calls in cmd/soulacy.
//
// Common config keys: base_url, api_key, model. Provider-specific keys are
// documented per factory below.
func init() {
	// ollama — no required keys; model defaults to "llama3".
	// Keys: base_url, model, keep_alive, options (map).
	registry.MustRegisterProvider("ollama", func(cfg map[string]any) (sdkllm.Provider, error) {
		return NewOllamaProvider(
			cfgmap.Str(cfg, "base_url", ""),
			cfgmap.Str(cfg, "model", "llama3"),
			cfgmap.Str(cfg, "keep_alive", ""),
			cfgmap.Map(cfg, "options"),
		), nil
	})

	// openai — also serves any OpenAI-compatible endpoint (OpenRouter /
	// Together / Groq / vLLM) under a custom "id".
	// Keys: id (default "openai"), api_key (required), base_url (default
	// https://api.openai.com/v1), model, organization, parallel_tool_calls.
	registry.MustRegisterProvider("openai", func(cfg map[string]any) (sdkllm.Provider, error) {
		apiKey := cfgmap.Str(cfg, "api_key", "")
		if apiKey == "" {
			return nil, fmt.Errorf("openai: config key %q is required", "api_key")
		}
		p := NewOpenAIProviderWithOptions(
			cfgmap.Str(cfg, "id", "openai"),
			cfgmap.Str(cfg, "base_url", "https://api.openai.com/v1"),
			apiKey,
			cfgmap.Str(cfg, "model", ""),
			cfgmap.Str(cfg, "organization", ""),
			cfgmap.BoolPtr(cfg, "parallel_tool_calls"),
		)
		p.SetRequestTimeout(providerRequestTimeout(cfg))
		return p, nil
	})

	// anthropic — native Messages API.
	// Keys: api_key (required), base_url, model, prompt_caching,
	// extended_thinking, thinking_budget.
	registry.MustRegisterProvider("anthropic", func(cfg map[string]any) (sdkllm.Provider, error) {
		apiKey := cfgmap.Str(cfg, "api_key", "")
		if apiKey == "" {
			return nil, fmt.Errorf("anthropic: config key %q is required", "api_key")
		}
		return NewAnthropicProviderWithOptions(
			cfgmap.Str(cfg, "base_url", ""),
			apiKey,
			cfgmap.Str(cfg, "model", ""),
			cfgmap.Bool(cfg, "prompt_caching", false),
			cfgmap.Bool(cfg, "extended_thinking", false),
			cfgmap.Int(cfg, "thinking_budget", 0),
		), nil
	})

	// gemini — Google Gemini; registered under both "gemini" and the
	// config-file key "google" (the provider's ID() is "google").
	// Keys: api_key (required), base_url, model, thinking_budget,
	// safety_level.
	gemini := func(cfg map[string]any) (sdkllm.Provider, error) {
		apiKey := cfgmap.Str(cfg, "api_key", "")
		if apiKey == "" {
			return nil, fmt.Errorf("gemini: config key %q is required", "api_key")
		}
		return NewGeminiProviderWithOptions(
			cfgmap.Str(cfg, "base_url", ""),
			apiKey,
			cfgmap.Str(cfg, "model", ""),
			cfgmap.Int(cfg, "thinking_budget", 0),
			cfgmap.Str(cfg, "safety_level", ""),
		), nil
	}
	registry.MustRegisterProvider("gemini", gemini)
	registry.MustRegisterProvider("google", gemini)

	// nvidia — NVIDIA NIM / API catalog (OpenAI-compatible). Defaults base_url to
	// the hosted NVIDIA endpoint; point it at a local NIM container by setting
	// base_url. Keys: api_key (required, "nvapi-…"), base_url, model.
	registry.MustRegisterProvider("nvidia", func(cfg map[string]any) (sdkllm.Provider, error) {
		apiKey := cfgmap.Str(cfg, "api_key", "")
		if apiKey == "" {
			return nil, fmt.Errorf("nvidia: config key %q is required", "api_key")
		}
		p := NewOpenAIProviderWithOptions(
			cfgmap.Str(cfg, "id", "nvidia"),
			cfgmap.Str(cfg, "base_url", "https://integrate.api.nvidia.com/v1"),
			apiKey,
			cfgmap.Str(cfg, "model", ""),
			cfgmap.Str(cfg, "organization", ""),
			cfgmap.BoolPtr(cfg, "parallel_tool_calls"),
		)
		p.SetRequestTimeout(providerRequestTimeout(cfg))
		return p, nil
	})
}

// providerRequestTimeout reads an optional `request_timeout` config key (a Go
// duration string such as "300s" or "10m") and returns it. It returns 0 when
// the key is absent or unparseable, so the provider keeps its default.
func providerRequestTimeout(cfg map[string]any) time.Duration {
	s := strings.TrimSpace(cfgmap.Str(cfg, "request_timeout", ""))
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}
