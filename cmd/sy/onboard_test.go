// onboard_test.go — tests for the config.yaml patch helpers in onboard.go.
// These edit config.yaml via the yaml.v3 Node API so `sy onboard` can add a
// provider/key without corrupting an operator's hand-edited config. The
// critical invariants: edits land on the RIGHT key/provider (never a sibling),
// re-running with the same value never duplicates a key, and comments survive.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// writeTemp writes body to a temp config.yaml and returns its path.
func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

// parseConfig reads and unmarshals a patched file into a generic map so tests
// can assert on the resulting structure (not on exact text formatting).
func parseConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("patched config no longer parses as YAML: %v\n---\n%s", err, data)
	}
	return m
}

func providerMap(t *testing.T, m map[string]any, id string) map[string]any {
	t.Helper()
	llm, ok := m["llm"].(map[string]any)
	if !ok {
		t.Fatalf("llm block missing or wrong type: %#v", m["llm"])
	}
	provs, ok := llm["providers"].(map[string]any)
	if !ok {
		t.Fatalf("llm.providers missing or wrong type: %#v", llm["providers"])
	}
	p, ok := provs[id].(map[string]any)
	if !ok {
		t.Fatalf("provider %q missing or wrong type: %#v", id, provs[id])
	}
	return p
}

const baseConfig = `# Soulacy gateway configuration.
server:
  host: 127.0.0.1
  port: 18789
  api_key: "sy_existing"
llm:
  default_provider: ollama
  providers:
    ollama:
      base_url: "http://localhost:11434"
      # Pull this with: ollama pull llama3.3:70b
      model: "llama3.3:70b"
log:
  level: info
`

// The regression that motivated this refactor: setting google's base_url must
// NOT overwrite the ollama provider's base_url.
func TestPatchProviderBaseURL_DoesNotTouchSiblings(t *testing.T) {
	p := writeTemp(t, baseConfig)
	if err := patchProviderKey(p, "google", ""); err != nil {
		t.Fatalf("patchProviderKey: %v", err)
	}
	if err := patchProviderBaseURL(p, "google", "https://generativelanguage.googleapis.com/v1beta"); err != nil {
		t.Fatalf("patchProviderBaseURL: %v", err)
	}
	m := parseConfig(t, p)

	google := providerMap(t, m, "google")
	if got := google["base_url"]; got != "https://generativelanguage.googleapis.com/v1beta" {
		t.Errorf("google.base_url = %v; want the gemini URL", got)
	}
	ollama := providerMap(t, m, "ollama")
	if got := ollama["base_url"]; got != "http://localhost:11434" {
		t.Errorf("ollama.base_url was clobbered: got %v; want http://localhost:11434", got)
	}
	if got := ollama["model"]; got != "llama3.3:70b" {
		t.Errorf("ollama.model changed unexpectedly: got %v", got)
	}
}

func TestPatchProviderKey_AddsAndReplaces(t *testing.T) {
	p := writeTemp(t, baseConfig)
	if err := patchProviderKey(p, "openai", "sk-test"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if got := providerMap(t, parseConfig(t, p), "openai")["api_key"]; got != "sk-test" {
		t.Fatalf("openai.api_key = %v; want sk-test", got)
	}
	// Replace existing.
	if err := patchProviderKey(p, "openai", "sk-new"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got := providerMap(t, parseConfig(t, p), "openai")["api_key"]; got != "sk-new" {
		t.Fatalf("openai.api_key = %v; want sk-new", got)
	}
}

func TestPatchDefaultProvider(t *testing.T) {
	p := writeTemp(t, baseConfig)
	if err := patchDefaultProvider(p, "google"); err != nil {
		t.Fatalf("patchDefaultProvider: %v", err)
	}
	llm := parseConfig(t, p)["llm"].(map[string]any)
	if got := llm["default_provider"]; got != "google" {
		t.Errorf("default_provider = %v; want google", got)
	}
}

func TestPatchServerHost(t *testing.T) {
	p := writeTemp(t, baseConfig)
	if err := patchServerHost(p, "0.0.0.0"); err != nil {
		t.Fatalf("patchServerHost: %v", err)
	}
	srv := parseConfig(t, p)["server"].(map[string]any)
	if got := srv["host"]; got != "0.0.0.0" {
		t.Errorf("server.host = %v; want 0.0.0.0", got)
	}
	if got := srv["api_key"]; got != "sy_existing" {
		t.Errorf("server.api_key was disturbed: %v", got)
	}
}

// Re-running the search patchers with the same value must not duplicate keys
// (the bug that produced "mapping key already defined").
func TestPatchSearch_NoDuplicateOnRepeat(t *testing.T) {
	p := writeTemp(t, baseConfig)
	for i := 0; i < 3; i++ {
		if err := patchSearchProvider(p, "ollama"); err != nil {
			t.Fatalf("patchSearchProvider #%d: %v", i, err)
		}
		if err := patchSearchAPIKey(p, "k123"); err != nil {
			t.Fatalf("patchSearchAPIKey #%d: %v", i, err)
		}
	}
	m := parseConfig(t, p) // would fail to parse if a key were duplicated
	search, ok := m["search"].(map[string]any)
	if !ok {
		t.Fatalf("search block missing: %#v", m["search"])
	}
	if search["provider"] != "ollama" || search["api_key"] != "k123" {
		t.Errorf("unexpected search block: %#v", search)
	}
}

func TestPatchUpdateManifestURL_AddsAndReplaces(t *testing.T) {
	p := writeTemp(t, baseConfig)
	first := "https://github.com/vmodekurti/soulacy-personal/releases/latest/download/release-manifest.json"
	second := "/opt/soulacy/release-manifest.json"
	if err := patchUpdateManifestURL(p, first); err != nil {
		t.Fatalf("add update manifest: %v", err)
	}
	if err := patchUpdateManifestURL(p, second); err != nil {
		t.Fatalf("replace update manifest: %v", err)
	}
	m := parseConfig(t, p)
	updates, ok := m["updates"].(map[string]any)
	if !ok {
		t.Fatalf("updates block missing: %#v", m["updates"])
	}
	if got := updates["manifest_url"]; got != second {
		t.Fatalf("updates.manifest_url = %v; want %s", got, second)
	}
}

// Comments on surviving nodes should be preserved through edits.
func TestPatch_PreservesComments(t *testing.T) {
	p := writeTemp(t, baseConfig)
	if err := patchDefaultProvider(p, "google"); err != nil {
		t.Fatalf("patch: %v", err)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), "Pull this with: ollama pull") {
		t.Errorf("inline comment was stripped:\n%s", data)
	}
}

func TestMaskKey(t *testing.T) {
	cases := map[string]string{
		"":                        "",
		"abc":                     "•••",
		"abcdefgh":                "••••••••",
		"sy_1234567890abcdef":     "sy_••••••••••••cdef",
		"sy_abcdefghijklmnopqrst": "sy_••••••••••••••••qrst",
	}
	for in, want := range cases {
		if got := maskKey(in); got != want {
			t.Errorf("maskKey(%q) = %q; want %q", in, got, want)
		}
	}
}
