// securityreadiness.go — S4 (Cohort F) production-profile hardening.
//
// Adds a new deployment-readiness sub-check that fails production mode
// when the workspace has agents whose capability tier is Privileged
// (per internal/tier) and one or more of them is exposed on a shared
// external channel WITHOUT `accept_privileged_exposure: true` on the
// binding. Composes with the existing tier + channel-binding gate in
// internal/app/channels.go — this is the readiness surfacing, not a
// duplicate policy.
//
// Also implements the migration warning path: existing workspaces
// stay bootable — no config auto-rewrites — but the readiness surface
// tells the operator exactly which bindings need attention when they
// flip the deployment profile to `production`.
package gateway

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/tier"
	"github.com/soulacy/soulacy/pkg/agent"
)

// handleSecurityReadiness renders the S4 verdict as JSON. Consumed by
// the Studio preflight (S6), the Security Doctor (S7), and the launch
// dashboard.
func (s *Server) handleSecurityReadiness(c *fiber.Ctx) error {
	return c.JSON(s.evaluateSecurityReadiness())
}

// securityReadinessJourneyItem renders the S4 verdict as a readiness
// journey entry so the launch dashboard shows a single line summary
// alongside the other categories. Falls back to a friendly zero-state
// when the report is clean.
func securityReadinessJourneyItem(sec securityReadiness) readinessItem {
	detail := "No privileged agent is exposed on a shared channel without acknowledgement."
	if len(sec.PrivilegedExposures) > 0 {
		unaccepted := 0
		for _, e := range sec.PrivilegedExposures {
			if !e.Accepted {
				unaccepted++
			}
		}
		if unaccepted == 0 {
			detail = fmt.Sprintf("%d privileged agent exposure(s) on shared channels, all accepted.", len(sec.PrivilegedExposures))
		} else {
			detail = fmt.Sprintf("%d privileged agent exposure(s) on shared channels; %d still need accept_privileged_exposure:true.",
				len(sec.PrivilegedExposures), unaccepted)
		}
	}
	return readinessItem{
		Key:    "security",
		Label:  "Security defaults",
		Status: sec.Status,
		Detail: detail,
		Href:   "#security",
	}
}

// securityReadiness is the S4 readiness verdict. Ready==false blocks
// production launch through the deployment-readiness journey.
type securityReadiness struct {
	Ready               bool                       `json:"ready"`
	Status              string                     `json:"status"` // ok | warn | fail
	Profile             string                     `json:"profile"`
	Reasons             []string                   `json:"reasons,omitempty"`
	PrivilegedExposures []privilegedExposureReport `json:"privileged_exposures,omitempty"`
	WildcardMCPAgents   []string                   `json:"wildcard_mcp_agents,omitempty"`
	NextActions         []string                   `json:"next_actions,omitempty"`
}

// privilegedExposureReport names one agent and the shared channels
// through which it can be reached. `Accepted` is true when every
// binding to that agent has `accept_privileged_exposure: true`; the
// S4 gate fails production when Accepted is false for any exposure.
type privilegedExposureReport struct {
	AgentID   string   `json:"agent_id"`
	AgentName string   `json:"agent_name,omitempty"`
	Tier      string   `json:"tier"`
	Channels  []string `json:"channels"`
	Accepted  bool     `json:"accepted"`
	Reasons   []string `json:"reasons,omitempty"`
}

// sharedExternalChannels enumerates the channel-kind strings the
// binding-gate treats as external. Matches
// runtime.isSharedExternalChannel and internal/app/channels.go so the
// three stay in sync. HTTP + internal + workboard are the operator's
// own surfaces and never trigger the S4 warning.
func sharedExternalChannels() map[string]bool {
	return map[string]bool{
		"telegram": true, "slack": true, "discord": true,
		"whatsapp": true, "whatsapp_web": true,
		"email": true, "teams": true, "google_chat": true,
		"sms": true, "webhook": true,
	}
}

