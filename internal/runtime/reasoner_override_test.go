package runtime

import (
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

// A half-configured global reasoner override (provider set, model empty) must
// NOT switch a cloud agent onto a different provider — that carried the agent's
// model name (e.g. "gemini-2.5-pro") onto local Ollama and failed with
// "model not found". With the guard, such an agent keeps its own provider/model.
func TestReasoningDef_GuardsHalfConfiguredOverride(t *testing.T) {
	e := &Engine{reasonerProvider: "ollama", reasonerModel: ""}

	google := &agent.Definition{LLM: agent.LLMConfig{Provider: "google", Model: "gemini-2.5-pro"}}
	got := e.reasoningDef(google)
	if got.LLM.Provider != "google" || got.LLM.Model != "gemini-2.5-pro" {
		t.Fatalf("half-configured override must not switch provider: got %s/%s",
			got.LLM.Provider, got.LLM.Model)
	}

	// An agent already on the override provider is unaffected (no-op switch),
	// keeping its own model.
	local := &agent.Definition{LLM: agent.LLMConfig{Provider: "ollama", Model: "qwen3:32b"}}
	got = e.reasoningDef(local)
	if got.LLM.Provider != "ollama" || got.LLM.Model != "qwen3:32b" {
		t.Fatalf("same-provider override should keep the model: got %s/%s",
			got.LLM.Provider, got.LLM.Model)
	}

	// A FULL override (provider + model) does switch everything, as intended.
	e2 := &Engine{reasonerProvider: "ollama", reasonerModel: "qwen3:32b"}
	got = e2.reasoningDef(google)
	if got.LLM.Provider != "ollama" || got.LLM.Model != "qwen3:32b" {
		t.Fatalf("full override should apply: got %s/%s", got.LLM.Provider, got.LLM.Model)
	}
}
