package gateway

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/pkg/skill"
)

func TestMarketplaceStatusReportsDefaultSourcesAndInstalledSkills(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  port: 18789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	s.skillLoader = &rescanSkillLoader{skills: []*skill.Skill{{
		Name:        "weather-planner",
		Description: "Decision-focused weather planning",
		Path:        "/tmp/weather-planner/SKILL.md",
	}}}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/marketplace/status", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["installed_skills"].(float64) != 1 {
		t.Fatalf("installed_skills = %v", body["installed_skills"])
	}
	if body["registries"].(float64) < 2 || body["searchable_sources"].(float64) < 1 {
		t.Fatalf("registry readiness = %v", body)
	}
	if body["default_registries"] != true {
		t.Fatalf("default registries not reported: %v", body)
	}
	checks := body["checks"].([]any)
	if len(checks) == 0 {
		t.Fatalf("expected readiness checks: %v", body)
	}
}

func TestReadinessIncludesMarketplaceParity(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	seed := `
server:
  port: 18789
registries:
  - id: signed
    type: http
    base_url: https://registry.example.test
    signing_key: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
`
	if err := os.WriteFile(cfgPath, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	s.skillLoader = &rescanSkillLoader{skills: []*skill.Skill{{Name: "launch-kit", Description: "Launch guidance"}}}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if _, ok := body["marketplace"].(map[string]any); !ok {
		t.Fatalf("readiness missing marketplace payload: %v", body)
	}
	parity := body["parity"].(map[string]any)
	areas := parity["areas"].([]any)
	found := false
	for _, raw := range areas {
		area := raw.(map[string]any)
		if area["key"] == "marketplace" {
			found = true
			if area["status"] != "ok" {
				t.Fatalf("marketplace parity = %v, want ok", area)
			}
		}
	}
	if !found {
		t.Fatalf("marketplace parity area missing: %v", areas)
	}
}
