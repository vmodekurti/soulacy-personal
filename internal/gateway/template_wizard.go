// template_wizard.go — live readiness + no-side-effect mock test for the
// Template Install Wizard (Epic 2).
//
//   - handleTemplateReadiness inspects the ACTUAL vault, providers, and channels
//     to report which of a template's requirements are satisfied — a live
//     checklist, not the static inference the catalog returns.
//   - handleTemplateMockTest returns a deterministic dry-run preview of what the
//     template's agent would do, without calling any real tool, model, or channel.

package gateway

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/secrets"
	"github.com/soulacy/soulacy/pkg/agent"
)

type readinessCheck struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Category  string `json:"category"` // provider | secret | channel | mcp
	Satisfied bool   `json:"satisfied"`
	Optional  bool   `json:"optional"`
	Detail    string `json:"detail,omitempty"`
}

func (s *Server) handleTemplateReadiness(c *fiber.Ctx) error {
	name := c.Params("name")
	entry, err := s.templatesCatalog().Get(name)
	if err != nil || entry == nil || entry.Definition == nil {
		return s.errMsg(c, fiber.StatusNotFound, "unknown template: "+name)
	}
	def := entry.Definition
	var checks []readinessCheck

	// Provider: configured (registered) in the LLM config, or local (ollama).
	provider := strings.TrimSpace(def.LLM.Provider)
	if provider == "" {
		provider = strings.TrimSpace(s.cfg.LLM.DefaultProvider)
	}
	if provider != "" {
		_, configured := s.cfg.LLM.Providers[provider]
		local := provider == "ollama"
		checks = append(checks, readinessCheck{
			Key: "provider:" + provider, Label: "LLM provider: " + provider, Category: "provider",
			Satisfied: configured || local,
			Detail:    providerDetail(configured, local),
		})
	}

	// Secrets: live vault lookup.
	mgr := secrets.New(s.CredentialVault())
	for _, rs := range entry.RequiredSecrets {
		satisfied := false
		if mgr.Enabled() {
			if v, ok := mgr.Get(c.UserContext(), rs.Key); ok && strings.TrimSpace(v) != "" {
				satisfied = true
			}
		}
		detail := rs.Reason
		if !mgr.Enabled() {
			detail = "credential vault is not enabled"
		}
		checks = append(checks, readinessCheck{
			Key: "secret:" + rs.Key, Label: rs.Label, Category: "secret",
			Satisfied: satisfied, Detail: detail,
		})
	}

	// Channels the template delivers to: configured in the channels block?
	for _, ch := range declaredChannels(def) {
		_, configured := s.cfg.Channels[ch]
		checks = append(checks, readinessCheck{
			Key: "channel:" + ch, Label: "Output channel: " + ch, Category: "channel",
			Satisfied: configured, Optional: true,
			Detail: channelReadyDetail(configured),
		})
	}

	// MCP servers the template declares.
	if def.MCPServers != nil {
		for _, srv := range *def.MCPServers {
			srv = strings.TrimSpace(srv)
			if srv == "" {
				continue
			}
			_, configured := s.cfg.MCP.Servers[srv]
			checks = append(checks, readinessCheck{
				Key: "mcp:" + srv, Label: "MCP server: " + srv, Category: "mcp",
				Satisfied: configured,
				Detail:    mcpReadyDetail(configured),
			})
		}
	}

	ready := true
	for _, ch := range checks {
		if !ch.Satisfied && !ch.Optional {
			ready = false
			break
		}
	}
	return c.JSON(fiber.Map{"template": name, "ready": ready, "checks": checks})
}

func providerDetail(configured, local bool) string {
	switch {
	case configured:
		return "configured"
	case local:
		return "local provider — no key needed (ensure it is running)"
	default:
		return "not configured — add credentials in Providers"
	}
}

func channelReadyDetail(configured bool) string {
	if configured {
		return "configured"
	}
	return "optional — configure this channel to enable delivery"
}

func mcpReadyDetail(configured bool) string {
	if configured {
		return "configured"
	}
	return "not configured — add this MCP server before installing"
}

// declaredChannels collects the channels a template delivers to, from its
// channel bindings and its scheduled-output channel.
func declaredChannels(def *agent.Definition) []string {
	seen := map[string]bool{}
	var out []string
	add := func(ch string) {
		ch = strings.TrimSpace(ch)
		if ch == "" || seen[ch] {
			return
		}
		seen[ch] = true
		out = append(out, ch)
	}
	for _, ch := range def.Channels {
		add(ch)
	}
	if def.Schedule != nil && def.Schedule.Output != nil {
		add(def.Schedule.Output.Channel)
	}
	return out
}

// handleTemplateMockTest returns a deterministic, side-effect-free preview of
// what the template's agent would do when it runs.
func (s *Server) handleTemplateMockTest(c *fiber.Ctx) error {
	name := c.Params("name")
	entry, err := s.templatesCatalog().Get(name)
	if err != nil || entry == nil || entry.Definition == nil {
		return s.errMsg(c, fiber.StatusNotFound, "unknown template: "+name)
	}
	def := entry.Definition

	var plan []string
	// Trigger
	if def.Trigger == agent.TriggerCron && def.Schedule != nil && def.Schedule.Cron != "" {
		plan = append(plan, "Trigger: runs on schedule ("+def.Schedule.Cron+")")
	} else {
		plan = append(plan, "Trigger: runs on demand / when messaged")
	}
	// Tools
	tools := mockToolNames(def)
	if len(tools) > 0 {
		plan = append(plan, "Would use tools: "+strings.Join(tools, ", ")+" (mocked — no real calls)")
	} else {
		plan = append(plan, "Reasons with the model; no external tools")
	}
	// Knowledge
	if len(def.Knowledge) > 0 {
		plan = append(plan, "Reads knowledge bases: "+strings.Join(def.Knowledge, ", "))
	}
	// Delivery
	chans := declaredChannels(def)
	if len(chans) > 0 {
		plan = append(plan, "Would deliver output to: "+strings.Join(chans, ", ")+" (mocked — nothing is sent)")
	} else {
		plan = append(plan, "Returns the result in chat")
	}

	return c.JSON(fiber.Map{
		"template": name,
		"ok":       true,
		"mock":     true,
		"plan":     plan,
		"note":     "This is a dry run. No model, tool, or channel was actually called.",
	})
}

func mockToolNames(def *agent.Definition) []string {
	var out []string
	seen := map[string]bool{}
	add := func(n string) {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		out = append(out, n)
	}
	if def.Builtins != nil {
		for _, b := range *def.Builtins {
			add(b)
		}
	}
	for _, t := range def.Tools {
		add(t.Name)
	}
	return out
}
