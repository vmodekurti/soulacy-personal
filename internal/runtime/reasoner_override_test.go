package runtime

import (
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

func TestReasoningDef_AgentAssignmentWinsOverReasonerFallback(t *testing.T) {
	e := &Engine{reasonerProvider: "ollama", reasonerModel: "qwen3:32b"}

	assigned := &agent.Definition{LLM: agent.LLMConfig{Provider: "google", Model: "gemini-2.5-pro"}}
	got := e.reasoningDef(assigned)
	if got.LLM.Provider != "google" || got.LLM.Model != "gemini-2.5-pro" {
		t.Fatalf("agent assignment must win: got %s/%s", got.LLM.Provider, got.LLM.Model)
	}

	providerOnly := &agent.Definition{LLM: agent.LLMConfig{Provider: "google"}}
	got = e.reasoningDef(providerOnly)
	if got.LLM.Provider != "google" || got.LLM.Model != "" {
		t.Fatalf("partial agent assignment must not be mixed with fallback: got %s/%s", got.LLM.Provider, got.LLM.Model)
	}

	modelOnly := &agent.Definition{LLM: agent.LLMConfig{Model: "gemini-2.5-pro"}}
	got = e.reasoningDef(modelOnly)
	if got.LLM.Provider != "" || got.LLM.Model != "gemini-2.5-pro" {
		t.Fatalf("partial agent assignment must remain authoritative: got %s/%s", got.LLM.Provider, got.LLM.Model)
	}
}

func TestReasoningDef_UsesReasonerFallbackForUnassignedAgent(t *testing.T) {
	e := &Engine{reasonerProvider: "ollama", reasonerModel: "qwen3:32b"}
	unassigned := &agent.Definition{}

	got := e.reasoningDef(unassigned)
	if got.LLM.Provider != "ollama" || got.LLM.Model != "qwen3:32b" {
		t.Fatalf("reasoner fallback should apply: got %s/%s", got.LLM.Provider, got.LLM.Model)
	}
	if unassigned.LLM.Provider != "" || unassigned.LLM.Model != "" {
		t.Fatal("reasoner fallback must not mutate the saved agent definition")
	}
}

func TestProviderSupportsNativeTools_UsesAgentBeforeReasonerFallback(t *testing.T) {
	e := &Engine{reasonerProvider: "ollama", reasonerModel: "qwen3:32b"}
	assigned := &agent.Definition{LLM: agent.LLMConfig{Provider: "anthropic", Model: "claude-sonnet"}}
	if !e.providerSupportsNativeTools(assigned) {
		t.Fatal("native-tool capability must be selected from the assigned agent provider")
	}
}
