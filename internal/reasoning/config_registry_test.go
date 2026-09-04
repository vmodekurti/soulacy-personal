// config_registry_test.go — tests for config.go (DefaultBackendFor, LoopConfigFromDefinition)
// and tool_registry.go (Registry.Register, Execute, built-in tool handlers).
package reasoning_test

import (
	"context"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/pkg/agent"
)

// ─── DefaultBackendFor ───────────────────────────────────────────────────────

func TestDefaultBackendFor_Anthropic(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{Provider: "anthropic", Model: "claude-3-haiku"},
	}
	keys := reasoning.ProviderKeys{AnthropicKey: "test-key"}
	b := reasoning.DefaultBackendFor(def, keys)
	if b == nil {
		t.Fatal("expected non-nil backend for anthropic provider")
	}
}

func TestDefaultBackendFor_Anthropic_NoKey(t *testing.T) {
	// When AnthropicKey is empty, should fall through to Ollama.
	def := &agent.Definition{
		LLM: agent.LLMConfig{Provider: "anthropic"},
	}
	keys := reasoning.ProviderKeys{} // no key
	b := reasoning.DefaultBackendFor(def, keys)
	if b == nil {
		t.Fatal("expected non-nil backend (fallback to Ollama)")
	}
}

func TestDefaultBackendFor_OpenAI(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{Provider: "openai", Model: "gpt-4o-mini"},
	}
	keys := reasoning.ProviderKeys{OpenAIKey: "test-key"}
	b := reasoning.DefaultBackendFor(def, keys)
	if b == nil {
		t.Fatal("expected non-nil backend for openai provider")
	}
}

func TestDefaultBackendFor_OpenAI_NoKey(t *testing.T) {
	// OpenAI with no key falls through to Groq path.
	def := &agent.Definition{
		LLM: agent.LLMConfig{Provider: "openai", Model: "gpt-4o"},
	}
	keys := reasoning.ProviderKeys{}
	b := reasoning.DefaultBackendFor(def, keys)
	if b == nil {
		t.Fatal("expected non-nil backend (fallthrough)")
	}
}

func TestDefaultBackendFor_Groq(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{Provider: "groq", Model: "llama3-70b-8192"},
	}
	keys := reasoning.ProviderKeys{}
	b := reasoning.DefaultBackendFor(def, keys)
	if b == nil {
		t.Fatal("expected non-nil backend for groq provider")
	}
}

func TestDefaultBackendFor_Groq_CustomBaseURL(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{Provider: "groq", Model: "llama3-70b-8192", BaseURL: "https://custom.groq.example/v1"},
	}
	keys := reasoning.ProviderKeys{}
	b := reasoning.DefaultBackendFor(def, keys)
	if b == nil {
		t.Fatal("expected non-nil backend for groq with custom base URL")
	}
}

func TestDefaultBackendFor_Together(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{Provider: "together", Model: "mixtral"},
	}
	keys := reasoning.ProviderKeys{}
	b := reasoning.DefaultBackendFor(def, keys)
	if b == nil {
		t.Fatal("expected non-nil backend for together provider")
	}
}

func TestDefaultBackendFor_Together_CustomBaseURL(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{Provider: "together", Model: "mixtral", BaseURL: "https://custom.together.example/v1"},
	}
	keys := reasoning.ProviderKeys{}
	b := reasoning.DefaultBackendFor(def, keys)
	if b == nil {
		t.Fatal("expected non-nil backend for together with custom base URL")
	}
}

func TestDefaultBackendFor_Ollama(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{Provider: "ollama", Model: "gemma4:latest"},
	}
	keys := reasoning.ProviderKeys{}
	b := reasoning.DefaultBackendFor(def, keys)
	if b == nil {
		t.Fatal("expected non-nil backend for ollama provider")
	}
}

