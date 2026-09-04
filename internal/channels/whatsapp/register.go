package whatsapp

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/cfgmap"
	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/sdk/channel"
	"github.com/soulacy/soulacy/sdk/registry"
)

// Registry self-registration (Story E10).
//
// Config keys: phone_number_id, access_token, verify_token (all required),
// app_secret, agent_id, trigger_phrase, ignore_groups (default false —
// webhook-driven channel), allowed_user_ids, allowed_chat_ids.
//
// Host-internal key: "logger" may carry a *zap.Logger; absent, the adapter
// logs to a no-op logger. Factory config maps are schemaless by design; this
// is the documented escape hatch for adapters that take a logger.
func init() {
	registry.MustRegisterChannel("whatsapp", func(cfg map[string]any) (channel.Adapter, error) {
		phoneNumberID := cfgmap.Str(cfg, "phone_number_id", "")
		accessToken := cfgmap.Str(cfg, "access_token", "")
		verifyToken := cfgmap.Str(cfg, "verify_token", "")
		if phoneNumberID == "" || accessToken == "" || verifyToken == "" {
			return nil, fmt.Errorf("whatsapp: config keys %q, %q, %q are all required",
				"phone_number_id", "access_token", "verify_token")
		}
		log, _ := cfg["logger"].(*zap.Logger)
		if log == nil {
			log = zap.NewNop()
		}
		return NewWithActivation(
			phoneNumberID,
			accessToken,
			verifyToken,
			cfgmap.Str(cfg, "app_secret", ""),
			cfgmap.Str(cfg, "agent_id", ""),
			channels.ActivationFromConfig(cfg, false),
			log,
		), nil
	})
}
