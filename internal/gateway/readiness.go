package gateway

import (
	"fmt"
	"github.com/soulacy/soulacy/internal/updates"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/studio"
)

type readinessItem struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Status   string `json:"status"`
	Detail   string `json:"detail"`
	Href     string `json:"href,omitempty"`
	Priority int    `json:"priority,omitempty"`
}

type readinessSummary struct {
	Status            string `json:"status"`
	Score             int    `json:"score"`
	ReadyItems        int    `json:"ready_items"`
	WarningItems      int    `json:"warning_items"`
	BlockerItems      int    `json:"blocker_items"`
	TotalItems        int    `json:"total_items"`
	ProvidersReady    int    `json:"providers_ready"`
	ChannelsReady     int    `json:"channels_ready"`
	Agents            int    `json:"agents"`
	EnabledAgents     int    `json:"enabled_agents"`
	ChatAgents        int    `json:"chat_agents"`
	ScheduledAgents   int    `json:"scheduled_agents"`
	LearningAgents    int    `json:"learning_agents"`
	Templates         int    `json:"templates"`
	UpdatesReady      bool   `json:"updates_ready"`
	DeploymentProfile string `json:"deployment_profile"`
	OpsAlertsReady    bool   `json:"ops_alerts_ready"`
	VoiceReady        bool   `json:"voice_ready"`
	SchedulesReady    bool   `json:"schedules_ready"`
}

type launchChecklistItem struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Remedy string `json:"remedy,omitempty"`
	Href   string `json:"href,omitempty"`
}

type parityArea struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Status    string `json:"status"`
	Score     int    `json:"score"`
	Detail    string `json:"detail"`
	Next      string `json:"next"`
	Benchmark string `json:"benchmark"`
	Href      string `json:"href,omitempty"`
}

type studioContractReadiness struct {
	Status       string `json:"status"`
	Score        int    `json:"score"`
	Agents       int    `json:"agents"`
	Checked      int    `json:"checked"`
	Passing      int    `json:"passing"`
	Blockers     int    `json:"blockers"`
	Warnings     int    `json:"warnings"`
	WorstAgent   string `json:"worst_agent,omitempty"`
	WorstSummary string `json:"worst_summary,omitempty"`
}

// handleReadiness returns Soulacy's product journey state in one place:
// setup, build, delivery, monitoring, learning, and release readiness. It is
// intentionally platform-wide, not tied to one agent, so every agent type gets
// the same go-to-market guardrails.
func (s *Server) handleReadiness(c *fiber.Ctx) error {
	return c.JSON(s.readinessPayload(c))
}

