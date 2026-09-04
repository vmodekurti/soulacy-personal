package config

// Story E17: arbitrary plugin-specific settings parse into PluginsConfig
// without unmarshalling errors — the shape under each plugin key is opaque
// to the core parser.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPluginsConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
server:
  api_key: k
plugins_config:
  weather-bot:
    units: metric
    retries: 3
    nested:
      endpoint: https://api.example.com
  matrix-bridge:
    homeserver: https://matrix.org
`), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, _, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wb := cfg.PluginsConfig["weather-bot"]
	if wb == nil {
		t.Fatal("weather-bot section missing")
	}
	if wb["units"] != "metric" {
		t.Fatalf("units = %v", wb["units"])
	}
	nested, ok := wb["nested"].(map[string]any)
	if !ok || nested["endpoint"] != "https://api.example.com" {
		t.Fatalf("nested = %v", wb["nested"])
	}
	if cfg.PluginsConfig["matrix-bridge"]["homeserver"] != "https://matrix.org" {
		t.Fatalf("matrix-bridge = %v", cfg.PluginsConfig["matrix-bridge"])
	}
}

func TestLoadNoPluginsConfigIsNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  api_key: k\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, _, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.PluginsConfig) != 0 {
		t.Fatalf("PluginsConfig = %v, want empty", cfg.PluginsConfig)
	}
}
