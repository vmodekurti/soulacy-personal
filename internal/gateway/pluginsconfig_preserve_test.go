package gateway

// Story E17: config writes must preserve the plugins_config block
// unmutated. handlePatchConfig reads the RAW yaml into a map, applies only
// the typed patch fields, and writes the whole map back — this test pins
// that unknown top-level blocks survive the round trip byte-for-value.

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPatchConfigPreservesPluginsConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	seed := `
server:
  api_key: secret
plugins_config:
  weather-bot:
    units: metric
    api_key: sk-keep-me
    nested:
      endpoint: https://api.example.com
`
	if err := os.WriteFile(cfgPath, []byte(seed), 0600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)

	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"llm":{"default_provider":"anthropic"}}`)
	if status != http.StatusOK {
		t.Fatalf("patch status = %d body=%v", status, body)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	pc, ok := m["plugins_config"].(map[string]any)
	if !ok {
		t.Fatalf("plugins_config block lost on write: %v", m)
	}
	wb, ok := pc["weather-bot"].(map[string]any)
	if !ok {
		t.Fatalf("weather-bot section lost: %v", pc)
	}
	// values preserved UNREDACTED on disk (redaction is read-API-only)
	if wb["api_key"] != "sk-keep-me" {
		t.Fatalf("plugin secret mutated on disk: %v", wb["api_key"])
	}
	if wb["units"] != "metric" {
		t.Fatalf("units mutated: %v", wb["units"])
	}
	nested, ok := wb["nested"].(map[string]any)
	if !ok || nested["endpoint"] != "https://api.example.com" {
		t.Fatalf("nested block mutated: %v", wb["nested"])
	}
	// and the patched field actually landed
	llm, _ := m["llm"].(map[string]any)
	if llm == nil || llm["default_provider"] != "anthropic" {
		t.Fatalf("patch did not apply: %v", m["llm"])
	}
}
