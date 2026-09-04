package qdrant

import (
	"context"
	"fmt"
	"time"

	"github.com/soulacy/soulacy/internal/cfgmap"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/sdk/registry"
	sdkvector "github.com/soulacy/soulacy/sdk/vector"
)

// Registry self-registration (Story E10).
//
// Config keys: base_url (default http://localhost:6333), collection
// (default "soulacy_memory"), api_key, dims (default 768).
// Host-internal key: "embedder" carries the memory.Embedder used to vectorise
// text. Collection creation is verified within a 15s timeout, matching the
// previous hardcoded wiring.
func init() {
	registry.MustRegisterVector("qdrant", func(cfg map[string]any) (sdkvector.Backend, error) {
		embedder, _ := cfg["embedder"].(memory.Embedder)
		if embedder == nil {
			return nil, fmt.Errorf("qdrant: config key %q must carry a memory.Embedder", "embedder")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return New(ctx, Config{
			BaseURL:    cfgmap.Str(cfg, "base_url", "http://localhost:6333"),
			Collection: cfgmap.Str(cfg, "collection", "soulacy_memory"),
			APIKey:     cfgmap.Str(cfg, "api_key", ""),
			Dims:       cfgmap.Int(cfg, "dims", 768),
			Embedder:   embedder,
		})
	})
}
