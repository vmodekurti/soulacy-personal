package email

import (
	"time"

	"github.com/soulacy/soulacy/internal/cfgmap"
	"github.com/soulacy/soulacy/sdk/channel"
	"github.com/soulacy/soulacy/sdk/registry"
)

// Registry self-registration for the outbound email (SMTP) channel.
//
// Config keys: id (default "email"), host (required), port (default 587),
// username, password, from (defaults to username), default_output_to, subject,
// tls (starttls|implicit|none — inferred from the port when unset),
// timeout_seconds (default 20).
func init() {
	registry.MustRegisterChannel("email", func(cfg map[string]any) (channel.Adapter, error) {
		timeout := time.Duration(cfgmap.Int(cfg, "timeout_seconds", 20)) * time.Second
		return New(
			cfgmap.Str(cfg, "id", "email"),
			cfgmap.Str(cfg, "host", ""),
			cfgmap.Int(cfg, "port", 587),
			cfgmap.Str(cfg, "username", ""),
			cfgmap.Str(cfg, "password", ""),
			cfgmap.Str(cfg, "from", ""),
			cfgmap.Str(cfg, "default_output_to", ""),
			cfgmap.Str(cfg, "subject", ""),
			cfgmap.Str(cfg, "tls", ""),
			timeout,
		)
	})
}
