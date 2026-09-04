package webhook

import (
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/cfgmap"
	"github.com/soulacy/soulacy/sdk/channel"
	"github.com/soulacy/soulacy/sdk/registry"
)

// Registry self-registration for the generic outbound webhook channel.
//
// Config keys: id (default "webhook"), url (required), method (default POST),
// headers (optional "Key: value" lines or map), template (optional plain-text
// body template), secret (optional HMAC signing secret), timeout_seconds
// (default 10). When `secret` is set, every request carries
// X-Soulacy-Timestamp + X-Soulacy-Signature so the receiver can verify it.
func init() {
	registry.MustRegisterChannel("webhook", func(cfg map[string]any) (channel.Adapter, error) {
		endpoint := cfgmap.Str(cfg, "url", "")
		if endpoint == "" {
			endpoint = cfgmap.Str(cfg, "default_output_to", "")
		}
		timeout := time.Duration(cfgmap.Int(cfg, "timeout_seconds", 10)) * time.Second
		return New(
			cfgmap.Str(cfg, "id", "webhook"),
			endpoint,
			cfgmap.Str(cfg, "method", "POST"),
			parseHeaders(cfg["headers"]),
			cfgmap.Str(cfg, "template", ""),
			cfgmap.Str(cfg, "secret", ""),
			timeout,
		)
	})
}

func parseHeaders(raw any) map[string]string {
	out := map[string]string{}
	switch v := raw.(type) {
	case map[string]any:
		for k, val := range v {
			k = strings.TrimSpace(k)
			if k != "" {
				out[k] = fmt.Sprint(val)
			}
		}
	case map[string]string:
		for k, val := range v {
			k = strings.TrimSpace(k)
			if k != "" {
				out[k] = val
			}
		}
	case string:
		for _, line := range strings.Split(v, "\n") {
			key, val, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			if key != "" {
				out[key] = strings.TrimSpace(val)
			}
		}
	}
	return out
}