func (s *Server) readinessPayload(c *fiber.Ctx) fiber.Map {
	providers := s.providerDoctorChecks(c)
	channels := s.channelDoctorChecks()
	vault := s.vaultDoctorCheck(c)
	templates, _ := s.templatesCatalog().List()
	s.applyTemplateRuntimeDefaults(templates)

	agents, enabledAgents, chatAgents, scheduledAgents, learningAgents := s.agentReadinessCounts()
	providersReady := countDoctorProviders(providers, "ok", "warn")
	channelsReady := countDoctorChannels(channels, "ok", "warn")
	usableOutbound := countUsableOutboundChannels(channels)
	updateManifest := s.updateManifestSource()
	executors := s.executorReadiness()
	browser := s.browserAutomationReadiness()
	marketplace := s.marketplaceReadiness()
	mobile := s.mobileCompanionReadiness()
	chat := s.chatExperienceReadiness(c)
	voice := s.voiceReadiness()
	costs := s.costReadiness(c)
	slo := s.sloReadiness(c)
	opsAlerts := s.opsAlertReadiness()
	studioContracts := s.studioContractReadiness(c)
	docs := s.publicDocsReadiness()
	schedules := s.scheduleReadiness()
	deployment := s.deploymentReadiness(providersReady, usableOutbound, enabledAgents, updateManifest, costs, slo)

	journey := []readinessItem{
		providerReadinessItem(providers, providersReady),
		{
			Key:    "first_run",
			Label:  "First Run",
			Status: statusFromBool(len(templates) > 0),
			Detail: detailTemplates(len(templates)),
			Href:   "#onboarding",
		},
		studioReadinessItem(len(templates), studioContracts),
		agentReadinessItem(agents, enabledAgents, chatAgents, scheduledAgents),
		scheduleReadinessItem(schedules),
		channelReadinessItem(channels, channelsReady, usableOutbound),
		{
			Key:    "monitor",
			Label:  "Runs",
			Status: "ok",
			Detail: "Run history, replay, and Studio debug links are available.",
			Href:   "#activity",
		},
		learningReadinessItem(learningAgents, enabledAgents),
		{
			Key:    "deployment",
			Label:  "Deployment Profile",
			Status: deployment.Status,
			Detail: deploymentReadinessDetail(deployment),
			Href:   "#config",
		},
		securityReadinessJourneyItem(s.evaluateSecurityReadiness()),
	}

	next := make([]readinessItem, 0)
	for i := range journey {
		switch journey[i].Status {
		case "ok":
		case "warn":
			journey[i].Priority = 20 + i
			next = append(next, journey[i])
		default:
			journey[i].Priority = 10 + i
			next = append(next, journey[i])
		}
	}
	score := readinessScore(journey)
	readyItems, warningItems, blockerItems := readinessStatusCounts(journey)
	enterprise := s.enterpriseParityPosture()
	parityAreas := s.parityAreas(providersReady, usableOutbound, enabledAgents, scheduledAgents, learningAgents, len(templates), updateManifest, enterprise, executors, browser, marketplace, mobile, chat, voice, costs, slo, opsAlerts, studioContracts, docs)
	parityScore := parityScore(parityAreas)
	parityGaps := topParityGaps(parityAreas, 5)
	checklist := buildLaunchChecklist(providers, channels, vault, deployment)
	sort.SliceStable(next, func(i, j int) bool {
		return next[i].Priority < next[j].Priority
	})
	if len(next) > 5 {
		next = next[:5]
	}

	status := "ready"
	for _, item := range journey {
		if item.Status == "fail" {
			status = "needs_setup"
			break
		}
		if item.Status == "warn" && status != "needs_setup" {
			status = "at_risk"
		}
	}

	return fiber.Map{
		"summary": readinessSummary{
			Status:            status,
			Score:             score,
			ReadyItems:        readyItems,
			WarningItems:      warningItems,
			BlockerItems:      blockerItems,
			TotalItems:        len(journey),
			ProvidersReady:    providersReady,
			ChannelsReady:     channelsReady,
			Agents:            agents,
			EnabledAgents:     enabledAgents,
			ChatAgents:        chatAgents,
			ScheduledAgents:   scheduledAgents,
			LearningAgents:    learningAgents,
			Templates:         len(templates),
			UpdatesReady:      updateManifest != "",
			DeploymentProfile: deployment.Profile,
			OpsAlertsReady:    opsAlerts.Status == "ok",
			VoiceReady:        voice.Ready,
			SchedulesReady:    schedules.Status == "ok",
		},
		"journey":          journey,
		"next_actions":     next,
		"launch_checklist": checklist,
		"providers":        providers,
		"channels":         channels,
		"vault":            vault,
		"executors":        executors,
		"browser":          browser,
		"marketplace":      marketplace,
		"mobile":           mobile,
		"chat":             chat,
		"voice":            voice,
		"costs":            costs,
		"slo":              slo,
		"ops_alerts":       opsAlerts,
		"studio_contracts": studioContracts,
		"docs":             docs,
		"schedules":        schedules,
		"deployment":       deployment,
		"parity": fiber.Map{
			"score":    parityScore,
			"areas":    parityAreas,
			"top_gaps": parityGaps,
		},
		"release": fiber.Map{
			"version":         strings.TrimSpace(config.Version),
			"update_manifest": updateManifest,
			"updates_ready":   updateManifest != "",
			"update_hint":     updateManifestHint(updateManifest),
			"install_command": "sy update install --yes",
			"dry_run_command": "sy update install --dry-run",
		},
	}
}

