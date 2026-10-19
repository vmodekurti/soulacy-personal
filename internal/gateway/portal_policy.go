package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/tier"
	"github.com/soulacy/soulacy/pkg/agent"
)

const portalProtectedSettings = "Core operational settings are read-only to Genie: authentication, permissions, networking, storage, runtime defaults and budgets, service lifecycle, shared channels/plugins/tool servers, protected or privileged agents, and providers already in use. Change these directly in the Portal."

// This is an execution boundary, independent of discovery and prompting.
// Adding a catalog entry does not grant permission to change infrastructure.
func (p *portalSession) allowPortalMutation(a portalActionSpec, params, body map[string]any) error {
	if a.Mode == "read" {
		return nil
	}
	deny := func(reason string) error {
		return fmt.Errorf("genie cannot change this protected setting: %s. %s", reason, portalProtectedSettings)
	}
	id, _ := params["id"].(string)
	switch {
	case strings.HasPrefix(a.Name, "agents."):
		switch a.Name {
		case "agents.create", "agents.update", "agents.enable", "agents.disable", "agents.delete", "agents.run":
		default:
			return deny(a.Name)
		}
		if a.Name == "agents.create" {
			id, _ = body["id"].(string)
			for _, saved := range p.server.loader.All() {
				if strings.EqualFold(strings.TrimSpace(id), saved.ID) {
					return fmt.Errorf("agent %s already exists; use agents.update to change ordinary settings without overwriting other fields", id)
				}
			}
		}
		if p.protectedPortalAgent(id) {
			return deny("core or privileged agent " + id)
		}
		if a.Name == "agents.rollback" {
			return deny("version restoration can restore privileged settings")
		}
		if a.Name == "agents.create" || a.Name == "agents.update" {
			allowed := map[string]bool{"id": a.Name == "agents.create", "name": true, "description": true, "system_prompt": true, "llm": true, "enabled": true, "schedule": true, "max_turns": true, "budget": true, "run_timeout": true, "step_timeout": true, "tags": true}
			if a.Name == "agents.create" {
				allowed["trigger"] = true
				allowed["channels"] = true
			}
			for key := range body {
				if !allowed[key] {
					return deny("agent field " + key)
				}
			}
			if a.Name == "agents.create" {
				// HTTP chat or scheduled agents only. Additional tool/host grants and
				// externally reachable channel bindings stay in the explicit Portal UI.
				if channels, ok := body["channels"].([]any); ok {
					for _, v := range channels {
						if v != "http" {
							return deny("external channel binding")
						}
					}
				}
				if trigger, ok := body["trigger"].(string); ok && trigger != "channel" && trigger != "cron" {
					return deny("agent trigger " + trigger)
				}
			}
			// Model configuration is a typed struct; unknown nested keys must not
			// become an alternate spelling for privileged fields.
			if llm, ok := body["llm"].(map[string]any); ok {
				for key := range llm {
					switch key {
					case "provider", "model", "temperature", "max_tokens", "top_p", "reasoning_effort", "presence_penalty", "frequency_penalty":
					default:
						return deny("model field " + key)
					}
				}
			}
		}
		return nil
	case a.Name == "providers.save" || a.Name == "providers.model" || a.Name == "providers.delete":
		if p.protectedPortalProvider(id) {
			return deny("provider " + id + " is in use or has stored credentials")
		}
		if a.Name == "providers.save" {
			if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`).MatchString(id) {
				return fmt.Errorf("provider id must use lowercase letters, numbers, underscores or hyphens")
			}
			if raw, ok := body["base_url"].(string); ok {
				u, err := url.Parse(raw)
				if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
					return fmt.Errorf("provider base_url must be an HTTP(S) endpoint without credentials or query parameters")
				}
			}
			for key := range body {
				switch key {
				case "base_url", "model", "local", "region", "retention", "prompt_caching", "thinking_budget", "parallel_tool_calls", "keep_alive":
				default:
					return deny("provider field " + key)
				}
			}
		}
		return nil
	case a.Name == "workboard.run":
		if p.server.workboardStore == nil {
			return fmt.Errorf("workboard is unavailable")
		}
		taskID, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid task id")
		}
		task, err := p.server.workboardStore.Get(context.Background(), taskID)
		if err != nil {
			return err
		}
		if p.protectedPortalAgent(task.AgentID) {
			return deny("workboard task uses a protected agent")
		}
		return nil
	case a.Name == "workboard.create" || a.Name == "workboard.update":
		if agentID, ok := body["agent_id"].(string); ok && p.protectedPortalAgent(agentID) {
			return deny("workboard task uses a protected agent")
		}
		return nil
	case a.Name == "providers.pull":
		return nil
	case strings.HasPrefix(a.Name, "knowledge."), strings.HasPrefix(a.Name, "workboard."), strings.HasPrefix(a.Name, "memory."), strings.HasPrefix(a.Name, "person."), strings.HasPrefix(a.Name, "schedule."), a.Name == "skills.rescan":
		return nil
	default:
		return deny(a.Name)
	}
}

func (p *portalSession) protectedPortalProvider(id string) bool {
	cfg := p.server.cfg
	if id == "" || id == cfg.LLM.DefaultProvider || id == cfg.LLM.Studio.Provider {
		return true
	}
	if pc, ok := cfg.LLM.Providers[id]; ok && pc.APIKey != "" {
		return true
	}
	// A provider referenced by any saved agent (even disabled) is protected;
	// model preparation and future schedules must keep working unchanged.
	for _, def := range p.server.loader.All() {
		if agentUsesPortalProvider(def, id) {
			return true
		}
	}
	return false
}
func agentUsesPortalProvider(def *agent.Definition, id string) bool {
	if def == nil {
		return false
	}
	if def.LLM.Provider == id {
		return true
	}
	// Inspect all typed provider references, including workflow steps and
	// fallback model settings, without serializing them into a model response.
	// The primary LLM covers normal agents; source traversal is below.
	return portalDefinitionUsesProvider(def, id)
}

func portalDefinitionUsesProvider(def *agent.Definition, id string) bool {
	raw, err := json.Marshal(def)
	if err != nil {
		return true
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	var uses func(any) bool
	uses = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for key, value := range x {
				if (key == "provider" || key == "fallback_provider") && value == id {
					return true
				}
				if uses(value) {
					return true
				}
			}
		case []any:
			for _, value := range x {
				if uses(value) {
					return true
				}
			}
		}
		return false
	}
	return uses(value)
}

func (p *portalSession) protectedPortalAgent(id string) bool {
	id = strings.TrimSpace(id)
	if strings.EqualFold(id, runtime.GenieAgentID) || strings.EqualFold(id, runtime.SystemAgentID) {
		return true
	}
	for _, def := range p.server.loader.All() {
		if strings.EqualFold(id, def.ID) && tier.Compute(def, p.server.loader.Get) == tier.Privileged {
			return true
		}
	}
	return false
}
