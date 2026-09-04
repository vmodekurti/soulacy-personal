package llm

// Story E11: every built-in provider runs the official SDK conformance kit
// in CI, so the llm.Provider contract and the implementations cannot drift.

import (
	"testing"

	sdkllm "github.com/soulacy/soulacy/sdk/llm"
	"github.com/soulacy/soulacy/sdk/llm/providertest"
)

func TestConformance_Ollama(t *testing.T) {
	providertest.RunProviderSuite(t, func() sdkllm.Provider {
		return NewOllamaProvider("http://127.0.0.1:1", "llama3", "", nil)
	})
}

func TestConformance_OpenAI(t *testing.T) {
	providertest.RunProviderSuite(t, func() sdkllm.Provider {
		return NewOpenAIProviderWithOptions("openai", "http://127.0.0.1:1", "sk-test", "gpt-4o", "", nil)
	})
}

func TestConformance_Anthropic(t *testing.T) {
	providertest.RunProviderSuite(t, func() sdkllm.Provider {
		return NewAnthropicProviderWithOptions("http://127.0.0.1:1", "sk-ant-test", "claude-x", false, false, 0)
	})
}

func TestConformance_Gemini(t *testing.T) {
	providertest.RunProviderSuite(t, func() sdkllm.Provider {
		return NewGeminiProviderWithOptions("http://127.0.0.1:1", "g-test", "gemini-x", 0, "")
	})
}
