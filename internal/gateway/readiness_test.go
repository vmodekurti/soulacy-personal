package gateway

import (
	"net/http"
	"strings"
	"testing"
)

func TestReadinessEndpointReturnsProductJourney(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	createBody := `{
		"id": "ready-agent",
		"name": "Ready Agent",
		"trigger": "channel",
		"channels": ["http"],
		"llm": {"provider": "test", "model": "fake-model"},
		"system_prompt": "Help.",
		"enabled": true,
		"learning": {"enabled": true}
	}`
	if status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody); status != http.StatusCreated {
		t.Fatalf("create status = %d body=%v", status, body)
	}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("readiness status = %d body=%v", status, body)
	}
	summary, ok := body["summary"].(map[string]any)
	if !ok {
		t.Fatalf("missing summary: %#v", body)
	}
	if got, _ := summary["enabled_agents"].(float64); got < 1 {
		t.Fatalf("enabled_agents = %v, want at least 1", summary["enabled_agents"])
	}
	if _, ok := summary["updates_ready"].(bool); !ok {
		t.Fatalf("missing updates_ready: %#v", summary)
	}
	if got, _ := summary["deployment_profile"].(string); got == "" {
		t.Fatalf("missing deployment_profile: %#v", summary)
	}
	if _, ok := summary["voice_ready"].(bool); !ok {
		t.Fatalf("missing voice_ready: %#v", summary)
	}
	release, ok := body["release"].(map[string]any)
	if !ok {
		t.Fatalf("missing release metadata: %#v", body)
	}
	if _, ok := release["update_hint"].(string); !ok {
		t.Fatalf("missing release update_hint: %#v", release)
	}
	journey, ok := body["journey"].([]any)
	if !ok || len(journey) < 6 {
		t.Fatalf("journey = %#v", body["journey"])
	}
	if _, ok := body["next_actions"].([]any); !ok {
		t.Fatalf("missing next_actions: %#v", body)
	}
	if checklist, ok := body["launch_checklist"].([]any); !ok || len(checklist) == 0 {
		t.Fatalf("missing launch_checklist: %#v", body)
	}
	if _, ok := body["vault"].(map[string]any); !ok {
		t.Fatalf("missing vault readiness: %#v", body)
	}
	contracts, ok := body["studio_contracts"].(map[string]any)
	if !ok {
		t.Fatalf("missing studio contract readiness: %#v", body)
	}
	if got, _ := contracts["checked"].(float64); got < 1 {
		t.Fatalf("studio contract checked = %v, want at least 1", contracts["checked"])
	}
	parity, ok := body["parity"].(map[string]any)
	if !ok {
		t.Fatalf("missing parity cockpit: %#v", body)
	}
	if got, _ := parity["score"].(float64); got <= 0 {
		t.Fatalf("parity score = %v, want positive", parity["score"])
	}
	if areas, ok := parity["areas"].([]any); !ok || len(areas) < 8 {
		t.Fatalf("parity areas = %#v", parity["areas"])
	}
	if _, ok := body["executors"].(map[string]any); !ok {
		t.Fatalf("missing executors readiness: %#v", body)
	}
	if _, ok := body["browser"].(map[string]any); !ok {
		t.Fatalf("missing browser readiness: %#v", body)
	}
	if voice, ok := body["voice"].(map[string]any); !ok {
		t.Fatalf("missing voice readiness: %#v", body)
	} else if voice["status"] == "" || voice["score"] == nil {
		t.Fatalf("voice readiness incomplete: %#v", voice)
	}
	if docs, ok := body["docs"].(map[string]any); !ok {
		t.Fatalf("missing docs readiness: %#v", body)
	} else if docs["status"] == "" || docs["score"] == nil {
		t.Fatalf("docs readiness incomplete: %#v", docs)
	}
	if deployment, ok := body["deployment"].(map[string]any); !ok {
		t.Fatalf("missing deployment readiness: %#v", body)
	} else if deployment["profile"] == "" || deployment["status"] == "" {
		t.Fatalf("deployment readiness incomplete: %#v", deployment)
	}
	if gaps, ok := parity["top_gaps"].([]any); !ok || len(gaps) == 0 {
		t.Fatalf("parity top gaps = %#v", parity["top_gaps"])
	}
}

func TestDeploymentStatusProductionIsStrict(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	s.cfg.Deployment.Profile = "production"

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/deployment/status", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("deployment status = %d body=%v", status, body)
	}
	if body["profile"] != "production" || body["strict"] != true {
		t.Fatalf("deployment body = %#v", body)
	}
	if body["status"] != "fail" {
		t.Fatalf("production deployment status = %v, want fail with missing launch prerequisites", body["status"])
	}
	checks, ok := body["checks"].([]any)
	if !ok || len(checks) == 0 {
		t.Fatalf("missing checks: %#v", body)
	}
}

func TestReadinessUsesConfiguredUpdateManifest(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	s.cfg.Updates.ManifestURL = "https://releases.example.test/soulacy/manifest.json"

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("readiness status = %d body=%v", status, body)
	}
	summary := body["summary"].(map[string]any)
	if got, _ := summary["updates_ready"].(bool); !got {
		t.Fatalf("updates_ready = %v, want true", summary["updates_ready"])
	}
	release := body["release"].(map[string]any)
	if got := release["update_manifest"]; got != "https://releases.example.test/soulacy/manifest.json" {
		t.Fatalf("update_manifest = %#v", got)
	}
}