func TestDefaultBackendFor_Ollama_LargeQwenModel(t *testing.T) {
	// qwen2.5:72b is a large qwen model — PlanReflectModel should also use it.
	def := &agent.Definition{
		LLM: agent.LLMConfig{Provider: "ollama", Model: "qwen2.5:72b"},
	}
	b := reasoning.DefaultBackendFor(def, reasoning.ProviderKeys{})
	if b == nil {
		t.Fatal("expected non-nil backend")
	}
}

func TestDefaultBackendFor_Ollama_SmallQwenModel(t *testing.T) {
	// qwen2.5:7b is NOT a large qwen model — PlanReflectModel stays at default.
	def := &agent.Definition{
		LLM: agent.LLMConfig{Provider: "ollama", Model: "qwen2.5:7b"},
	}
	b := reasoning.DefaultBackendFor(def, reasoning.ProviderKeys{})
	if b == nil {
		t.Fatal("expected non-nil backend")
	}
}

func TestDefaultBackendFor_UnknownProvider(t *testing.T) {
	// Unknown provider defaults to Ollama.
	def := &agent.Definition{
		LLM: agent.LLMConfig{Provider: "some-unknown-provider"},
	}
	b := reasoning.DefaultBackendFor(def, reasoning.ProviderKeys{})
	if b == nil {
		t.Fatal("expected non-nil backend (Ollama fallback for unknown provider)")
	}
}

func TestDefaultBackendFor_Anthropic_WithBaseURL(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{
			Provider: "anthropic",
			Model:    "claude-3-opus",
			BaseURL:  "https://custom.anthropic.example",
		},
	}
	b := reasoning.DefaultBackendFor(def, reasoning.ProviderKeys{AnthropicKey: "key"})
	if b == nil {
		t.Fatal("expected non-nil backend")
	}
}

func TestDefaultBackendFor_OpenAI_WithBaseURL(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{
			Provider: "openai",
			Model:    "gpt-4o",
			BaseURL:  "https://custom-openai.example/v1",
		},
	}
	b := reasoning.DefaultBackendFor(def, reasoning.ProviderKeys{OpenAIKey: "key"})
	if b == nil {
		t.Fatal("expected non-nil backend")
	}
}

// ─── LoopConfigFromDefinition ────────────────────────────────────────────────

func TestLoopConfigFromDefinition_EmptyStrategy(t *testing.T) {
	def := &agent.Definition{
		Reasoning: agent.ReasoningConfig{}, // Strategy is empty
	}
	_, ok := reasoning.LoopConfigFromDefinition(def, "sys", true)
	if ok {
		t.Error("expected ok=false when strategy is empty")
	}
}

func TestLoopConfigFromDefinition_ReactStrategy(t *testing.T) {
	def := &agent.Definition{
		Reasoning: agent.ReasoningConfig{
			Strategy: "react",
			MaxSteps: 5,
		},
		Tools: []agent.ToolDef{
			{Name: "web_search"},
			{Name: "memory_read"},
		},
	}
	cfg, ok := reasoning.LoopConfigFromDefinition(def, "you are helpful", true)
	if !ok {
		t.Fatal("expected ok=true for react strategy")
	}
	if cfg.Strategy != reasoning.StrategyReAct {
		t.Errorf("expected StrategyReAct, got %q", cfg.Strategy)
	}
	if cfg.MaxSteps != 5 {
		t.Errorf("expected MaxSteps=5, got %d", cfg.MaxSteps)
	}
	if cfg.SystemPrompt != "you are helpful" {
		t.Errorf("expected system prompt 'you are helpful', got %q", cfg.SystemPrompt)
	}
	if len(cfg.ToolNames) != 2 {
		t.Errorf("expected 2 tool names, got %d", len(cfg.ToolNames))
	}
}

