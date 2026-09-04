package gateway

import (
	"sort"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/templates"
)

type onboardingStep struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Status   string `json:"status"` // ok|warn|todo
	Detail   string `json:"detail"`
	Href     string `json:"href,omitempty"`
	Priority int    `json:"priority,omitempty"`
}

func (s *Server) handleOnboardingStatus(c *fiber.Ctx) error {
	providers := s.providerDoctorChecks(c)
	channels := s.channelDoctorChecks()
	templates, _ := s.templatesCatalog().List()
	s.applyTemplateRuntimeDefaults(templates)

	steps := []onboardingStep{
		s.onboardingProviderStep(providers),
		s.onboardingAgentStep(),
		s.onboardingChannelStep(channels),
		s.onboardingTemplateStep(len(templates)),
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Priority < steps[j].Priority })

	complete := true
	for _, st := range steps {
		if st.Status == "todo" {
			complete = false
			break
		}
	}

	return c.JSON(fiber.Map{
		"complete":            complete,
		"steps":               steps,
		"providers":           providers,
		"channels":            channels,
		"suggested_templates": firstOnboardingTemplates(templates, 4),
	})
}

func (s *Server) onboardingProviderStep(checks []doctorProviderCheck) onboardingStep {
	if len(checks) == 0 {
		return onboardingStep{Key: "provider", Label: "Connect a model", Status: "todo", Detail: "No LLM providers are configured yet.", Href: "providers", Priority: 10}
	}
	for _, ch := range checks {
		if ch.Status == "ok" {
			return onboardingStep{Key: "provider", Label: "Connect a model", Status: "ok", Detail: "Provider " + ch.ID + " is ready.", Href: "providers", Priority: 10}
		}
	}
	for _, ch := range checks {
		if ch.Status == "warn" {
			return onboardingStep{Key: "provider", Label: "Connect a model", Status: "warn", Detail: ch.ID + ": " + ch.Detail, Href: "providers", Priority: 10}
		}
	}
	return onboardingStep{Key: "provider", Label: "Connect a model", Status: "todo", Detail: checks[0].ID + ": " + checks[0].Detail, Href: "providers", Priority: 10}
}

func (s *Server) onboardingAgentStep() onboardingStep {
	total := 0
	enabled := 0
	if s.loader != nil {
		for _, def := range s.loader.All() {
			if s.loader.IsBuiltin(def.ID) {
				continue
			}
			total++
			if def.Enabled {
				enabled++
			}
		}
	}
	if enabled > 0 {
		return onboardingStep{Key: "agent", Label: "Deploy an agent", Status: "ok", Detail: "You have enabled agents ready to run.", Href: "studio", Priority: 20}
	}
	if total > 0 {
		return onboardingStep{Key: "agent", Label: "Deploy an agent", Status: "warn", Detail: "Agents exist, but none are enabled.", Href: "agents", Priority: 20}
	}
	return onboardingStep{Key: "agent", Label: "Build in Studio", Status: "todo", Detail: "Start from a template or generate a workflow in Studio.", Href: "studio", Priority: 20}
}

func (s *Server) onboardingChannelStep(checks []doctorChannelCheck) onboardingStep {
	for _, ch := range checks {
		if ch.Enabled && ch.Configured && ch.Registered && ch.Status == "ok" && ch.ID != "http" {
			return onboardingStep{Key: "channel", Label: "Configure delivery", Status: "ok", Detail: "Channel " + ch.ID + " is connected.", Href: "channels", Priority: 30}
		}
	}
	for _, ch := range checks {
		if ch.Configured && ch.ID != "http" {
			return onboardingStep{Key: "channel", Label: "Configure delivery", Status: "warn", Detail: ch.ID + ": " + ch.Detail, Href: "channels", Priority: 30}
		}
	}
	return onboardingStep{Key: "channel", Label: "Configure delivery", Status: "todo", Detail: "Add Telegram, Slack, Discord, WhatsApp, or another output channel.", Href: "channels", Priority: 30}
}

func (s *Server) onboardingTemplateStep(count int) onboardingStep {
	if count > 0 {
		return onboardingStep{Key: "template", Label: "Start from a template", Status: "ok", Detail: "Starter templates are available.", Href: "templates", Priority: 40}
	}
	return onboardingStep{Key: "template", Label: "Start from a template", Status: "warn", Detail: "No templates loaded from embedded or user template folders.", Href: "templates", Priority: 40}
}

func firstOnboardingTemplates(entries []templates.Entry, limit int) []templates.Entry {
	if len(entries) <= limit {
		return entries
	}
	return entries[:limit]
}
