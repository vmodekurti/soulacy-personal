package extstorage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/wsroot"
	sdkext "github.com/soulacy/soulacy/sdk/extstorage"
	"github.com/soulacy/soulacy/sdk/memory"
	"github.com/soulacy/soulacy/sdk/vector"
)

// VectorBackend adapts a negotiated storage sidecar to vector.Backend.
type VectorBackend struct {
	c              *Client
	workspaceAware bool
}

var _ vector.WorkspaceBackend = (*VectorBackend)(nil)

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
	return &VectorBackend{c: c, workspaceAware: hasCapability(c.Negotiated().Capabilities, "vector.workspace")}, nil
}

// Client exposes the underlying session (shared dir, Done) to the host.
func (b *VectorBackend) Client() *Client { return b.c }

// Write implements vector.Backend.
func (b *VectorBackend) Write(ctx context.Context, entry memory.Entry) error {
	workspaceID, err := b.requireVectorWorkspace(entry.WorkspaceID)
	if err != nil {
		return err
	}
	params := sdkext.VectorWriteParams{
		WorkspaceID: workspaceID,
		ID:          entry.ID,
		AgentID:     entry.AgentID,
		SessionID:   entry.SessionID,
		Scope:       string(entry.Scope),
		Timestamp:   entry.CreatedAt.Unix(),
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
func (b *VectorBackend) Search(ctx context.Context, workspaceID, agentID, query string, topK int) ([]vector.Result, error) {
	workspaceID, err := b.requireVectorWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	var res sdkext.VectorSearchResult
	err = b.c.Call(ctx, sdkext.MethodVectorSearch, sdkext.VectorSearchParams{
		WorkspaceID: workspaceID, AgentID: agentID, Query: query, TopK: topK,
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
				WorkspaceID: workspaceID,
				ID:          h.ID,
				AgentID:     h.AgentID,
				SessionID:   h.SessionID,
				Scope:       memory.Scope(h.Scope),
				Content:     content,
				CreatedAt:   time.Unix(h.Timestamp, 0),
			},
			Distance: h.Distance,
		})
	}
	return out, nil
}

func (b *VectorBackend) SearchInWorkspace(ctx context.Context, workspaceID, agentID, query string, topK int) ([]vector.Result, error) {
	return b.Search(ctx, workspaceID, agentID, query, topK)
}

func (b *VectorBackend) requireVectorWorkspace(workspaceID string) (string, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return "", fmt.Errorf("extstorage: vector workspace is required")
	}
	workspaceID = wsroot.Normalize(workspaceID)
	if workspaceID != wsroot.PersonalWorkspaceID && !b.workspaceAware {
		return "", fmt.Errorf("extstorage: sidecar does not advertise vector.workspace; refusing workspace %q", workspaceID)
	}
	return workspaceID, nil
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