type enterpriseParityPosture struct {
	Controls []string
	Missing  []string
	Score    int
	Status   string
}

func (s *Server) parityAreas(providersReady, usableOutbound, enabledAgents, scheduledAgents, learningAgents, templates int, updateManifest string, enterprise enterpriseParityPosture, executors executorReadiness, browser browserAutomationReadiness, marketplace marketplaceReadiness, mobile mobileCompanionReadiness, chat chatExperienceReadiness, voice voiceReadiness, costs costReadiness, slo sloReadiness, opsAlerts opsAlertReadiness, contracts studioContractReadiness, docs publicDocsReadiness) []parityArea {
	areas := []parityArea{
		parityOnboarding(providersReady, enabledAgents, templates, updateManifest),
		parityChannels(usableOutbound),
		parityStudio(templates, contracts),
		parityChat(chat),
		parityVoice(voice),
		parityLearning(learningAgents, enabledAgents),
		parityAutomation(scheduledAgents, usableOutbound),
		parityOps(providersReady, enabledAgents, updateManifest, costs, slo, opsAlerts),
		parityBrowserAutomation(browser),
		parityRemoteExecution(executors),
		parityMobileCompanion(mobile),
		parityMarketplace(marketplace),
		parityDocsPublishing(docs),
		parityEnterprise(enterprise),
	}
	return areas
}

func (s *Server) enterpriseParityPosture() enterpriseParityPosture {
	controls := make([]string, 0, 5)
	missing := make([]string, 0, 5)

	authReady := s != nil && s.authEngine != nil && s.authEngine.Effective()
	if !authReady && s != nil && s.cfg != nil {
		authReady = strings.TrimSpace(s.cfg.Server.APIKey) != ""
	}
	if authReady {
		controls = append(controls, "authenticated API")
	} else {
		missing = append(missing, "authenticated API")
	}

	if s != nil && s.rbacManager != nil {
		controls = append(controls, "RBAC policies")
	} else {
		missing = append(missing, "RBAC policies")
	}

	if s != nil && s.apiKeyStore != nil {
		controls = append(controls, "managed API keys")
	} else {
		missing = append(missing, "managed API keys")
	}

	if s != nil && s.credVault != nil {
		controls = append(controls, "encrypted credential vault")
	} else {
		missing = append(missing, "encrypted credential vault")
	}

	auditReady := false
	if s != nil && s.cfg != nil {
		auditReady = strings.TrimSpace(s.cfg.Runtime.AuditDir) != ""
	}
	if auditReady {
		controls = append(controls, "audit log directory")
	} else {
		missing = append(missing, "audit log directory")
	}

	score := 25 + len(controls)*10
	status := "fail"
	if len(controls) >= 2 {
		status = "warn"
	}
	if len(controls) >= 5 {
		score = 78
		status = "warn"
	}

	return enterpriseParityPosture{
		Controls: controls,
		Missing:  missing,
		Score:    score,
		Status:   status,
	}
}

func parityEnterprise(posture enterpriseParityPosture) parityArea {
	detail := "Single-workspace local operation is strong, but enterprise controls are not yet configured."
	if len(posture.Controls) > 0 {
		detail = "Enterprise foundations present: " + strings.Join(posture.Controls, ", ") + ". Multi-org tenancy is still not productized."
	}
	next := "Introduce workspaces/orgs, environment separation, and admin audit views; deployment profiles are now visible in launch readiness."
	if len(posture.Missing) > 0 {
		next = "Complete " + strings.Join(posture.Missing, ", ") + "; then add workspaces/orgs, environment separation, and admin audit views."
	}
	return parityArea{
		Key:       "enterprise",
		Label:     "Enterprise Tenancy",
		Status:    posture.Status,
		Score:     posture.Score,
		Detail:    detail,
		Next:      next,
		Benchmark: "Commercial launch",
		Href:      "#config",
	}
}

