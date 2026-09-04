package gateway

// Story 18: editable plugins_config via PATCH /api/v1/config. The critical
// invariant: the GUI edits the REDACTED view, so a full redacted GET →
// edit → PATCH round-trip must never overwrite real secrets on disk with
// "***" placeholders.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func seedPluginsConfig(t *testing.T) (string, *Server) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	seed := `
server:
  api_key: secret
plugins_config:
  weather-bot:
    units: metric
    api_key: sk-real-secret
    nested:
      endpoint: https://api.example.com
      token: tok-real
  other-plugin:
    speed: fast
`
	if err := os.WriteFile(cfgPath, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	// Mirror the on-disk plugins_config into the in-memory config so the
	// GET view (which renders s.cfg, like a freshly booted gateway) shows
	// the same data the file holds.
	s.cfg.PluginsConfig = map[string]map[string]any{
		"weather-bot": {
			"units":   "metric",
			"api_key": "sk-real-secret",
			"nested": map[string]any{
				"endpoint": "https://api.example.com",
				"token":    "tok-real",
			},
		},
		"other-plugin": {"speed": "fast"},
	}
	return cfgPath, s
}

func diskPluginsConfig(t *testing.T, cfgPath string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	pc, _ := m["plugins_config"].(map[string]any)
	if pc == nil {
		t.Fatalf("plugins_config lost: %v", m)
	}
	return pc
}

// The full GUI cycle: GET (redacted) → change one value → PATCH everything
// back, redactions included. Secrets stay intact on disk; the edit lands;
// unknown plugins/keys untouched.
func TestPatchPluginsConfig_RedactedRoundTripKeepsSecrets(t *testing.T) {
	cfgPath, s := seedPluginsConfig(t)

	// 1. GET — the GUI sees redacted values.
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/config", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get: %d", status)
	}
	view := body["plugins_config"].(map[string]any)
	wb := view["weather-bot"].(map[string]any)
	if wb["api_key"] != "***" {
		t.Fatalf("api_key not redacted in view: %v", wb["api_key"])
	}

	// 2. Edit one field in the redacted view and PATCH the whole section
	// back — exactly what a naive GUI round-trip does.
	wb["units"] = "imperial"
	patch, _ := json.Marshal(map[string]any{"plugins_config": map[string]any{"weather-bot": wb}})
	status, body = gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret", string(patch))
	if status != http.StatusOK {
		t.Fatalf("patch: %d %v", status, body)
	}

	pc := diskPluginsConfig(t, cfgPath)
	got := pc["weather-bot"].(map[string]any)
	if got["api_key"] != "sk-real-secret" {
		t.Errorf("SECRET OVERWRITTEN: api_key = %v", got["api_key"])
	}
	if got["units"] != "imperial" {
		t.Errorf("edit lost: units = %v", got["units"])
	}
	nested := got["nested"].(map[string]any)
	if nested["token"] != "tok-real" {
		t.Errorf("nested secret overwritten: %v", nested["token"])
	}
	if nested["endpoint"] != "https://api.example.com" {
		t.Errorf("nested value mutated: %v", nested["endpoint"])
	}
	// untouched plugin preserved
	if op, _ := pc["other-plugin"].(map[string]any); op == nil || op["speed"] != "fast" {
		t.Errorf("other-plugin mutated: %v", pc["other-plugin"])
	}
}

func TestPatchPluginsConfig_SetsNewSecretAndKeys(t *testing.T) {
	cfgPath, s := seedPluginsConfig(t)

	patch := `{"plugins_config": {"weather-bot": {"api_key": "sk-NEW", "retries": 3}, "brand-new": {"mode": "on"}}}`
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret", patch)
	if status != http.StatusOK {
		t.Fatalf("patch: %d %v", status, body)
	}

	pc := diskPluginsConfig(t, cfgPath)
	wb := pc["weather-bot"].(map[string]any)
	if wb["api_key"] != "sk-NEW" {
		t.Errorf("explicit new secret must land: %v", wb["api_key"])
	}
	if wb["units"] != "metric" {
		t.Errorf("unpatched key must survive a partial patch: %v", wb["units"])
	}
	if wb["retries"] != 3 {
		t.Errorf("new key lost: %v", wb["retries"])
	}
	if bn, _ := pc["brand-new"].(map[string]any); bn == nil || bn["mode"] != "on" {
		t.Errorf("new plugin section lost: %v", pc["brand-new"])
	}
}

func TestPatchPluginsConfig_NullDeletesKey(t *testing.T) {
	cfgPath, s := seedPluginsConfig(t)

	patch := `{"plugins_config": {"weather-bot": {"units": null}}}`
	status, _ := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret", patch)
	if status != http.StatusOK {
		t.Fatalf("patch: %d", status)
	}
	pc := diskPluginsConfig(t, cfgPath)
	wb := pc["weather-bot"].(map[string]any)
	if _, present := wb["units"]; present {
		t.Errorf("null must delete the key: %v", wb)
	}
	if wb["api_key"] != "sk-real-secret" {
		t.Errorf("secret lost during delete: %v", wb["api_key"])
	}
}

// The response view stays redacted after a patch (no secret echo).
func TestPatchPluginsConfig_ResponseStaysRedacted(t *testing.T) {
	_, s := seedPluginsConfig(t)
	patch := `{"plugins_config": {"weather-bot": {"api_key": "sk-NEW"}}}`
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret", patch)
	if status != http.StatusOK {
		t.Fatalf("patch: %d", status)
	}
	cfgView := body["config"].(map[string]any)
	pc, _ := cfgView["plugins_config"].(map[string]any)
	if pc == nil {
		t.Fatal("config view missing plugins_config")
	}
	wb := pc["weather-bot"].(map[string]any)
	if wb["api_key"] != "***" {
		t.Errorf("patch response leaked a secret: %v", wb["api_key"])
	}
}
