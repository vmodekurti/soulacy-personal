// securitydoctor.go — S7 (Cohort F) HTTP surface for the Security
// Doctor. Two endpoints: one that returns the per-agent report, one
// that runs the "what if this page asks the agent to shell out" dry-run.
package gateway

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/securitydoctor"
	"github.com/soulacy/soulacy/pkg/agent"
)

// handleSecurityDoctor implements GET /api/v1/agents/:id/security_doctor.
// Assembles the full Doctor report by consulting the runtime.Loader
// (peer walks + tier), the configured channel bindings (shared /
// accepted flags), and the process-level sandbox backend.
func (s *Server) handleSecurityDoctor(c *fiber.Ctx) error {
	agentID := strings.TrimSpace(c.Params("id"))
	if agentID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "missing agent id")
	}
	if s.loader == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "loader not initialised")
	}
	def := s.loader.Get(agentID)
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}

	rep := securitydoctor.Build(securitydoctor.Input{
		Definition:                 def,
		Loader:                     s.loader.Get,
		ChannelBindings:            s.channelBindingsForAgent(agentID),
		SandboxBackend:             s.sandboxBackendLabel(),
		WorkspaceIntentGateDefault: s.workspaceIntentGateDefault(),
	})
	return c.JSON(rep)
}

// workspaceIntentGateDefault returns the F-Bridge workspace-scoped default
// intent-gate mode. Empty when no workspace value is configured, in which
// case the per-agent SOUL.yaml value is authoritative (and both empty falls
// through to intent.ModePrompt at runtime).
func (s *Server) workspaceIntentGateDefault() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	return strings.TrimSpace(s.cfg.Security.IntentGate)
}

// handleSecurityDoctorDryRun implements POST /api/v1/agents/:id/security_doctor/dry_run.
// Body carries the DryRunInput; the response is the Doctor's dry-run
// verdict. Nothing is executed — the scanner + intent gate run against
// the provided injected content and follow-up tool.
func (s *Server) handleSecurityDoctorDryRun(c *fiber.Ctx) error {
	agentID := strings.TrimSpace(c.Params("id"))
	if agentID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "missing agent id")
	}
	var req securitydoctor.DryRunInput
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	var def *agent.Definition
	if s.loader != nil {
		def = s.loader.Get(agentID)
	}
	// F-Bridge — thread the workspace default through so simulations use the
	// same effective mode as the runtime would when the per-agent value is
	// unset. Operator-supplied WorkspaceIntentGateDefault (if any) still wins
	// so the doctor can also be used to test "what if I switched to deny?".
	if req.WorkspaceIntentGateDefault == "" {
		req.WorkspaceIntentGateDefault = s.workspaceIntentGateDefault()
	}
	return c.JSON(securitydoctor.DryRun(def, req))
}

// channelBindingsForAgent enumerates the config-level bindings that
// currently target `agentID`. Renders one ChannelEntry per binding
// with the shared-external flag and the accepted flag pulled from the
// binding's accept_privileged_exposure key.
func (s *Server) channelBindingsForAgent(agentID string) []securitydoctor.ChannelEntry {
	var out []securitydoctor.ChannelEntry
	if s == nil || s.cfg == nil {
		return out
	}
	shared := sharedExternalChannels()
	for kind, raw := range s.cfg.Channels {
		if raw == nil {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(kind))
		if enabled, ok := raw["enabled"].(bool); ok && !enabled {
			continue
		}
		if aid := stringValue(raw, "agent_id"); aid == agentID {
			out = append(out, entryFromBinding(kind, shared[key], raw))
		}
		if list, ok := raw["bots"].([]any); ok {
			for _, item := range list {
				bot, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if stringValue(bot, "agent_id") != agentID {
					continue
				}
				name := stringValue(bot, "bot_name")
				if name == "" {
					name = stringValue(bot, "name")
				}
				label := kind
				if name != "" {
					label = kind + "/" + name
				}
				out = append(out, entryFromBinding(label, shared[key], bot))
			}
		}
	}
	return out
}

func entryFromBinding(name string, shared bool, bindingCfg map[string]any) securitydoctor.ChannelEntry {
	accepted, _ := bindingCfg["accept_privileged_exposure"].(bool)
	return securitydoctor.ChannelEntry{Name: name, Shared: shared, Accepted: accepted}
}

// sandboxBackendLabel returns a short string describing the active
// process-sandbox backend. Reads from the config the runtime layer
// already consumes.
func (s *Server) sandboxBackendLabel() string {
	if s == nil || s.cfg == nil {
		return "unknown"
	}
	if !s.cfg.Runtime.Sandbox.Enabled {
		return "disabled"
	}
	if s.cfg.Runtime.Sandbox.MemoryMB > 0 || s.cfg.Runtime.Sandbox.CPUSeconds > 0 {
		return "linux-rlimits"
	}
	return "advisory"
}