func parityOnboarding(providersReady, enabledAgents, templates int, updateManifest string) parityArea {
	score := 35
	status := "fail"
	detail := "First-run setup exists, but launch readiness still needs a tested provider, template, and enabled agent."
	if providersReady > 0 && templates > 0 {
		score = 70
		status = "warn"
		detail = "Guided setup can create useful agents; finish with an enabled agent and production update manifest."
	}
	if providersReady > 0 && enabledAgents > 0 && templates > 0 && strings.TrimSpace(updateManifest) != "" {
		score = 90
		status = "ok"
		detail = "First-run path is credible: provider, template, enabled agent, and update manifest are present."
	}
	return parityArea{
		Key:       "onboarding",
		Label:     "Onboarding & Cohesion",
		Status:    status,
		Score:     score,
		Detail:    detail,
		Next:      "Keep reducing setup choices until a new user reaches a working assistant in under ten minutes.",
		Benchmark: "OpenClaw",
		Href:      "#onboarding",
	}
}

func parityChannels(usableOutbound int) parityArea {
	if usableOutbound >= 3 {
		return parityArea{Key: "channels", Label: "Channel Reach", Status: "ok", Score: 84, Detail: plural(usableOutbound, "outbound channel") + " configured; reliability and generic sidecars are now the main work.", Next: "Add generic sidecar packs for Signal, Matrix, iMessage, LINE, and WeChat while keeping delivery diagnostics first-class.", Benchmark: "OpenClaw", Href: "#channels"}
	}
	if usableOutbound > 0 {
		return parityArea{Key: "channels", Label: "Channel Reach", Status: "warn", Score: 55, Detail: plural(usableOutbound, "outbound channel") + " configured; OpenClaw still wins broad reach.", Next: "Add guided setup and delivery tests for more channel adapters and sidecars.", Benchmark: "OpenClaw", Href: "#channels"}
	}
	return parityArea{Key: "channels", Label: "Channel Reach", Status: "fail", Score: 25, Detail: "No outbound messaging channel is production-ready.", Next: "Configure at least one default outbound channel such as Telegram, Slack, Discord, Email, Teams, Google Chat, or Webhook.", Benchmark: "OpenClaw", Href: "#channels"}
}

func parityStudio(templates int, contracts studioContractReadiness) parityArea {
	if contracts.Status == "fail" {
		return parityArea{Key: "studio", Label: "Intent-First Studio", Status: "fail", Score: contracts.Score, Detail: studioContractDetail(contracts), Next: "Open the weakest agent in Studio, run the contract fixes, and do not ship until saved workflows have no contract blockers.", Benchmark: "Soulacy differentiator", Href: "#studio"}
	}
	if contracts.Status == "warn" {
		score := contracts.Score
		if templates == 0 && score > 62 {
			score = 62
		}
		next := "Use Studio contract checks as the release gate for every saved agent."
		if templates == 0 {
			next = "Restore starter templates, then use Studio contract checks as the release gate for every saved agent."
		}
		return parityArea{Key: "studio", Label: "Intent-First Studio", Status: "warn", Score: score, Detail: studioContractDetail(contracts), Next: next, Benchmark: "Soulacy differentiator", Href: "#studio"}
	}
	if templates > 0 {
		score := contracts.Score
		if score < 82 {
			score = 82
		}
		return parityArea{Key: "studio", Label: "Intent-First Studio", Status: "ok", Score: score, Detail: studioContractDetail(contracts), Next: "Keep graph editing secondary to guided plans and keep the contract gate enabled before every save/build.", Benchmark: "Soulacy differentiator", Href: "#studio"}
	}
	return parityArea{Key: "studio", Label: "Intent-First Studio", Status: "warn", Score: 62, Detail: "Studio can scan saved workflow contracts, but starter templates are missing, which weakens guided authoring.", Next: "Restore templates and keep the plan-first authoring path front and center.", Benchmark: "Soulacy differentiator", Href: "#studio"}
}

