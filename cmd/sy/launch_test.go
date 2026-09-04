package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLaunchStrictError(t *testing.T) {
	if err := launchStrictError("needs_setup", false); err != nil {
		t.Fatalf("advisory mode should not fail: %v", err)
	}
	if err := launchStrictError("ready", true); err != nil {
		t.Fatalf("ready strict mode should not fail: %v", err)
	}
	if err := launchStrictError("at_risk", true); err == nil {
		t.Fatal("strict mode should fail when launch readiness is not ready")
	}
}

func TestLaunchGateErrorUsesMinScore(t *testing.T) {
	r := launchReadiness{}
	r.Summary.Status = "ready"
	r.Summary.Score = 74
	if err := launchGateError(r, false, 70); err != nil {
		t.Fatalf("score above minimum should pass: %v", err)
	}
	if err := launchGateError(r, false, 80); err == nil {
		t.Fatal("score below minimum should fail")
	}
}

func TestLaunchCertifyEnv(t *testing.T) {
	env := launchCertifyEnv(launchCertifyOptions{
		ReportDir:     "/tmp/soulacy-cert",
		Quick:         true,
		LiveChannels:  true,
		BrowserMCP:    true,
		BrowserRender: true,
		StudioLive:    true,
	})
	want := map[string]bool{
		"SOULACY_PARITY_REPORT_DIR=/tmp/soulacy-cert": true,
		"SOULACY_PARITY_QUICK=1":                      true,
		"SOULACY_PARITY_LIVE_CHANNELS=1":              true,
		"SOULACY_PARITY_BROWSER_MCP=1":                true,
		"SOULACY_PARITY_BROWSER_RENDER=1":             true,
		"SOULACY_PARITY_STUDIO_LIVE=1":                true,
	}
	for _, got := range env {
		delete(want, got)
	}
	for missing := range want {
		t.Fatalf("launchCertifyEnv missing %q in %#v", missing, env)
	}
}

func TestLaunchReadinessParsesParityPayload(t *testing.T) {
	var r launchReadiness
	payload := []byte(`{
		"summary": {"status":"at_risk","score":72},
		"deployment": {"profile":"production","label":"Production","status":"warn","score":78,"ready":7,"total":9,"strict":true,"owner":"platform","region":"us-central"},
		"launch_checklist": [
			{"key":"provider:openai","label":"Provider · openai","status":"warn","detail":"model list failed","remedy":"Check the API key."}
		],
		"parity": {
			"score": 64,
			"top_gaps": [
				{"key":"channels","label":"Channel Reach","status":"warn","score":55,"benchmark":"OpenClaw","next":"Add channel sidecars."}
			]
		}
	}`)
	if err := json.Unmarshal(payload, &r); err != nil {
		t.Fatalf("unmarshal launch readiness: %v", err)
	}
	if r.Parity.Score != 64 || len(r.Parity.TopGaps) != 1 {
		t.Fatalf("parity payload = %#v", r.Parity)
	}
	if r.Deployment.Profile != "production" || !r.Deployment.Strict || r.Deployment.Ready != 7 {
		t.Fatalf("deployment payload = %#v", r.Deployment)
	}
	if len(r.LaunchChecklist) != 1 || r.LaunchChecklist[0].Remedy == "" {
		t.Fatalf("launch checklist payload = %#v", r.LaunchChecklist)
	}
	if r.Parity.TopGaps[0].Benchmark != "OpenClaw" {
		t.Fatalf("benchmark = %q", r.Parity.TopGaps[0].Benchmark)
	}
}

func TestRenderLaunchProofMarkdownIncludesReleaseEvidence(t *testing.T) {
	r := launchReadiness{}
	r.Summary.Status = "at_risk"
	r.Summary.Score = 81
	r.Summary.ProvidersReady = 2
	r.Summary.ChannelsReady = 1
	r.Summary.EnabledAgents = 3
	r.Summary.Agents = 4
	r.Deployment.Label = "Production"
	r.Deployment.Status = "warn"
	r.Parity.Score = 76
	r.Journey = []launchReadinessItem{{Label: "Studio", Status: "warn", Detail: "1 contract warning"}}
	r.LaunchChecklist = []launchChecklistItem{{Label: "Provider · openai", Status: "ok", Detail: "ready"}}
	r.Parity.TopGaps = []launchParityArea{{Label: "Enterprise", Score: 50, Next: "Add tenancy"}}
	r.NextActions = []launchReadinessItem{{Label: "Studio", Status: "warn", Detail: "Review contract warning"}}

	md := renderLaunchProofMarkdown(r, "20260713T120000Z")
	for _, want := range []string{
		"# Soulacy Launch Proof",
		"Readiness score: `81%`",
		"Competitive parity score: `76%`",
		"| Studio | warn | 1 contract warning |",
		"| Enterprise | 50% | Add tenancy |",
		"Review contract warning",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("proof markdown missing %q:\n%s", want, md)
		}
	}
}

func TestEscapeMarkdownCellKeepsTablesValid(t *testing.T) {
	got := escapeMarkdownCell("line one\nline | two")
	if got != "line one line \\| two" {
		t.Fatalf("escapeMarkdownCell = %q", got)
	}
}
