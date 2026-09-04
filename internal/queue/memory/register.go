package memory

import (
	sdkqueue "github.com/soulacy/soulacy/sdk/queue"
	"github.com/soulacy/soulacy/sdk/registry"
)

// Registry self-registration (Story E10). The in-process queue takes no
// configuration.
func init() {
	registry.MustRegisterQueue("memory", func(cfg map[string]any) (sdkqueue.Backend, error) {
		return New(), nil
	})
}
