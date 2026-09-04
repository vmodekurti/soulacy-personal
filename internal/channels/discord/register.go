package discord

import (
	"fmt"

	"github.com/soulacy/soulacy/internal/cfgmap"
	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/sdk/channel"
	"github.com/soulacy/soulacy/sdk/registry"
)

// Registry self-registration (Story E10).
//
// Config keys: id (default "discord"), token (required), agent_id, guild_id,
// trigger_phrase, ignore_groups (default true), allowed_user_ids,
// allowed_chat_ids.
func init() {
	registry.MustRegisterChannel("discord", func(cfg map[string]any) (channel.Adapter, error) {
		token := cfgmap.Str(cfg, "token", "")
		if token == "" {
			return nil, fmt.Errorf("discord: config key %q is required", "token")
		}
		return NewWithIDAndActivation(
			cfgmap.Str(cfg, "id", "discord"),
			token,
			cfgmap.Str(cfg, "agent_id", ""),
			cfgmap.Str(cfg, "guild_id", ""),
			channels.ActivationFromConfig(cfg, true),
		), nil
	})
}
