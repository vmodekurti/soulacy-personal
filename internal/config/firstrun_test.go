package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureBootstrapPreservesConfiguredAPIKey(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &Config{Server: ServerConfig{
		APIKey: "  sy_platform_generated  ",
		Host:   "0.0.0.0",
		Port:   8080,
	}}

	result, err := EnsureBootstrap(cfg, configPath)
	if err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	if result.Action != BootstrapWroteConfig {
		t.Fatalf("action = %v, want BootstrapWroteConfig", result.Action)
	}
	if result.APIKey != "sy_platform_generated" {
		t.Fatalf("result API key = %q, want configured key", result.APIKey)
	}
	if cfg.Server.APIKey != "sy_platform_generated" {
		t.Fatalf("config API key = %q, want configured key", cfg.Server.APIKey)
	}

	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(body), `api_key: "sy_platform_generated"`) {
		t.Fatalf("written config did not preserve configured key:\n%s", body)
	}
}

func TestEnsureBootstrapGeneratesAPIKeyWhenUnset(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &Config{}

	result, err := EnsureBootstrap(cfg, configPath)
	if err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	if result.Action != BootstrapWroteConfig {
		t.Fatalf("action = %v, want BootstrapWroteConfig", result.Action)
	}
	if !strings.HasPrefix(result.APIKey, "sy_") {
		t.Fatalf("generated API key = %q, want sy_ prefix", result.APIKey)
	}
	if cfg.Server.APIKey != result.APIKey {
		t.Fatalf("config API key = %q, result = %q", cfg.Server.APIKey, result.APIKey)
	}
}
