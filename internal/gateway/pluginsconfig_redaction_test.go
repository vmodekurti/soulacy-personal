package gateway

// Story E17: plugins_config is exposed through the config API for the GUI,
// but plugin secrets must never reach the browser. Same heuristic as the
// unknown-channel redaction (Story 1's safeChannelsView pattern), applied
// recursively because plugin settings nest freely.

import "testing"

func TestSafePluginsConfigViewRedactsSecrets(t *testing.T) {
	src := map[string]map[string]any{
		"weather-bot": {
			"units":   "metric",
			"api_key": "sk-very-secret",
			"nested": map[string]any{
				"auth_token": "tok-123",
				"endpoint":   "https://api.example.com",
			},
		},
	}
	out := safePluginsConfigView(src)

	wb := out["weather-bot"]
	if wb["units"] != "metric" {
		t.Fatalf("non-secret mutated: %v", wb["units"])
	}
	if wb["api_key"] != "***" {
		t.Fatalf("api_key not redacted: %v", wb["api_key"])
	}
	nested := wb["nested"].(map[string]any)
	if nested["auth_token"] != "***" {
		t.Fatalf("nested secret not redacted: %v", nested["auth_token"])
	}
	if nested["endpoint"] != "https://api.example.com" {
		t.Fatalf("nested non-secret mutated: %v", nested["endpoint"])
	}

	// source map untouched (deep copy, matching safeChannelsView semantics)
	if src["weather-bot"]["api_key"] != "sk-very-secret" {
		t.Fatal("source map mutated")
	}
	if src["weather-bot"]["nested"].(map[string]any)["auth_token"] != "tok-123" {
		t.Fatal("nested source map mutated")
	}
}

func TestSafePluginsConfigViewEmptySecretsStayEmpty(t *testing.T) {
	out := safePluginsConfigView(map[string]map[string]any{
		"p": {"api_key": ""},
	})
	if out["p"]["api_key"] != "" {
		t.Fatalf("empty secret must stay empty (GUI uses it to detect unset keys), got %v", out["p"]["api_key"])
	}
}

func TestSafePluginsConfigViewNil(t *testing.T) {
	if out := safePluginsConfigView(nil); out != nil {
		t.Fatalf("nil in → nil out, got %v", out)
	}
}
