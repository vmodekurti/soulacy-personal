package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadModelRoutingPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `
llm:
  default_provider: ollama
  routing:
    enabled: true
    default_mode: auto
    preset: balanced
    max_failovers: 2
    failure_threshold: 4
    cooldown: 45s
    routes:
      simple:
        - provider: ollama
          model: qwen3:8b
      complex:
        - provider: anthropic
          model: claude-test
    fallbacks:
      - provider: openai
        model: gpt-test
`
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.LLM.Routing.Enabled || cfg.LLM.Routing.FailureThreshold != 4 || cfg.LLM.Routing.Cooldown != "45s" {
		t.Fatalf("routing=%#v", cfg.LLM.Routing)
	}
	if got := cfg.LLM.Routing.Routes["complex"][0]; got.Provider != "anthropic" || got.Model != "claude-test" {
		t.Fatalf("complex route=%#v", got)
	}
}

func TestValidateRejectsUnknownRoutingClass(t *testing.T) {
	cfg := &Config{}
	cfg.LLM.Routing.Routes = map[string][]LLMRouteCandidate{"impossible": {{Provider: "ollama"}}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "unsupported task class") {
		t.Fatalf("expected invalid routing class, got %v", err)
	}
}
