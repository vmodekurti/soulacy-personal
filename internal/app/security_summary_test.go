package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
)

func TestEffectiveSecuritySummaryContainsNoSecretValues(t *testing.T) {
	secret := "never-log-this-provider-key"
	cfg := &config.Config{Server: config.ServerConfig{Host: "127.0.0.1", APIKey: secret}, Auth: config.AuthConfig{Mode: "apikey", JWTSecret: secret}, LLM: config.LLMConfig{Providers: map[string]config.ProviderConfig{"x": {APIKey: secret}}}}
	b, _ := json.Marshal(effectiveSecuritySummary(cfg, true))
	if strings.Contains(string(b), secret) {
		t.Fatalf("security summary leaked a secret: %s", b)
	}
	if !strings.Contains(string(b), `"auth_effective":true`) {
		t.Fatalf("summary missing posture: %s", b)
	}
}
