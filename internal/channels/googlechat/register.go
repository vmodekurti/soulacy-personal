package googlechat

import (
	"time"

	"github.com/soulacy/soulacy/internal/cfgmap"
	"github.com/soulacy/soulacy/sdk/channel"
	"github.com/soulacy/soulacy/sdk/registry"
)

// Registry self-registration for the outbound Google Chat channel.
//
// Config keys: id (default "google_chat"), webhook_url (required; url and
// default_output_to are accepted as compatibility aliases), prefix,
// timeout_seconds (default 10).
func init() {
	registry.MustRegisterChannel("google_chat", func(cfg map[string]any) (channel.Adapter, error) {
		endpoint := cfgmap.Str(cfg, "webhook_url", "")
		if endpoint == "" {
			endpoint = cfgmap.Str(cfg, "url", "")
		}
		if endpoint == "" {
			endpoint = cfgmap.Str(cfg, "default_output_to", "")
		}
		timeout := time.Duration(cfgmap.Int(cfg, "timeout_seconds", 10)) * time.Second
		return New(
			cfgmap.Str(cfg, "id", "google_chat"),
			endpoint,
			cfgmap.Str(cfg, "prefix", ""),
			timeout,
		)
	})
}