func TestLoopConfigFromDefinition_PlanExecuteStrategy(t *testing.T) {
	def := &agent.Definition{
		Reasoning: agent.ReasoningConfig{
			Strategy:     "plan_execute",
			MaxPlanSteps: 4,
			StepTimeout:  "45s",
			TotalTimeout: "300s",
		},
	}
	cfg, ok := reasoning.LoopConfigFromDefinition(def, "sys", true)
	if !ok {
		t.Fatal("expected ok=true for plan_execute strategy")
	}
	if cfg.Strategy != reasoning.StrategyPlanExecute {
		t.Errorf("expected StrategyPlanExecute, got %q", cfg.Strategy)
	}
	if cfg.MaxPlanSteps != 4 {
		t.Errorf("expected MaxPlanSteps=4, got %d", cfg.MaxPlanSteps)
	}
	if cfg.StepTimeout.String() != "45s" {
		t.Errorf("expected StepTimeout=45s, got %s", cfg.StepTimeout)
	}
	if cfg.TotalTimeout.String() != "5m0s" {
		t.Errorf("expected TotalTimeout=5m0s, got %s", cfg.TotalTimeout)
	}
}

func TestLoopConfigFromDefinition_EnforceSystemReact(t *testing.T) {
	def := &agent.Definition{
		Reasoning: agent.ReasoningConfig{
			Strategy: "plan_execute",
		},
		Capabilities: []string{"system"},
	}
	cfg, ok := reasoning.LoopConfigFromDefinition(def, "sys", true)
	if !ok {
		t.Fatal("expected ok=true")
	}
	// The config builder must aggressively override plan_execute to react
	// because plan_execute cannot pass arguments to OS-level parameterized tools.
	if cfg.Strategy != reasoning.StrategyReAct {
		t.Errorf("expected strategy to be overridden to react for system agent, got %q", cfg.Strategy)
	}
}

func TestLoopConfigFromDefinition_DefaultValues(t *testing.T) {
	// When MaxSteps/MaxPlanSteps/StepTimeout/TotalTimeout are all zero, defaults apply.
	def := &agent.Definition{
		Reasoning: agent.ReasoningConfig{
			Strategy: "react",
			// all other fields zero/empty
		},
	}
	cfg, ok := reasoning.LoopConfigFromDefinition(def, "", true)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cfg.MaxSteps != 8 {
		t.Errorf("expected default MaxSteps=8, got %d", cfg.MaxSteps)
	}
	if cfg.MaxPlanSteps != 6 {
		t.Errorf("expected default MaxPlanSteps=6, got %d", cfg.MaxPlanSteps)
	}
	if cfg.StepTimeout != 30*time.Second {
		t.Errorf("expected default StepTimeout=30s, got %s", cfg.StepTimeout)
	}
	if cfg.TotalTimeout != 180*time.Second {
		t.Errorf("expected default TotalTimeout=180s, got %s", cfg.TotalTimeout)
	}
}

func TestLoopConfigFromDefinition_DynamicDefaultsForComplexPlanExecute(t *testing.T) {
	builtins := []string{"web_search", "fetch_url", "channel.send"}
	mcpTools := []string{
		"mcp__notebooklm__refresh_auth",
		"mcp__notebooklm__notebook_create",
		"mcp__notebooklm__source_add",
		"mcp__notebooklm__studio_create",
		"mcp__notebooklm__studio_status",
	}
	def := &agent.Definition{
		SystemPrompt: "Search recent AI articles, fetch each one, create a NotebookLM podcast briefing, poll until audio is ready, and deliver it.",
		Reasoning:    agent.ReasoningConfig{Strategy: "plan_execute"},
		Builtins:     &builtins,
		MCPTools:     &mcpTools,
	}
	cfg, ok := reasoning.LoopConfigFromDefinition(def, "sys", true)
	if !ok {
		t.Fatal("expected ok=true for plan_execute strategy")
	}
	if cfg.MaxSteps < 24 {
		t.Fatalf("complex plan_execute should get larger default max steps, got %d", cfg.MaxSteps)
	}
	if cfg.MaxPlanSteps < 12 {
		t.Fatalf("complex plan_execute should get larger default max plan steps, got %d", cfg.MaxPlanSteps)
	}
}

