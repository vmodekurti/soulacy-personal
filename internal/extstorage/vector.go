package extstorage

import (
	"context"
	"fmt"
	"time"

	sdkext "github.com/soulacy/soulacy/sdk/extstorage"
	"github.com/soulacy/soulacy/sdk/memory"
	"github.com/soulacy/soulacy/sdk/vector"
)

// VectorBackend adapts a negotiated storage sidecar to vector.Backend.
type VectorBackend struct {
	c *Client
}

// NewVectorBackend spawns + negotiates a sidecar and verifies it
// advertises the "vector" capability.
func NewVectorBackend(ctx context.Context, cfg ClientConfig) (*VectorBackend, error) {
	c := NewClient(cfg)
	if err := c.Start(ctx); err != nil {
		return nil, err
	}
	if !hasCapability(c.Negotiated().Capabilities, "vector") {
		_ = c.Close()
		return nil, fmt.Errorf("extstorage: %s does not advertise the vector capability (got %v)",
			cfg.Name, c.Negotiated().Capabilities)
	}
	return &VectorBackend{c: c}, nil
}

// Client exposes the underlying session (shared dir, Done) to the host.
func (b *VectorBackend) Client() *Client { return b.c }

// Write implements vector.Backend.
func (b *VectorBackend) Write(ctx context.Context, entry memory.Entry) error {
	params := sdkext.VectorWriteParams{
		ID:        entry.ID,
		AgentID:   entry.AgentID,
		SessionID: entry.SessionID,
		Scope:     string(entry.Scope),
		Timestamp: entry.CreatedAt.Unix(),
	}

	if len(entry.Content) >= 1024 {
		relPath, err := b.c.WriteScratchFile("vector", entry.Content)
		if err != nil {
			return err
		}
		params.ContentFile = relPath
	} else {
		params.Content = entry.Content
	}

	var res sdkext.VectorWriteResult
	return b.c.Call(ctx, sdkext.MethodVectorWrite, params, &res)
}

// Search implements vector.Backend.
func (b *VectorBackend) Search(ctx context.Context, agentID, query string, topK int) ([]vector.Result, error) {
	var res sdkext.VectorSearchResult
	err := b.c.Call(ctx, sdkext.MethodVectorSearch, sdkext.VectorSearchParams{
		AgentID: agentID, Query: query, TopK: topK,
	}, &res)
	if err != nil {
		return nil, err
	}
	out := make([]vector.Result, 0, len(res.Results))
	for _, h := range res.Results {
		content := h.Content
		if h.ContentFile != "" {
			var err error
			content, err = b.c.ReadScratchFile(h.ContentFile)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, vector.Result{
			Entry: memory.Entry{
				ID:        h.ID,
				AgentID:   h.AgentID,
				SessionID: h.SessionID,
				Scope:     memory.Scope(h.Scope),
				Content:   content,
				CreatedAt: time.Unix(h.Timestamp, 0),
			},
			Distance: h.Distance,
		})
	}
	return out, nil
}

// Close implements vector.Backend.
func (b *VectorBackend) Close() error { return b.c.Close() }

func hasCapability(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}
