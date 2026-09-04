package app

// Channel wiring helpers (Story E10): adapter construction goes through the
// SDK factory registry; the host keeps only config-shape handling (multi-bot
// lists, adapter id derivation, capability-tier binding gate).

import (
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/tier"
	"github.com/soulacy/soulacy/sdk/registry"
)

// buildChannel resolves a channel adapter through the SDK factory registry.
// The bot/channel config map is passed to the factory verbatim, plus the
// host-chosen adapter id (when non-empty) and the process logger under the
// documented host-internal "logger" key.
func buildChannel(name, adapterID string, cfg map[string]any, log *zap.Logger) (channels.Adapter, error) {
	m := make(map[string]any, len(cfg)+2)
	for k, v := range cfg {
		m[k] = v
	}
	if adapterID != "" {
		m["id"] = adapterID
	}
	m["logger"] = log
	a, ok, err := registry.NewChannel(name, m)
	if !ok {
		return nil, fmt.Errorf("no channel factory registered for %q (registered: %v)", name, registry.Channels())
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

// providerCfgMap converts a config.ProviderConfig struct into the schemaless
// map the SDK provider factories consume. Zero values are omitted so factory
// defaults apply.
func providerCfgMap(p config.ProviderConfig) map[string]any {
	m := map[string]any{}
	if p.BaseURL != "" {
		m["base_url"] = p.BaseURL
	}
	if p.APIKey != "" {
		m["api_key"] = p.APIKey
	}
	if p.Model != "" {
		m["model"] = p.Model
	}
	if p.KeepAlive != "" {
		m["keep_alive"] = p.KeepAlive
	}
	if p.Options != nil {
		m["options"] = p.Options
	}
	if p.PromptCaching {
		m["prompt_caching"] = true
	}
	if p.ExtendedThinking {
		m["extended_thinking"] = true
	}
	if p.ThinkingBudget != 0 {
		m["thinking_budget"] = p.ThinkingBudget
	}
	if p.SafetyLevel != "" {
		m["safety_level"] = p.SafetyLevel
	}
	if p.Organization != "" {
		m["organization"] = p.Organization
	}
	if p.ParallelToolCalls != nil {
		m["parallel_tool_calls"] = p.ParallelToolCalls
	}
	return m
}

// providerKeyFor returns the API key for an llm.providers entry, falling back
// to the named environment variable — the same precedence the provider
// factories apply. Used to wire reasoning loop backends (Story 16).
func providerKeyFor(cfg *config.Config, providerID, envVar string) string {
	if pc, ok := cfg.LLM.Providers[providerID]; ok && pc.APIKey != "" {
		return pc.APIKey
	}
	return os.Getenv(envVar)
}

func adapterIDForLog(channel string, index int, agentID string) string {
	if index == 0 {
		return channel
	}
	return channel + "-" + sanitizeID(agentID)
}

// bindingDecision is the policy gate for binding `agentID` to a channel
// adapter. It generalises the prior ID-based system-agent block into a
// tier-based check (see docs/CHANNEL_DESIGN.md Q1).
//
// Policy:
//
//   • channelKind == "http"          — always allowed. The HTTP channel is
//                                      gated by the gateway's API-key auth,
//                                      so binding doesn't escalate exposure
//                                      beyond what's already authenticated.
//
//   • tier == ReadOnly               — allowed on any channel, silently.
//
//   • tier == Active                 — allowed with an INFO log noting the
//                                      tier. This is the backward-compat
//                                      path: pre-existing single-agent
//                                      bindings (web_search, kb_search,
//                                      etc.) keep working.
//
//   • tier == Privileged             — requires `accept_privileged_exposure:
//                                      true` on the binding map. Allowed
//                                      with a stark WARN when accepted;
//                                      blocked with a stark WARN otherwise.
//                                      Catches shell_exec, write_file,
//                                      system_tools, wildcard builtins/MCP,
//                                      and any transitive peer that has
//                                      those capabilities.
//
//   • tier == Unknown                — agent isn't loaded; allow with a
//                                      WARN (engine errors at first run).
//
// `bindingCfg` is the raw channel-binding map from config.yaml; we read
// `accept_privileged_exposure` (bool) from it. The flag MUST live on the
// binding rather than on the agent definition — the operator deploying an
// agent to a public channel is the one accepting the risk, not the agent
// author.
//
// Always emits one structured log line per binding decision so operators
// can audit `grep "channel binding"` in /tmp/soulacy.log.
func bindingDecision(adapterID, agentID, channelKind string, bindingCfg map[string]any, loader *runtime.Loader, log *zap.Logger) bool {
	agentID = strings.TrimSpace(agentID)
	channelKind = strings.TrimSpace(channelKind)

	// Web (http) is gated by API-key auth at the gateway; no tier check needed.
	if channelKind == "http" {
		log.Info("channel binding: web",
			zap.String("adapter_id", adapterID),
			zap.String("channel", channelKind),
			zap.String("agent_id", agentID),
			zap.String("decision", "allow"),
		)
		return true
	}

	if loader == nil {
		// Defensive: classification needs the loader for transitive peer
		// reach. Without it, fail closed for non-web channels — operators
		// should not be silently mis-classified.
		log.Warn("channel binding: classification skipped (loader nil); refusing non-web binding",
			zap.String("adapter_id", adapterID),
			zap.String("channel", channelKind),
			zap.String("agent_id", agentID),
			zap.String("decision", "block"),
		)
		return false
	}

	def := loader.Get(agentID)
	if def == nil {
		// Agent not loaded yet — let it through with a warn; the engine's
		// per-message lookup will error at runtime if it stays missing.
		log.Warn("channel binding: agent not loaded; allowing (engine will refuse at runtime if missing)",
			zap.String("adapter_id", adapterID),
			zap.String("channel", channelKind),
			zap.String("agent_id", agentID),
			zap.String("decision", "allow_unloaded"),
		)
		return true
	}

	t := tier.Compute(def, loader.Get)
	accept, _ := bindingCfg["accept_privileged_exposure"].(bool)

	switch t {
	case tier.ReadOnly:
		log.Info("channel binding: read-only",
			zap.String("adapter_id", adapterID),
			zap.String("channel", channelKind),
			zap.String("agent_id", agentID),
			zap.String("tier", t.String()),
			zap.String("decision", "allow"),
		)
		return true

	case tier.Active:
		// Backward compat: active is allowed by default on non-web. The
		// log captures the decision so operators see the new tier in
		// /tmp/soulacy.log without anything breaking.
		log.Info("channel binding: active (allowed by default — set accept_privileged_exposure:false to deny)",
			zap.String("adapter_id", adapterID),
			zap.String("channel", channelKind),
			zap.String("agent_id", agentID),
			zap.String("tier", t.String()),
			zap.String("decision", "allow"),
		)
		return true

	case tier.Privileged:
		if accept {
			log.Warn("channel binding: PRIVILEGED agent exposed (operator accepted via accept_privileged_exposure: true)",
				zap.String("adapter_id", adapterID),
				zap.String("channel", channelKind),
				zap.String("agent_id", agentID),
				zap.String("tier", t.String()),
				zap.String("decision", "allow_with_consent"),
				zap.String("hint", "this binding can spawn processes / write files / install software via the agent's tools"),
			)
			return true
		}
		log.Warn("channel binding: PRIVILEGED agent BLOCKED on non-web channel",
			zap.String("adapter_id", adapterID),
			zap.String("channel", channelKind),
			zap.String("agent_id", agentID),
			zap.String("tier", t.String()),
			zap.String("decision", "block"),
			zap.String("hint", "set accept_privileged_exposure: true on this binding to override (acknowledges shell/write/install exposure to channel users)"),
		)
		return false

	default:
		log.Warn("channel binding: unknown tier; refusing",
			zap.String("adapter_id", adapterID),
			zap.String("channel", channelKind),
			zap.String("agent_id", agentID),
			zap.String("decision", "block"),
		)
		return false
	}
}


// sanitizeID replaces characters that are not safe for adapter IDs or log
// fields with hyphens. Keeps letters, digits, hyphens, and underscores.
func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// firstOr returns the first element of list, or fallback when empty.
func firstOr(list []string, fallback string) string {
	if len(list) > 0 {
		return list[0]
	}
	return fallback
}

// concise trims an error to a short, user-safe one-liner for chat replies.
func concise(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 140 {
		s = s[:140] + "…"
	}
	return s
}