func TestLaunchChecklistSurfacesProviderAndVaultRemedies(t *testing.T) {
	s := newTestGateway(t, "secret")

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("readiness status = %d body=%v", status, body)
	}
	checklist, ok := body["launch_checklist"].([]any)
	if !ok || len(checklist) == 0 {
		t.Fatalf("launch_checklist missing: %#v", body)
	}
	var sawProvider, sawVault bool
	for _, raw := range checklist {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("checklist item is not object: %#v", raw)
		}
		key, _ := item["key"].(string)
		if strings.HasPrefix(key, "provider:") {
			sawProvider = true
			if item["status"] != "fail" {
				t.Fatalf("provider checklist status = %v, want fail; item=%v", item["status"], item)
			}
			if item["remedy"] == "" {
				t.Fatalf("provider checklist missing remedy: %v", item)
			}
		}
		if key == "vault" {
			sawVault = true
			if item["remedy"] == "" {
				t.Fatalf("vault checklist missing remedy: %v", item)
			}
		}
	}
	if !sawProvider || !sawVault {
		t.Fatalf("expected provider and vault items, saw provider=%v vault=%v checklist=%v", sawProvider, sawVault, checklist)
	}
}

func TestReadinessScoreReachesHundredWhenEverythingIsReady(t *testing.T) {
	items := []readinessItem{
		{Status: "ok"},
		{Status: "ok"},
		{Status: "ok"},
		{Status: "ok"},
		{Status: "ok"},
		{Status: "ok"},
		{Status: "ok"},
		{Status: "ok"},
	}
	if got := readinessScore(items); got != 100 {
		t.Fatalf("readinessScore(all ok) = %d, want 100", got)
	}
}

func TestReadinessScoreWeightsWarningsAndBlockers(t *testing.T) {
	items := []readinessItem{
		{Status: "ok"},
		{Status: "warn"},
		{Status: "fail"},
	}
	if got := readinessScore(items); got != 51 {
		t.Fatalf("readinessScore(mixed) = %d, want 51", got)
	}
}

func TestReadinessStatusCounts(t *testing.T) {
	items := []readinessItem{
		{Status: "ok"},
		{Status: "warn"},
		{Status: "fail"},
		{Status: "missing"},
	}
	ready, warnings, blockers := readinessStatusCounts(items)
	if ready != 1 || warnings != 1 || blockers != 2 {
		t.Fatalf("counts = ready:%d warnings:%d blockers:%d, want 1/1/2", ready, warnings, blockers)
	}
}

func TestParityGapsPrioritizeLowestScores(t *testing.T) {
	areas := []parityArea{
		{Key: "studio", Status: "ok", Score: 90},
		{Key: "enterprise", Status: "fail", Score: 25},
		{Key: "mobile", Status: "warn", Score: 50},
		{Key: "channels", Status: "warn", Score: 55},
	}
	gaps := topParityGaps(areas, 2)
	if len(gaps) != 2 {
		t.Fatalf("gaps len = %d, want 2", len(gaps))
	}
	if gaps[0].Key != "enterprise" || gaps[1].Key != "mobile" {
		t.Fatalf("gaps order = %#v", gaps)
	}
	if got := parityScore(areas); got != 55 {
		t.Fatalf("parityScore = %d, want 55", got)
	}
}

func TestParityStudioReflectsContractBlockers(t *testing.T) {
	area := parityStudio(3, studioContractReadiness{
		Status:       "fail",
		Score:        38,
		Checked:      2,
		Passing:      0,
		Blockers:     3,
		Warnings:     1,
		WorstAgent:   "Broken Workflow",
		WorstSummary: "Studio contract blocked by 2 issue(s), with 1 warning(s).",
	})
	if area.Status != "fail" || area.Score != 38 {
		t.Fatalf("studio area = %#v, want fail/38", area)
	}
	if !strings.Contains(area.Detail, "Broken Workflow") || !strings.Contains(area.Next, "contract") {
		t.Fatalf("studio area should explain weakest contract and next step: %#v", area)
	}
}

func TestParityEnterpriseReflectsConfiguredControls(t *testing.T) {
	area := parityEnterprise(enterpriseParityPosture{
		Controls: []string{"authenticated API", "RBAC policies", "managed API keys", "encrypted credential vault", "audit log directory"},
		Missing:  nil,
		Score:    78,
		Status:   "warn",
	})
	if area.Status != "warn" || area.Score != 78 {
		t.Fatalf("enterprise area = %#v, want warn/78", area)
	}
	if area.Detail == "" || area.Next == "" {
		t.Fatalf("enterprise area should explain current controls and next step: %#v", area)
	}
}

func TestParityEnterpriseFailsWhenControlsAreAbsent(t *testing.T) {
	area := parityEnterprise(enterpriseParityPosture{
		Missing: []string{"authenticated API", "RBAC policies"},
		Score:   25,
		Status:  "fail",
	})
	if area.Status != "fail" || area.Score != 25 {
		t.Fatalf("enterprise area = %#v, want fail/25", area)
	}
}
