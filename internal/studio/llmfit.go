package studio

import "strings"

// llmfit.go — model classifications used by the reasoning-agent contract
// (Story 2b, Cohort C). These are intentionally duplicated from
// internal/agentvalidate rather than shared through a leaf package: the two
// packages have different lifecycles (agentvalidate blocks on Save, Studio
// warns during authoring) and both have narrow local test surfaces. Keep
// these lists conservative — a false positive here nags every save.

// isEmbeddingModel reports whether the model looks like an embedding model
// rather than a chat/instruct one. Embedding models can't drive a reasoning
// loop at all, so the contract blocks on them.
func isEmbeddingModel(model string) bool {
	m := strings.ToLower(model)
	for _, marker := range []string{"embed", "nomic", "bge", "e5-", "minilm", "sentence"} {
		if strings.Contains(m, marker) {
			return true
		}
	}
	return false
}

// weakJSONModel flags small / poorly instruction-tuned local models whose
// JSON tool-call output is unreliable in a reasoning loop. Provider is
// currently unused (kept for symmetry with the caller and future tuning).
func weakJSONModel(provider, model string) bool { //nolint:unparam
	m := strings.ToLower(model)
	weak := []string{
		"llama3.2:1b", "llama3.2:3b",
		"gemma:2b", "gemma2:2b",
		"phi3:mini",
		"mistral:7b",
		"neural-chat",
		"stablelm",
		"tinyllama",
	}
	for _, w := range weak {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

// smallContextModel flags models known to have a narrow context window that
// tends to blow through the ceiling once a reasoning loop accumulates a few
// turns of tool output. Provider is currently unused.
func smallContextModel(provider, model string) bool { //nolint:unparam
	m := strings.ToLower(model)
	small := []string{
		"llama3.2:1b", "llama3.2:3b",
		"gemma:2b", "gemma2:2b",
		"phi3:mini",
		"mistral:7b",
		"tinyllama",
	}
	for _, s := range small {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// reasoningModelSuggestions returns concrete better-model suggestions for the
// operator to consider when their current model is flagged as weak. Provider
// is match-precise so the suggestion set is achievable for their current
// dropdown.
func reasoningModelSuggestions(provider string) []string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		return []string{"claude-sonnet-4-6", "claude-haiku-4-5-20251001"}
	case "openai":
		return []string{"gpt-4o", "gpt-4o-mini"}
	case "groq":
		return []string{"llama-3.3-70b-versatile", "mixtral-8x7b-32768"}
	default:
		return []string{"qwen2.5:72b", "qwen2.5:32b", "gemma4:latest", "llama3.3:70b"}
	}
}