func parityLearning(learningAgents, enabledAgents int) parityArea {
	if learningAgents > 0 {
		return parityArea{Key: "learning", Label: "Learning Loop", Status: "ok", Score: 78, Detail: plural(learningAgents, "agent") + " can create reviewable lessons; evidence panels make improvement visible.", Next: "Broaden learning evidence across agents and turn repeated successes into installable skills automatically.", Benchmark: "Hermes", Href: "#memory"}
	}
	status := "warn"
	score := 50
	if enabledAgents == 0 {
		status = "fail"
		score = 35
	}
	return parityArea{Key: "learning", Label: "Learning Loop", Status: status, Score: score, Detail: "Learning infrastructure exists, but no enabled agent is using it yet.", Next: "Enable learning on production agents and review accepted/rejected proposals weekly.", Benchmark: "Hermes", Href: "#memory"}
}

func parityAutomation(scheduledAgents, usableOutbound int) parityArea {
	if scheduledAgents > 0 && usableOutbound > 0 {
		return parityArea{Key: "automation", Label: "Scheduled Automations", Status: "ok", Score: 84, Detail: plural(scheduledAgents, "scheduled agent") + " can deliver through configured channels.", Next: "Add schedule reliability alerts and missed-run remediation defaults.", Benchmark: "OpenClaw/Hermes", Href: "#schedule"}
	}
	return parityArea{Key: "automation", Label: "Scheduled Automations", Status: "warn", Score: 58, Detail: "Scheduler exists, but production value needs scheduled agents paired with delivery channels.", Next: "Create one scheduled agent, run a manual test, then verify outbound delivery.", Benchmark: "OpenClaw/Hermes", Href: "#schedule"}
}

func parityScore(areas []parityArea) int {
	if len(areas) == 0 {
		return 0
	}
	total := 0
	for _, area := range areas {
		total += area.Score
	}
	return total / len(areas)
}

func topParityGaps(areas []parityArea, limit int) []parityArea {
	out := make([]parityArea, 0, len(areas))
	for _, area := range areas {
		if area.Status != "ok" {
			out = append(out, area)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score < out[j].Score
	})
	if limit > 0 && len(out) > limit {
		return out[:limit]
	}
	return out
}

func (s *Server) updateManifestSource() string {
	if s != nil && s.cfg != nil {
		if src := strings.TrimSpace(s.cfg.Updates.ManifestURL); src != "" {
			return src
		}
	}
	if src := strings.TrimSpace(os.Getenv("SOULACY_UPDATE_MANIFEST")); src != "" {
		return src
	}
	return ""
}

func readinessStatusCounts(items []readinessItem) (ready, warnings, blockers int) {
	for _, item := range items {
		switch item.Status {
		case "ok":
			ready++
		case "warn":
			warnings++
		default:
			blockers++
		}
	}
	return
}

func readinessScore(items []readinessItem) int {
	if len(items) == 0 {
		return 0
	}
	points := 0
	for _, item := range items {
		switch item.Status {
		case "ok":
			points += 100
		case "warn":
			points += 55
		}
	}
	return points / len(items)
}

