package gateway

import (
	"fmt"
	"github.com/soulacy/soulacy/internal/updates"
	"strings"

	"github.com/gofiber/fiber/v2"
)

type deploymentCheck struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type deploymentReadiness struct {
	Profile     string            `json:"profile"`
	Label       string            `json:"label"`
	Status      string            `json:"status"`
	Score       int               `json:"score"`
	Ready       int               `json:"ready"`
	Total       int               `json:"total"`
	Owner       string            `json:"owner,omitempty"`
	Region      string            `json:"region,omitempty"`
	Notes       string            `json:"notes,omitempty"`
	Strict      bool              `json:"strict"`
	Checks      []deploymentCheck `json:"checks"`
	NextActions []string          `json:"next_actions,omitempty"`
}

func (s *Server) handleDeploymentStatus(c *fiber.Ctx) error {
	providers := s.providerDoctorChecks(c)
	channels := s.channelDoctorChecks()
	_, enabledAgents, _, _, _ := s.agentReadinessCounts(s.agents(c))
	providersReady := countDoctorProviders(providers, "ok", "warn")
	usableOutbound := countUsableOutboundChannels(channels)
	updateManifest := s.updateManifestSource()
	costs := s.costReadiness(c)
	slo := s.sloReadiness(c)
	return c.JSON(s.deploymentReadiness(s.agents(c), providersReady, usableOutbound, enabledAgents, updateManifest, costs, slo))
}

func (s *Server) deploymentReadiness(scope agentScope, providersReady, usableOutbound, enabledAgents int, updateManifest string, costs costReadiness, slo sloReadiness) deploymentReadiness {
	// H1 — nil-safe front: s / s.cfg are optional in some test call sites,
	// so guard the deref before reading Deployment. Matches the same defensive
	// pattern used below for s.authEngine / s.cfg.Server.
	profile := ""
	if s != nil && s.cfg != nil {
		profile = normalizeDeploymentProfile(s.cfg.Deployment.Profile)
	} else {
		profile = normalizeDeploymentProfile("")
	}
	strict := profile == "production"
	statusForRequired := func(ok bool) string {
		if ok {
			return "ok"
		}
		if strict {
			return "fail"
		}
		return "warn"
	}
	authReady := s != nil && s.authEngine != nil && s.authEngine.Effective()
	if !authReady && s != nil && s.cfg != nil {
		authReady = strings.TrimSpace(s.cfg.Server.APIKey) != ""
	}
	// True for BOTH supported configurations: a custom manifest URL, or the
	// built-in GitHub releases source that `sy update` uses when none is set.
	// This used to be `!= ""`, which marked every default install as failing a
	// required production check for a feature that was working.
	updateReady := true
	_ = updateManifest

	checks := []deploymentCheck{
		{
			Key:    "profile",
			Label:  "Deployment profile",
			Status: "ok",
			Detail: deploymentProfileDetail(profile),
		},
		{
			Key:    "auth",
			Label:  "Authenticated API",
			Status: statusForRequired(authReady),
			Detail: detailForBool(authReady, "Gateway API authentication is configured.", "Production gateways need an API key or auth engine."),
		},
		{
			Key:    "providers",
			Label:  "Model providers",
			Status: statusForRequired(providersReady > 0),
			Detail: fmt.Sprintf("%d provider(s) are usable for agent calls.", providersReady),
		},
		{
			Key:    "agents",
			Label:  "Enabled agents",
			Status: statusForRequired(enabledAgents > 0),
			Detail: fmt.Sprintf("%d enabled agent(s) are available.", enabledAgents),
		},
		{
			Key:    "channels",
			Label:  "Outbound channels",
			Status: deploymentChannelStatus(profile, usableOutbound),
			Detail: fmt.Sprintf("%d outbound channel(s) are usable for scheduled or async delivery.", usableOutbound),
		},
		{
			Key:    "updates",
			Label:  "Update manifest",
			Status: statusForRequired(updateReady),
			Detail: detailForBool(updateReady, "Release update manifest is configured.",
				"Using the built-in "+updates.DefaultSourceDescription()+". Set updates.manifest_url to publish from your own host."),
		},
		{
			Key:    "costs",
			Label:  "Cost guardrails",
			Status: deploymentInheritedStatus(profile, costs.Status),
			Detail: "Cost readiness is " + statusLabelForReadiness(costs.Status) + ".",
		},
		{
			Key:    "slo",
			Label:  "Run SLOs",
			Status: deploymentInheritedStatus(profile, slo.Status),
			Detail: fmt.Sprintf("Run SLO readiness is %s across %s.", statusLabelForReadiness(slo.Status), deploymentValueOr(slo.Window, "24h")),
		},
		{
			Key:    "support",
			Label:  "Support evidence",
			Status: "ok",
			Detail: "Redacted support bundles include readiness, diagnostics, config, agent manifests, and recent logs.",
		},
	}

	// S4 (Cohort F) — Production defaults sub-check. Fails production
	// mode when the workspace exposes a Privileged agent through a
	// shared channel without accept_privileged_exposure:true; warns
	// outside production so operators see the finding when they
	// eventually flip the profile. Non-destructive migration by
	// design: we never rewrite the config, only surface the risk.
	sec := s.evaluateSecurityReadiness(scope)
	secDetail := "No privileged agent is exposed through a shared external channel."
	if len(sec.PrivilegedExposures) > 0 {
		secDetail = fmt.Sprintf("%d privileged agent exposure(s) on shared channels; %d without acceptance.",
			len(sec.PrivilegedExposures), countUnacceptedExposures(sec.PrivilegedExposures))
	}
	checks = append(checks, deploymentCheck{
		Key:    "security",
		Label:  "Security defaults",
		Status: sec.Status,
		Detail: secDetail,
	})

	ready := 0
	status := "ok"
	next := make([]string, 0)
	for _, check := range checks {
		switch check.Status {
		case "ok":
			ready++
		case "warn":
			if status == "ok" {
				status = "warn"
			}
			next = append(next, deploymentNextAction(check.Key))
		default:
			status = "fail"
			next = append(next, deploymentNextAction(check.Key))
		}
	}
	score := 0
	if len(checks) > 0 {
		score = int(float64(ready) / float64(len(checks)) * 100)
	}
	return deploymentReadiness{
		Profile:     profile,
		Label:       deploymentProfileLabel(profile),
		Status:      status,
		Score:       score,
		Ready:       ready,
		Total:       len(checks),
		Owner:       strings.TrimSpace(s.cfg.Deployment.Owner),
		Region:      strings.TrimSpace(s.cfg.Deployment.Region),
		Notes:       strings.TrimSpace(s.cfg.Deployment.Notes),
		Strict:      strict,
		Checks:      checks,
		NextActions: uniqueStrings(next),
	}
}

func deploymentValueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func normalizeDeploymentProfile(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "prod", "production":
		return "production"
	case "stage", "staging":
		return "staging"
	case "dev", "development":
		return "development"
	default:
		return "local"
	}
}

func deploymentProfileLabel(profile string) string {
	switch profile {
	case "production":
		return "Production"
	case "staging":
		return "Staging"
	case "development":
		return "Development"
	default:
		return "Local"
	}
}

func deploymentProfileDetail(profile string) string {
	switch profile {
	case "production":
		return "Strict launch blockers are enabled for auth, providers, agents, delivery, updates, cost, and SLO posture."
	case "staging":
		return "Staging profile warns on launch gaps while allowing validation and dry runs."
	case "development":
		return "Development profile favors fast iteration but still shows production gaps."
	default:
		return "Local profile keeps setup lightweight while surfacing upgrade steps for production."
	}
}

func deploymentChannelStatus(profile string, usableOutbound int) string {
	if usableOutbound > 0 {
		return "ok"
	}
	if profile == "production" || profile == "staging" {
		return "fail"
	}
	return "warn"
}

func deploymentInheritedStatus(profile, status string) string {
	switch status {
	case "ok":
		return "ok"
	case "fail":
		if profile == "production" {
			return "fail"
		}
		return "warn"
	default:
		return "warn"
	}
}

func detailForBool(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

func statusLabelForReadiness(status string) string {
	switch status {
	case "ok":
		return "healthy"
	case "warn":
		return "warning"
	case "fail":
		return "failing"
	default:
		return "unknown"
	}
}

func deploymentNextAction(key string) string {
	switch key {
	case "auth":
		return "Configure server.api_key or the auth engine before exposing the gateway."
	case "providers":
		return "Configure and test at least one model provider."
	case "agents":
		return "Enable at least one production agent and run a manual smoke test."
	case "channels":
		return "Configure a default outbound channel for scheduled and async agent results."
	case "updates":
		return "Set updates.manifest_url so production installs can check and apply releases."
	case "costs":
		return "Add pricing and budget guardrails in Config > Cost estimation."
	case "slo":
		return "Repair failed agents or tune SLO thresholds after representative runs."
	case "security":
		return "Every privileged agent exposed on a shared channel needs accept_privileged_exposure:true on the binding; consult /readiness security for the exact list."
	default:
		return "Review deployment readiness before launch."
	}
}

// countUnacceptedExposures totals privileged exposures whose binding
// still lacks accept_privileged_exposure:true. Used by the S4
// deployment readiness detail so operators see the shape of the
// remaining work without reading the full report.
func countUnacceptedExposures(reps []privilegedExposureReport) int {
	n := 0
	for _, r := range reps {
		if !r.Accepted {
			n++
		}
	}
	return n
}
