package llm

import (
	"github.com/soulacy/soulacy/sdk/registry"
	"testing"
)

func TestKeylessLocalOpenAIProvider(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:11434/v1", "http://localhost:1234/v1", "http://[::1]:8000/v1"} {
		p, ok, err := registry.NewProvider("openai", map[string]any{"id": "local-compatible", "base_url": endpoint, "model": "local"})
		if !ok || err != nil || p.ID() != "local-compatible" {
			t.Fatalf("local endpoint %s: %v %v", endpoint, ok, err)
		}
	}
	for _, endpoint := range []string{"", "https://api.openai.com/v1", "http://localhost.example/v1", "file:///tmp/model", "http://user:password@localhost/v1", "http://localhost/v1?api_key=secret"} {
		if AllowsUnauthenticatedOpenAIEndpoint(endpoint) {
			t.Errorf("allowed %s", endpoint)
		}
		if _, _, err := registry.NewProvider("openai", map[string]any{"base_url": endpoint}); err == nil {
			t.Errorf("keyless cloud/unsafe endpoint accepted: %s", endpoint)
		}
	}
}