func buildLaunchChecklist(providers []doctorProviderCheck, channels []doctorChannelCheck, vault doctorVaultCheck, deployment deploymentReadiness) []launchChecklistItem {
	var out []launchChecklistItem
	add := func(item launchChecklistItem) {
		if item.Status == "" {
			item.Status = "warn"
		}
		out = append(out, item)
	}

	if vault.Status != "ok" {
		add(launchChecklistItem{
			Key:    "vault",
			Label:  "Credential vault",
			Status: vault.Status,
			Detail: vault.Detail,
			Remedy: vault.Remedy,
			Href:   "#secrets",
		})
	}

	for _, p := range providers {
		if p.Status == "ok" {
			continue
		}
		add(launchChecklistItem{
			Key:    "provider:" + p.ID,
			Label:  "Provider · " + p.ID,
			Status: p.Status,
			Detail: p.Detail,
			Remedy: p.Remedy,
			Href:   "#providers",
		})
	}

	for _, ch := range channels {
		if ch.ID == "http" || ch.Status == "ok" {
			continue
		}
		remedy := ""
		if len(ch.Diagnostics) > 0 {
			remedy = ch.Diagnostics[0].Remedy
		}
		add(launchChecklistItem{
			Key:    "channel:" + ch.ID,
			Label:  "Channel · " + ch.ID,
			Status: ch.Status,
			Detail: ch.Detail,
			Remedy: remedy,
			Href:   "#channels",
		})
	}

	for _, check := range deployment.Checks {
		if check.Status == "ok" {
			continue
		}
		add(launchChecklistItem{
			Key:    "deployment:" + check.Key,
			Label:  check.Label,
			Status: check.Status,
			Detail: check.Detail,
			Remedy: deploymentNextAction(check.Key),
			Href:   "#config",
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := checklistRank(out[i].Status), checklistRank(out[j].Status)
		if ri != rj {
			return ri < rj
		}
		return out[i].Key < out[j].Key
	})
	if len(out) == 0 {
		return []launchChecklistItem{{
			Key:    "ready",
			Label:  "Production gate",
			Status: "ok",
			Detail: "Provider, delivery, vault, and deployment checks have no launch blockers.",
			Href:   "#dashboard",
		}}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func checklistRank(status string) int {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "fail":
		return 0
	case "warn":
		return 1
	case "ok":
		return 3
	default:
		return 2
	}
}

func (s *Server) agentReadinessCounts() (agents, enabled, chat, scheduled, learning int) {
	if s.loader == nil {
		return
	}
	for _, def := range s.loader.All() {
		if def == nil || s.loader.IsBuiltin(def.ID) {
			continue
		}
		agents++
		if def.Enabled {
			enabled++
		}
		if def.Trigger == "cron" || (def.Schedule != nil && def.Schedule.Cron != "") {
			scheduled++
		}
		if def.Learning.Enabled || def.Learning.AutoPropose {
			learning++
		}
		if agentHasChatSurface(def.Trigger, def.Channels, def.Surfaces) {
			chat++
		}
	}
	return
}

func (s *Server) studioContractReadiness(c *fiber.Ctx) studioContractReadiness {
	out := studioContractReadiness{Status: "warn", Score: 65}
	if s == nil || s.loader == nil {
		out.WorstSummary = "Agent loader is not available, so saved workflows could not be contract-scanned."
		return out
	}
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	in := s.preflightInput(c, cat)

	totalScore := 0
	worstScore := 101
	for _, def := range s.loader.All() {
		if def == nil || s.loader.IsBuiltin(def.ID) {
			continue
		}
		out.Agents++
		draft := studio.FromAgentDefinition(*def)
		res := studio.AssessContract(draft, cat, in)
		out.Checked++
		totalScore += res.Score
		out.Blockers += res.Blockers
		out.Warnings += res.Warnings
		if res.OK {
			out.Passing++
		}
		if res.Score < worstScore {
			worstScore = res.Score
			out.WorstAgent = strings.TrimSpace(def.Name)
			if out.WorstAgent == "" {
				out.WorstAgent = strings.TrimSpace(def.ID)
			}
			out.WorstSummary = res.Summary
		}
	}
	if out.Checked == 0 {
		out.WorstSummary = "No saved user agents were available for Studio contract scanning."
		return out
	}
	out.Score = totalScore / out.Checked
	switch {
	case out.Blockers == 0 && out.Score >= 80:
		out.Status = "ok"
	case out.Blockers > 0 && out.Score < 50:
		out.Status = "fail"
	default:
		out.Status = "warn"
	}
	return out
}

func studioReadinessItem(templateCount int, contracts studioContractReadiness) readinessItem {
	item := readinessItem{
		Key:    "studio",
		Label:  "Studio",
		Status: contracts.Status,
		Detail: detailStudio(templateCount, contracts),
		Href:   "#studio",
	}
	if item.Status == "" {
		item.Status = statusFromBool(templateCount > 0)
	}
	return item
}

func agentHasChatSurface(trigger any, channels, surfaces []string) bool {
	for _, s := range surfaces {
		if strings.EqualFold(s, "chat") {
			return true
		}
	}
	for _, ch := range channels {
		if strings.EqualFold(ch, "http") || strings.EqualFold(ch, "chat") {
			return true
		}
	}
	triggerText := strings.ToLower(strings.TrimSpace(fmt.Sprint(trigger)))
	return triggerText == "channel" || triggerText == "webhook"
}

func countDoctorProviders(checks []doctorProviderCheck, usable ...string) int {
	return countStatuses(len(checks), func(i int) string { return checks[i].Status }, usable...)
}

func countDoctorChannels(checks []doctorChannelCheck, usable ...string) int {
	return countStatuses(len(checks), func(i int) string { return checks[i].Status }, usable...)
}

func countStatuses(n int, statusAt func(int) string, usable ...string) int {
	allowed := map[string]bool{}
	for _, s := range usable {
		allowed[s] = true
	}
	count := 0
	for i := 0; i < n; i++ {
		if allowed[statusAt(i)] {
			count++
		}
	}
	return count
}

func countUsableOutboundChannels(checks []doctorChannelCheck) int {
	count := 0
	for _, ch := range checks {
		if ch.ID == "http" {
			continue
		}
		if ch.Enabled && ch.Configured && (ch.Connected || ch.Status == "ok" || ch.Status == "warn") {
			count++
		}
	}
	return count
}

func statusFromBool(ok bool) string {
	if ok {
		return "ok"
	}
	return "fail"
}

func providerReadinessItem(providers []doctorProviderCheck, ready int) readinessItem {
	if ready > 0 {
		return readinessItem{
			Key:    "providers",
			Label:  "Model Providers",
			Status: "ok",
			Detail: plural(ready, "provider") + " ready for agent runs.",
			Href:   "#providers",
		}
	}
	status := "fail"
	if len(providers) > 0 {
		status = "warn"
	}
	return readinessItem{
		Key:    "providers",
		Label:  "Model Providers",
		Status: status,
		Detail: "Connect and test at least one LLM provider before building production agents.",
		Href:   "#providers",
	}
}

func agentReadinessItem(agents, enabled, chat, scheduled int) readinessItem {
	if enabled > 0 {
		detail := plural(enabled, "enabled agent")
		if chat > 0 || scheduled > 0 {
			detail += " · " + strconv.Itoa(chat) + " chat-ready · " + strconv.Itoa(scheduled) + " scheduled"
		}
		return readinessItem{Key: "agents", Label: "Deployed Agents", Status: "ok", Detail: detail, Href: "#agents"}
	}
	if agents > 0 {
		return readinessItem{Key: "agents", Label: "Deployed Agents", Status: "warn", Detail: "Agents exist, but none are enabled.", Href: "#agents"}
	}
	return readinessItem{Key: "agents", Label: "Deployed Agents", Status: "fail", Detail: "Create or instantiate an agent from Studio.", Href: "#studio"}
}

func channelReadinessItem(channels []doctorChannelCheck, ready, outbound int) readinessItem {
	if outbound > 0 {
		return readinessItem{
			Key:    "channels",
			Label:  "Delivery Channels",
			Status: "ok",
			Detail: plural(outbound, "outbound channel") + " ready; use channel tests before scheduling.",
			Href:   "#channels",
		}
	}
	if ready > 0 {
		return readinessItem{
			Key:    "channels",
			Label:  "Delivery Channels",
			Status: "warn",
			Detail: "HTTP is available, but no outbound destination is ready for cron or notification agents.",
			Href:   "#channels",
		}
	}
	if len(channels) > 0 {
		return readinessItem{Key: "channels", Label: "Delivery Channels", Status: "warn", Detail: "Channels are present but need credentials, mappings, or a restart.", Href: "#channels"}
	}
	return readinessItem{Key: "channels", Label: "Delivery Channels", Status: "fail", Detail: "Configure Telegram, Slack, Discord, WhatsApp, or another outbound channel.", Href: "#channels"}
}

func learningReadinessItem(learningAgents, enabledAgents int) readinessItem {
	if learningAgents > 0 {
		return readinessItem{
			Key:    "learning",
			Label:  "Learning Loop",
			Status: "ok",
			Detail: plural(learningAgents, "agent") + " can create reviewable learning proposals.",
			Href:   "#memory",
		}
	}
	if enabledAgents > 0 {
		return readinessItem{
			Key:    "learning",
			Label:  "Learning Loop",
			Status: "warn",
			Detail: "Learning is optional, but production agents should capture reviewable lessons from successful runs.",
			Href:   "#memory",
		}
	}
	return readinessItem{
		Key:    "learning",
		Label:  "Learning Loop",
		Status: "warn",
		Detail: "Enable learning after your first production agent is running.",
		Href:   "#memory",
	}
}

// H1 — releaseStatus and releaseDetail were unreferenced after the readiness
// surface consolidated onto deployment/launch checklist rows. Deleted to
// satisfy the unused-linter; if the release lane needs its own status again,
// use deploymentReadiness / buildLaunchChecklist which are the canonical
// sources today.

func deploymentReadinessDetail(deployment deploymentReadiness) string {
	label := strings.TrimSpace(deployment.Label)
	if label == "" {
		label = "Local"
	}
	mode := "advisory"
	if deployment.Strict {
		mode = "strict"
	}
	total := deployment.Total
	if total == 0 {
		total = len(deployment.Checks)
	}
	return fmt.Sprintf("%s profile is %s: %d/%d deployment checks ready.", label, mode, deployment.Ready, total)
}

func updateManifestHint(src string) string {
	if strings.TrimSpace(src) == "" {
		// Not a gap: this is the default and it works. Say what it points at, so
		// an operator who needs a private release host knows the override exists
		// without being told a working install is broken.
		return "Updates come from the built-in " + updates.DefaultSourceDescription() +
			". Set updates.manifest_url only to publish from your own release host."
	}
	return "Run sy update check before rollout and sy update install --dry-run to verify the artifact."
}

func detailTemplates(count int) string {
	if count == 0 {
		return "No starter templates were loaded; first-run setup has nothing guided to deploy."
	}
	return plural(count, "template") + " available for guided setup."
}

func detailStudio(templateCount int, contracts studioContractReadiness) string {
	if contracts.Checked > 0 {
		return studioContractDetail(contracts)
	}
	if templateCount == 0 {
		return "Studio is available, but starter templates are missing."
	}
	return "Studio is the command center for designing, testing, debugging, and saving agents."
}

func studioContractDetail(contracts studioContractReadiness) string {
	if contracts.Checked == 0 {
		if contracts.WorstSummary != "" {
			return contracts.WorstSummary
		}
		return "No saved agents were available for Studio contract scanning."
	}
	detail := fmt.Sprintf("Studio contract scanned %d saved agent(s): %d passing, %d blocker(s), %d warning(s).", contracts.Checked, contracts.Passing, contracts.Blockers, contracts.Warnings)
	if contracts.WorstAgent != "" && contracts.WorstSummary != "" {
		detail += " Weakest: " + contracts.WorstAgent + " — " + contracts.WorstSummary
	}
	return detail
}

func plural(n int, label string) string {
	if n == 1 {
		return "1 " + label
	}
	return strconv.Itoa(n) + " " + strings.TrimSpace(label) + "s"
}
