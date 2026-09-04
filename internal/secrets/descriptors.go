package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/soulacy/soulacy/internal/config"
)

// Errors returned by Manager mutating operations.
var (
	ErrNoVault   = errors.New("secrets: credential vault not available")
	ErrEmptyName = errors.New("secrets: name is required")
)

// channelTokenKeys are the config keys treated as secrets inside a channel's
// settings map (channels.<id>.<key>).
var channelTokenKeys = []string{
	"bot_token", "app_token", "token", "signing_secret",
	"webhook_secret", "api_key", "password",
}

// structuredDescriptors enumerates the known secret slots derived from cfg:
// LLM provider api_keys, channel tokens, and the gateway server api_key.
func structuredDescriptors(cfg *config.Config) []Descriptor {
	var out []Descriptor
	if cfg == nil {
		return out
	}

	// LLM providers.
	ids := make([]string, 0, len(cfg.LLM.Providers))
	for id := range cfg.LLM.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out = append(out, Descriptor{
			Name:        llmKey(id),
			Category:    CategoryLLM,
			EnvVar:      strings.ToUpper(id) + "_API_KEY",
			Description: fmt.Sprintf("%s API key", id),
		})
	}

	// Channels.
	cids := make([]string, 0, len(cfg.Channels))
	for id := range cfg.Channels {
		cids = append(cids, id)
	}
	sort.Strings(cids)
	for _, id := range cids {
		settings := cfg.Channels[id]
		for _, k := range channelTokenKeys {
			if _, ok := settings[k]; !ok {
				continue
			}
			out = append(out, Descriptor{
				Name:        channelKey(id, k),
				Category:    CategoryChannel,
				Description: fmt.Sprintf("%s %s", id, strings.ReplaceAll(k, "_", " ")),
			})
		}
	}

	// NOTE: the gateway's own auth key (server.api_key) is deliberately NOT
	// vault-managed. It is the bootstrap credential the CLI must read from
	// config/env *before* the vault is reachable; migrating it into the vault
	// would lock the CLI out. It stays in config.yaml / SOULACY_SERVER_API_KEY.
	return out
}

func llmKey(id string) string          { return "llm.providers." + id + ".api_key" }
func channelKey(id, key string) string { return "channels." + id + "." + key }

// Resolve returns the effective value for a secret, honoring precedence
// vault → environment(envVar) → fallback (typically the config value).
func (m *Manager) Resolve(ctx context.Context, name, envVar, fallback string) string {
	if v, ok := m.Get(ctx, name); ok && v != "" {
		return v
	}
	if envVar != "" {
		if v := os.Getenv(envVar); v != "" {
			return v
		}
	}
	return fallback
}

// Overlay writes any vault-stored structured secrets onto the in-memory config
// (vault wins). Called at startup so downstream consumers (LLM router, channel
// adapters) see vault values without each needing vault awareness. Returns the
// number of fields overlaid.
func (m *Manager) Overlay(ctx context.Context, cfg *config.Config) int {
	if !m.Enabled() || cfg == nil {
		return 0
	}
	n := 0

	// LLM provider api_keys.
	for id, pc := range cfg.LLM.Providers {
		if v, ok := m.Get(ctx, llmKey(id)); ok && v != "" {
			pc.APIKey = v
			cfg.LLM.Providers[id] = pc // map value is a struct; reassign
			n++
		}
	}

	// Channel token fields.
	for id, settings := range cfg.Channels {
		if settings == nil {
			continue
		}
		for _, k := range channelTokenKeys {
			if v, ok := m.Get(ctx, channelKey(id, k)); ok && v != "" {
				settings[k] = v
				n++
			}
		}
	}

	// server.api_key is intentionally NOT overlaid — see structuredDescriptors.
	return n
}

// secretValuesInConfig collects the non-empty plaintext secret values currently
// present in cfg, keyed by canonical secret name. Used by migration.
func secretValuesInConfig(cfg *config.Config) map[string]string {
	vals := map[string]string{}
	if cfg == nil {
		return vals
	}
	for id, pc := range cfg.LLM.Providers {
		if strings.TrimSpace(pc.APIKey) != "" {
			vals[llmKey(id)] = pc.APIKey
		}
	}
	for id, settings := range cfg.Channels {
		for _, k := range channelTokenKeys {
			if raw, ok := settings[k]; ok {
				if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
					vals[channelKey(id, k)] = s
				}
			}
		}
	}
	// server.api_key is intentionally excluded — it stays in config/env as the
	// CLI/gateway bootstrap credential (see structuredDescriptors).
	return vals
}