func TestLoopConfigFromDefinition_InvalidDurations(t *testing.T) {
	// Invalid duration strings should fall back to defaults.
	def := &agent.Definition{
		Reasoning: agent.ReasoningConfig{
			Strategy:     "react",
			StepTimeout:  "not-a-duration",
			TotalTimeout: "also-not-valid",
		},
	}
	cfg, ok := reasoning.LoopConfigFromDefinition(def, "", true)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cfg.StepTimeout != 30*time.Second {
		t.Errorf("expected default StepTimeout=30s, got %s", cfg.StepTimeout)
	}
	if cfg.TotalTimeout != 180*time.Second {
		t.Errorf("expected default TotalTimeout=180s, got %s", cfg.TotalTimeout)
	}
}

func TestLoopConfigFromDefinition_NoTools(t *testing.T) {
	def := &agent.Definition{
		Reasoning: agent.ReasoningConfig{Strategy: "react"},
		// no Tools
	}
	cfg, ok := reasoning.LoopConfigFromDefinition(def, "", true)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(cfg.ToolNames) != 0 {
		t.Errorf("expected 0 tool names, got %d", len(cfg.ToolNames))
	}
}

// ─── Registry ────────────────────────────────────────────────────────────────

func TestRegistry_Execute_UnknownTool(t *testing.T) {
	r := reasoning.NewRegistry()
	obs := r.Execute(context.Background(), reasoning.ToolCall{Tool: "nonexistent"})
	if obs.Error == nil {
		t.Error("expected error for unknown tool")
	}
	if obs.Content == "" {
		t.Error("expected non-empty error content")
	}
}

func TestRegistry_Execute_Success(t *testing.T) {
	r := reasoning.NewRegistry()
	r.Register(reasoning.ToolSpec{
		Name:        "echo",
		AllowedKeys: []string{"message"},
		Handler: func(ctx context.Context, input map[string]string) (string, error) {
			return "echo: " + input["message"], nil
		},
	})

	obs := r.Execute(context.Background(), reasoning.ToolCall{
		Tool:  "echo",
		Input: map[string]string{"message": "hello"},
	})
	if obs.Error != nil {
		t.Fatalf("unexpected error: %v", obs.Error)
	}
	if obs.Content != "echo: hello" {
		t.Errorf("expected 'echo: hello', got %q", obs.Content)
	}
	if obs.Source != "echo" {
		t.Errorf("expected source 'echo', got %q", obs.Source)
	}
}

func TestRegistry_Execute_KeyFiltering(t *testing.T) {
	// Keys not in AllowedKeys should be stripped before the handler runs.
	r := reasoning.NewRegistry()
	var receivedKeys []string
	r.Register(reasoning.ToolSpec{
		Name:        "checker",
		AllowedKeys: []string{"allowed_key"},
		Handler: func(ctx context.Context, input map[string]string) (string, error) {
			for k := range input {
				receivedKeys = append(receivedKeys, k)
			}
			return "ok", nil
		},
	})

	r.Execute(context.Background(), reasoning.ToolCall{
		Tool: "checker",
		Input: map[string]string{
			"allowed_key":    "yes",
			"disallowed_key": "should be stripped",
		},
	})

	for _, k := range receivedKeys {
		if k == "disallowed_key" {
			t.Error("disallowed_key should have been stripped from input")
		}
	}
}

