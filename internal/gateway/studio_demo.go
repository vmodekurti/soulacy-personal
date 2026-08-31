package gateway

import (
	"encoding/json"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/pkg/agent"
)

// demoStudioMW is the server-side boundary for the public Studio. UI hiding is
// only presentation; this middleware is what keeps a demo principal from
// reaching shared authoring state or smuggling an unapproved provider/tool in
// a hand-written request.
func (s *Server) demoStudioMW() fiber.Handler {
	return func(c *fiber.Ctx) error {
		identity, ok := requestIdentity(c)
		if !ok || identity.Role() != tenancy.RoleDemoDeveloper {
			return c.Next()
		}
		cfg := s.config().PublicDemo
		if !cfg.Enabled || strings.TrimSpace(identity.WorkspaceID()) != strings.TrimSpace(cfg.WorkspaceID) {
			return s.errMsg(c, fiber.StatusForbidden, "public demo access is not enabled for this workspace")
		}
		path := strings.TrimPrefix(c.Path(), "/api/v1")
		for _, blocked := range []string{
			"/studio/save", "/studio/save-yaml", "/studio/rules", "/studio/learning-memory",
			"/studio/agents",
			"/studio/failed-runs", "/studio/run-trace", "/studio/run-diagnosis", "/studio/run-history",
			"/studio/build-trace", "/studio/build-traces",
			"/studio/diagnose-run", "/studio/diagnose-session", "/studio/repair-live", "/studio/apply-repair",
			"/studio/troubleshoot", "/studio/codegen",
		} {
			if path == blocked || strings.HasPrefix(path, blocked+"/") {
				return s.errMsg(c, fiber.StatusForbidden, "this action is unavailable in the public demo")
			}
		}
		if len(c.Body()) == 0 {
			return c.Next()
		}
		var body any
		if err := json.Unmarshal(c.Body(), &body); err != nil {
			return s.errMsg(c, fiber.StatusBadRequest, "public demo Studio requests must use a valid JSON body")
		}
		allowedTools := normalizedSet(cfg.AllowedTools)
		allowedProviders := normalizedSet(cfg.AllowedProviders)
		allowedModels := normalizedSet(cfg.AllowedModels)
		if reason := validateDemoValue(body, "", allowedTools, allowedProviders, allowedModels); reason != "" {
			return s.errMsg(c, fiber.StatusForbidden, reason)
		}
		return c.Next()
	}
}

// demoSafeAgentMW permits public visitors to run only deliberately curated,
// read-only showcase agents. The label is an explicit second opt-in beyond
// RBAC, so publishing a normal workspace agent can never expose it to demo
// visitors by accident.
func (s *Server) demoSafeAgentMW(source rbac.AgentIDSource) fiber.Handler {
	return func(c *fiber.Ctx) error {
		identity, ok := requestIdentity(c)
		if !ok || identity.Role() != tenancy.RoleDemoDeveloper {
			return c.Next()
		}
		cfg := s.config().PublicDemo
		if !cfg.Enabled || strings.TrimSpace(identity.WorkspaceID()) != strings.TrimSpace(cfg.WorkspaceID) {
			return s.errMsg(c, fiber.StatusForbidden, "public demo access is not enabled for this workspace")
		}
		agentID := strings.TrimSpace(c.Params(source.PathParam))
		if agentID == "" {
			agentID = strings.TrimSpace(c.Query(source.QueryParam))
		}
		if agentID == "" && source.BodyField != "" && len(c.Body()) > 0 {
			var body map[string]any
			if json.Unmarshal(c.Body(), &body) == nil {
				agentID, _ = body[source.BodyField].(string)
				agentID = strings.TrimSpace(agentID)
			}
		}
		def := s.agents(c).Get(agentID)
		if reason := validatePublicDemoAgent(def, normalizedSet(cfg.AllowedTools), normalizedSet(cfg.AllowedProviders), normalizedSet(cfg.AllowedModels)); reason != "" {
			return s.errMsg(c, fiber.StatusForbidden, reason)
		}
		return c.Next()
	}
}

func (s *Server) denyDemoMW(message string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if identity, ok := requestIdentity(c); ok && identity.Role() == tenancy.RoleDemoDeveloper {
			return s.errMsg(c, fiber.StatusForbidden, message)
		}
		return c.Next()
	}
}

