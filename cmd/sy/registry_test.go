package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/pkgregistry"
)

func TestFormatProbeReport(t *testing.T) {
	rep := pkgregistry.ProbeReport{
		URL: "https://skills.sh", Kind: "skillssh",
		Detail:    "skills.sh-compatible skill directory.",
		Samples:   []string{"acme/skills/web-audit"},
		HasAudits: true,
		Suggested: &config.RegistryConfig{ID: "skills.sh", Type: "skillssh", BaseURL: "https://skills.sh", Priority: 50},
	}
	out := formatProbeReport(rep)
	for _, want := range []string{"skillssh", "acme/skills/web-audit", "security audits", "id=skills.sh", "base_url=https://skills.sh"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}

func TestAppendRegistryToConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	seed := "server:\n  port: 18789\nregistries:\n  - id: main\n    type: http\n    base_url: https://r.example\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	err := appendRegistryToConfigFile(path, config.RegistryConfig{
		ID: "skills.sh", Type: "skillssh", BaseURL: "https://skills.sh/", Priority: 50,
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	raw, _ := os.ReadFile(path)
	var disk map[string]any
	if err := yaml.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	regs := disk["registries"].([]any)
	if len(regs) != 2 {
		t.Fatalf("registries = %v", regs)
	}
	added := regs[1].(map[string]any)
	if added["base_url"] != "https://skills.sh" || added["type"] != "skillssh" {
		t.Errorf("added = %v", added)
	}
	if srv := disk["server"].(map[string]any); srv["port"] != 18789 {
		t.Errorf("server block mutated: %v", disk["server"])
	}

	// Duplicate id refused.
	if err := appendRegistryToConfigFile(path, config.RegistryConfig{ID: "main", Type: "http", BaseURL: "https://x"}); err == nil {
		t.Error("duplicate id must be refused")
	}

	// Missing file → created with just the registries block.
	fresh := filepath.Join(t.TempDir(), "config.yaml")
	if err := appendRegistryToConfigFile(fresh, config.RegistryConfig{ID: "g", Type: "git"}); err != nil {
		t.Fatalf("fresh append: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("config not created: %v", err)
	}
}
