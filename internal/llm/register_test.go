package llm

import (
	"testing"

	"github.com/soulacy/soulacy/sdk/registry"
)

func TestRegistryOllamaFactory(t *testing.T) {
	p, ok, err := registry.NewProvider("ollama", map[string]any{
		"base_url": "http://localhost:11434", "model": "llama3",
	})
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if p.ID() != "ollama" {
		t.Fatalf("ID = %q", p.ID())
	}
	// no required keys — empty config builds the default provider
	if _, ok, err := registry.NewProvider("ollama", map[string]any{}); !ok || err != nil {
		t.Fatalf("empty cfg: ok=%v err=%v", ok, err)
	}
}

func TestRegistryOpenAIFactory(t *testing.T) {
	p, ok, err := registry.NewProvider("openai", map[string]any{
		"api_key": "sk-x", "model": "gpt-4o",
	})
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if p.ID() != "openai" {
		t.Fatalf("ID = %q", p.ID())
	}
	// custom id → OpenAI-compatible third-party endpoint
	p2, _, err := registry.NewProvider("openai", map[string]any{
		"id": "openrouter", "api_key": "k", "base_url": "https://openrouter.ai/api/v1",
	})
	if err != nil {
		t.Fatalf("custom id err: %v", err)
	}
	if p2.ID() != "openrouter" {
		t.Fatalf("custom ID = %q", p2.ID())
	}
	if _, _, err := registry.NewProvider("openai", map[string]any{}); err == nil {
		t.Fatal("missing api_key must error")
	}
}

func TestRegistryAnthropicFactory(t *testing.T) {
	p, ok, err := registry.NewProvider("anthropic", map[string]any{"api_key": "sk-ant"})
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if p.ID() != "anthropic" {
		t.Fatalf("ID = %q", p.ID())
	}
	if _, _, err := registry.NewProvider("anthropic", map[string]any{}); err == nil {
		t.Fatal("missing api_key must error")
	}
}

func TestRegistryGeminiFactory(t *testing.T) {
	for _, name := range []string{"gemini", "google"} {
		p, ok, err := registry.NewProvider(name, map[string]any{"api_key": "g-key"})
		if !ok || err != nil {
			t.Fatalf("%s: ok=%v err=%v", name, ok, err)
		}
		if p.ID() != "google" {
			t.Fatalf("%s: ID = %q", name, p.ID())
		}
		if _, _, err := registry.NewProvider(name, map[string]any{}); err == nil {
			t.Fatalf("%s: missing api_key must error", name)
		}
	}
}