func validatePublicDemoAgent(def *agent.Definition, tools, providers, models map[string]bool) string {
	if def == nil || strings.ToLower(strings.TrimSpace(def.Labels["soulacy.public_demo"])) != "true" {
		return "this agent is not available in the public demo"
	}
	if !providers[strings.ToLower(strings.TrimSpace(def.LLM.Provider))] || !models[strings.ToLower(strings.TrimSpace(def.LLM.Model))] {
		return "this agent uses a model outside the public demo allowance"
	}
	if def.Builtins == nil {
		return "public demo agents require an explicit tool allowlist"
	}
	for _, name := range *def.Builtins {
		if !tools[strings.ToLower(strings.TrimSpace(name))] {
			return "this agent uses a tool outside the public demo allowance"
		}
	}
	if len(def.Tools)+len(def.Skills)+len(def.Connections)+len(def.Knowledge)+len(def.Agents)+len(def.Channels)+len(def.Env)+len(def.ConfirmTools)+len(def.Hooks) > 0 ||
		def.MCPServers != nil || def.MCPTools != nil || def.PluginTools != nil || def.SystemTools || def.AllowShell || len(def.Capabilities) > 0 ||
		def.Unattended || def.Schedule != nil || def.Webhook != nil || def.Workflow != nil {
		return "this agent has capabilities that are unavailable in the public demo"
	}
	return ""
}

func normalizedSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
			out[value] = true
		}
	}
	return out
}

func validateDemoValue(value any, key string, tools, providers, models map[string]bool) string {
	key = strings.ToLower(strings.TrimSpace(key))
	if (key == "code" || key == "source_code" || key == "script") && demoValuePresent(value) {
		return "custom code is unavailable in the public demo"
	}
	if (key == "skills" || key == "mcp_servers" || key == "channels" || key == "connections" || key == "knowledge" || key == "new_agents") && demoValuePresent(value) {
		return "shared skills, MCP servers, connections, channels, knowledge, and agents cannot be attached in the public demo"
	}
	switch current := value.(type) {
	case map[string]any:
		if kind, _ := current["kind"].(string); strings.ToLower(strings.TrimSpace(kind)) == "python" || strings.ToLower(strings.TrimSpace(kind)) == "agent" {
			return "custom code and agent delegation are unavailable in the public demo"
		}
		if unattended, _ := current["unattended"].(bool); unattended {
			return "unattended execution is unavailable in the public demo"
		}
		if key == "tools" || key == "tool_allowlist" || key == "capabilities" {
			structuredTool := false
			for _, field := range []string{"name", "id", "tool"} {
				if raw, exists := current[field]; exists {
					structuredTool = true
					if candidate, ok := raw.(string); ok && !tools[strings.ToLower(strings.TrimSpace(candidate))] {
						return "that tool is not available in the public demo"
					}
				}
			}
			if !structuredTool {
				for name, enabled := range current {
					if demoValuePresent(enabled) && !tools[strings.ToLower(strings.TrimSpace(name))] {
						return "that tool is not available in the public demo"
					}
				}
			}
		}
		for childKey, child := range current {
			if reason := validateDemoValue(child, childKey, tools, providers, models); reason != "" {
				return reason
			}
		}
	case []any:
		for _, child := range current {
			if reason := validateDemoValue(child, key, tools, providers, models); reason != "" {
				return reason
			}
		}
	case string:
		candidate := strings.ToLower(strings.TrimSpace(current))
		if candidate == "" {
			return ""
		}
		switch {
		case key == "provider" || strings.HasSuffix(key, "_provider"):
			if !providers[candidate] {
				return "that model provider is not available in the public demo"
			}
		case key == "model" || strings.HasSuffix(key, "_model"):
			if !models[candidate] {
				return "that model is not available in the public demo"
			}
		case key == "tools" || key == "tool_allowlist" || key == "capabilities" || key == "tool":
			if !tools[candidate] {
				return "that tool is not available in the public demo"
			}
		}
	}
	return ""
}

func demoValuePresent(value any) bool {
	switch current := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(current) != ""
	case []any:
		return len(current) > 0
	case map[string]any:
		return len(current) > 0
	case bool:
		return current
	default:
		return true
	}
}
