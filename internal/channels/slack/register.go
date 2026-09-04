package slack

import (
	"fmt"

	"github.com/soulacy/soulacy/internal/cfgmap"
	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/sdk/channel"
	"github.com/soulacy/soulacy/sdk/registry"
)

// Registry self-registration (Story E10).
//
// Config keys: id (default "slack"), bot_token + app_token (both required),
// agent_id, trigger_phrase, ignore_groups (default true), allowed_user_ids,
// allowed_chat_ids.
func init() {
	registry.MustRegisterChannel("slack", func(cfg map[string]any) (channel.Adapter, error) {
		botToken := cfgmap.Str(cfg, "bot_token", "")
		appToken := cfgmap.Str(cfg, "app_token", "")
		if botToken == "" || appToken == "" {
			return nil, fmt.Errorf("slack: config keys %q and %q are required", "bot_token", "app_token")
		}
		return NewWithIDAndActivation(
			cfgmap.Str(cfg, "id", "slack"),
			botToken,
			appToken,
			cfgmap.Str(cfg, "agent_id", ""),
			channels.ActivationFromConfig(cfg, true),
		), nil
	})
}
