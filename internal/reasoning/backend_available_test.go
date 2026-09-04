package reasoning

import "testing"

func TestBackendAvailable(t *testing.T) {
	withKeys := ProviderKeys{AnthropicKey: "a", OpenAIKey: "o"}
	noKeys := ProviderKeys{}

	cases := []struct {
		provider string
		keys     ProviderKeys
		want     bool
	}{
		{"anthropic", withKeys, true},
		{"anthropic", noKeys, false},
		{"openai", withKeys, true},
		{"openai", noKeys, false},
		{"groq", withKeys, true},
		{"together", withKeys, true},
		{"ollama", noKeys, true}, // local, no key needed
		{"Ollama", noKeys, true}, // case-insensitive
		{"google", withKeys, false},
		{"gemini", withKeys, false},
		{"grok", withKeys, false},
		{"", withKeys, false},       // unresolved/unknown
		{"madeup", withKeys, false}, // unknown provider
	}
	for _, c := range cases {
		if got := BackendAvailable(c.provider, c.keys); got != c.want {
			t.Errorf("BackendAvailable(%q) = %v, want %v", c.provider, got, c.want)
		}
	}
}
