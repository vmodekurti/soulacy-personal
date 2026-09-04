package sqlitevec

import (
	"fmt"

	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/sdk/registry"
	sdkvector "github.com/soulacy/soulacy/sdk/vector"
)

// Registry self-registration (Story E10).
//
// Host-internal contract: the sqlite-vec backend wraps an already-open
// *memory.VectorStore (the engine also uses the store directly), so the
// host passes it under the "store" key. Factory config maps are schemaless
// by design; live-object keys like this are documented per driver.
func init() {
	registry.MustRegisterVector("sqlite-vec", func(cfg map[string]any) (sdkvector.Backend, error) {
		vs, ok := cfg["store"].(*memory.VectorStore)
		if !ok || vs == nil {
			return nil, fmt.Errorf("sqlite-vec: config key %q must carry a *memory.VectorStore", "store")
		}
		return New(vs), nil
	})
}
