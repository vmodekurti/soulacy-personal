package nats

import (
	"time"

	"github.com/soulacy/soulacy/internal/cfgmap"
	sdkqueue "github.com/soulacy/soulacy/sdk/queue"
	"github.com/soulacy/soulacy/sdk/registry"
)

// Registry self-registration (Story E10).
//
// Config keys: url, stream, subject_prefix, ack_wait (duration string,
// default 30s), max_deliver. Connection failures surface as factory errors.
func init() {
	registry.MustRegisterQueue("nats", func(cfg map[string]any) (sdkqueue.Backend, error) {
		ackWait, _ := time.ParseDuration(cfgmap.Str(cfg, "ack_wait", ""))
		if ackWait == 0 {
			ackWait = 30 * time.Second
		}
		return New(Config{
			URL:           cfgmap.Str(cfg, "url", ""),
			StreamName:    cfgmap.Str(cfg, "stream", ""),
			SubjectPrefix: cfgmap.Str(cfg, "subject_prefix", ""),
			AckWait:       ackWait,
			MaxDeliver:    cfgmap.Int(cfg, "max_deliver", 0),
		})
	})
}