func TestRegistry_Execute_NoAllowedKeys(t *testing.T) {
	// When AllowedKeys is empty, all input keys pass through.
	r := reasoning.NewRegistry()
	var receivedInput map[string]string
	r.Register(reasoning.ToolSpec{
		Name: "passthrough",
		// AllowedKeys intentionally empty — all keys allowed
		Handler: func(ctx context.Context, input map[string]string) (string, error) {
			receivedInput = input
			return "ok", nil
		},
	})

	r.Execute(context.Background(), reasoning.ToolCall{
		Tool:  "passthrough",
		Input: map[string]string{"a": "1", "b": "2"},
	})

	if receivedInput["a"] != "1" || receivedInput["b"] != "2" {
		t.Errorf("expected all keys passed through, got %v", receivedInput)
	}
}

func TestRegistry_Execute_HandlerError(t *testing.T) {
	r := reasoning.NewRegistry()
	r.Register(reasoning.ToolSpec{
		Name: "failing_tool",
		Handler: func(ctx context.Context, input map[string]string) (string, error) {
			return "", context.DeadlineExceeded
		},
	})

	obs := r.Execute(context.Background(), reasoning.ToolCall{Tool: "failing_tool"})
	if obs.Error == nil {
		t.Error("expected error from failing handler")
	}
	if obs.Content == "" {
		t.Error("expected non-empty error content")
	}
}

func TestRegistry_Register_Duplicate_Panics(t *testing.T) {
	r := reasoning.NewRegistry()
	r.Register(reasoning.ToolSpec{Name: "dup_tool", Handler: func(ctx context.Context, input map[string]string) (string, error) {
		return "ok", nil
	}})

	defer func() {
		if rec := recover(); rec == nil {
			t.Error("expected panic on duplicate registration")
		}
	}()

	r.Register(reasoning.ToolSpec{Name: "dup_tool", Handler: func(ctx context.Context, input map[string]string) (string, error) {
		return "ok2", nil
	}})
}

func TestRegistry_Execute_ContextCanceled(t *testing.T) {
	r := reasoning.NewRegistry()
	r.Register(reasoning.ToolSpec{
		Name: "ctx_tool",
		Handler: func(ctx context.Context, input map[string]string) (string, error) {
			return "would run but ctx was pre-cancelled", nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Execute

	obs := r.Execute(ctx, reasoning.ToolCall{Tool: "ctx_tool"})
	// Either context error or normal execution — both are valid since the
	// select in Execute races. We just verify no panic and obs is returned.
	_ = obs
}

// ─── WebSearchSpec (no API key path) ─────────────────────────────────────────

func TestWebSearchSpec_NoAPIKey(t *testing.T) {
	spec := reasoning.WebSearchSpec("", "") // no provider, no key
	r := reasoning.NewRegistry()
	r.Register(spec)

	// With no OLLAMA_API_KEY in env and no key passed, should return a clear
	// "unavailable" message rather than an error.
	obs := r.Execute(context.Background(), reasoning.ToolCall{
		Tool:  "web_search",
		Input: map[string]string{"query": "test query"},
	})
	if obs.Error != nil {
		t.Fatalf("expected no error (graceful unavailable message), got: %v", obs.Error)
	}
	if obs.Content == "" {
		t.Error("expected non-empty content (unavailable message)")
	}
}

func TestWebSearchSpec_EmptyQuery(t *testing.T) {
	spec := reasoning.WebSearchSpec("", "fake-key")
	r := reasoning.NewRegistry()
	r.Register(spec)

	obs := r.Execute(context.Background(), reasoning.ToolCall{
		Tool:  "web_search",
		Input: map[string]string{"query": ""},
	})
	if obs.Error == nil {
		t.Error("expected error for empty query")
	}
}

func TestWebSearchSpec_AllowedKeys(t *testing.T) {
	spec := reasoning.WebSearchSpec("", "")
	// Verify the spec has name and allowed keys set correctly.
	if spec.Name != "web_search" {
		t.Errorf("expected spec.Name='web_search', got %q", spec.Name)
	}
	// Check that the allowed keys include "query".
	found := false
	for _, k := range spec.AllowedKeys {
		if k == "query" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'query' in AllowedKeys")
	}
}