// evaluateSecurityReadiness inspects the loaded agents and configured
// channels, computes the S4 verdict, and returns it. Deterministic —
// no I/O beyond the loader lookup — so it's safe to call on every
// /readiness request.
func (s *Server) evaluateSecurityReadiness() securityReadiness {
	profile := normalizeDeploymentProfile(s.cfg.Deployment.Profile)
	rep := securityReadiness{Profile: profile}

	if s.loader == nil {
		rep.Status = "warn"
		rep.Reasons = append(rep.Reasons, "agent loader is not initialised; security readiness cannot be evaluated")
		return rep
	}

	// Walk every loaded agent, classify tier, and collect Privileged
	// agents. Use loader.All so cycle detection + peer walk work with
	// the full agent set.
	all := s.loader.All()
	privileged := make(map[string]*agent.Definition)
	var wildcardMCP []string
	for _, def := range all {
		if def == nil {
			continue
		}
		expl := tier.Explain(def, s.loader.Get)
		if expl.Tier == tier.Privileged {
			privileged[def.ID] = def
		}
		// Also flag wildcard MCP as a config smell in production — the
		// tier system already marks these Privileged (`internal/tier`
		// wildcard rules), but this collects them so the report has a
		// separate "wildcard MCP" bullet the operator can act on.
		if hasWildcardMCP(def) {
			wildcardMCP = append(wildcardMCP, def.ID)
		}
	}

	// Enumerate shared-external channel bindings and note which
	// privileged agents they expose.
	sharedKinds := sharedExternalChannels()
	exposures := map[string]*privilegedExposureReport{}
	for kind, raw := range s.cfg.Channels {
		if !sharedKinds[strings.ToLower(strings.TrimSpace(kind))] {
			continue
		}
		if raw == nil {
			continue
		}
		if enabled, ok := raw["enabled"].(bool); ok && !enabled {
			continue
		}
		// Legacy single-bot binding.
		bindAgent := stringValue(raw, "agent_id")
		if bindAgent != "" {
			collectExposure(exposures, privileged, bindAgent, kind, "primary", raw)
		}
		// Multi-bot `bots:` list.
		if list, ok := raw["bots"].([]any); ok {
			for _, item := range list {
				bot, ok := item.(map[string]any)
				if !ok {
					continue
				}
				aid := stringValue(bot, "agent_id")
				if aid == "" {
					continue
				}
				botName := stringValue(bot, "bot_name")
				if botName == "" {
					botName = stringValue(bot, "name")
				}
				collectExposure(exposures, privileged, aid, kind, botName, bot)
			}
		}
	}

	// Order the exposure reports for stable output.
	ids := make([]string, 0, len(exposures))
	for id := range exposures {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		e := exposures[id]
		sort.Strings(e.Channels)
		rep.PrivilegedExposures = append(rep.PrivilegedExposures, *e)
	}
	sort.Strings(wildcardMCP)
	rep.WildcardMCPAgents = wildcardMCP

	// Verdict.
	unaccepted := 0
	for _, e := range rep.PrivilegedExposures {
		if !e.Accepted {
			unaccepted++
		}
	}
	switch {
	case unaccepted > 0 && profile == "production":
		rep.Status = "fail"
		rep.Ready = false
		rep.Reasons = append(rep.Reasons,
			fmt.Sprintf("%d privileged agent exposure(s) on shared channels without accept_privileged_exposure:true", unaccepted))
		rep.NextActions = append(rep.NextActions,
			"For each binding listed under privileged_exposures, either set accept_privileged_exposure:true after auditing the risk or move the agent to an internal channel (http)")
	case unaccepted > 0:
		rep.Status = "warn"
		rep.Ready = true
		rep.Reasons = append(rep.Reasons,
			fmt.Sprintf("%d privileged agent exposure(s) on shared channels without accept_privileged_exposure:true (advisory outside production)", unaccepted))
		rep.NextActions = append(rep.NextActions,
			"Review the listed bindings; set accept_privileged_exposure:true after auditing or restrict the agent to http")
	case len(wildcardMCP) > 0 && profile == "production":
		rep.Status = "warn"
		rep.Ready = true
		rep.Reasons = append(rep.Reasons,
			fmt.Sprintf("%d agent(s) declare wildcard mcp_servers/mcp_tools — production installs should list servers explicitly", len(wildcardMCP)))
	default:
		rep.Status = "ok"
		rep.Ready = true
	}
	return rep
}

func collectExposure(
	out map[string]*privilegedExposureReport,
	privileged map[string]*agent.Definition,
	agentID, channelKind, botName string,
	bindingCfg map[string]any,
) {
	def, ok := privileged[agentID]
	if !ok {
		return
	}
	accepted, _ := bindingCfg["accept_privileged_exposure"].(bool)
	label := channelKind
	if strings.TrimSpace(botName) != "" && !strings.EqualFold(botName, "primary") {
		label = channelKind + "/" + botName
	}
	rep, exists := out[agentID]
	if !exists {
		rep = &privilegedExposureReport{
			AgentID:   agentID,
			AgentName: strings.TrimSpace(def.Name),
			Tier:      tier.Privileged.String(),
			Accepted:  true, // start optimistic; any unaccepted flips it false
		}
		out[agentID] = rep
	}
	rep.Channels = append(rep.Channels, label)
	if !accepted {
		rep.Accepted = false
		rep.Reasons = append(rep.Reasons,
			label+" binding does not set accept_privileged_exposure: true")
	}
}

func hasWildcardMCP(def *agent.Definition) bool {
	if def == nil {
		return false
	}
	check := func(p *[]string) bool {
		if p == nil {
			return false
		}
		for _, v := range *p {
			if v == "*" || strings.EqualFold(v, "all") {
				return true
			}
		}
		return false
	}
	return check(def.MCPServers) || check(def.MCPTools)
}

func stringValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}
