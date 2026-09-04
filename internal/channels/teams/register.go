package teams

import (
	"time"

	"github.com/soulacy/soulacy/internal/cfgmap"
	"github.com/soulacy/soulacy/sdk/channel"
	"github.com/soulacy/soulacy/sdk/registry"
)

// Registry self-registration for the outbound Microsoft Teams channel.
//
// Config keys: id (default "teams"), webhook_url (required; url and
// default_output_to are accepted as compatibility aliases), title,
// timeout_seconds (default 10).
func init() {
	registry.MustRegisterChannel("teams", func(cfg map[string]any) (channel.Adapter, error) {
		endpoint := cfgmap.Str(cfg, "webhook_url", "")
		if endpoint == "" {
			endpoint = cfgmap.Str(cfg, "url", "")
		}
		if endpoint == "" {
			endpoint = cfgmap.Str(cfg, "default_output_to", "")
		}
		timeout := time.Duration(cfgmap.Int(cfg, "timeout_seconds", 10)) * time.Second
		return New(
			cfgmap.Str(cfg, "id", "teams"),
			endpoint,
			cfgmap.Str(cfg, "title", ""),
			timeout,
		)
	})
}
