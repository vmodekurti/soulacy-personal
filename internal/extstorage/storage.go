package extstorage

import (
	"context"
	"fmt"
	"time"

	sdkext "github.com/soulacy/soulacy/sdk/extstorage"
	"github.com/soulacy/soulacy/sdk/memory"
	"github.com/soulacy/soulacy/sdk/storage"
)

// storageCallTimeout bounds each archive call — sdk/storage.MemoryBackend
// methods carry no context, so the adapter supplies one.
const storageCallTimeout = 30 * time.Second

// StorageBackend adapts a negotiated sidecar to sdk/storage.MemoryBackend
// (the long-term memory archive). The "storage" capability must be
// advertised in negotiate.
type StorageBackend struct {
	c *Client
}

var _ storage.ContextMemoryBackend = (*StorageBackend)(nil)

// NewStorageBackend spawns + negotiates a sidecar and verifies it
// advertises the "storage" capability.
func NewStorageBackend(ctx context.Context, cfg ClientConfig) (*StorageBackend, error) {
	c := NewClient(cfg)
	if err := c.Start(ctx); err != nil {
		return nil, err
	}
	if !hasCapability(c.Negotiated().Capabilities, "storage") {
		_ = c.Close()
		return nil, fmt.Errorf("extstorage: %s does not advertise the storage capability (got %v)",
			cfg.Name, c.Negotiated().Capabilities)
	}
	return &StorageBackend{c: c}, nil
}

// Client exposes the underlying session to the host.
func (b *StorageBackend) Client() *Client { return b.c }

func (b *StorageBackend) call(ctx context.Context, method string, params, result any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, storageCallTimeout)
	defer cancel()
	return b.c.Call(ctx, method, params, result)
}

// Archive implements storage.MemoryBackend.
func (b *StorageBackend) Archive(entry memory.Entry) error {
	return b.ArchiveContext(context.Background(), entry)
}

// ArchiveContext persists an entry while honoring caller cancellation.
func (b *StorageBackend) ArchiveContext(ctx context.Context, entry memory.Entry) error {
	params := sdkext.StorageArchiveParams{
		Entry: entry,
	}

	if len(entry.Content) >= 1024 {
		relPath, err := b.c.WriteScratchFile("storage", entry.Content)
		if err != nil {
			return err
		}
		params.ContentFile = relPath
		// Clear content to prevent sending it twice
		params.Entry.Content = ""
	}

	var res sdkext.StorageArchiveResult
	return b.call(ctx, sdkext.MethodStorageArchive, params, &res)
}

// Search implements storage.MemoryBackend.
func (b *StorageBackend) Search(agentID, query string, limit int) ([]memory.Entry, error) {
	return b.SearchContext(context.Background(), agentID, query, limit)
}

// SearchContext searches while honoring caller cancellation.
func (b *StorageBackend) SearchContext(ctx context.Context, agentID, query string, limit int) ([]memory.Entry, error) {
	var res sdkext.StorageSearchResult
	err := b.call(ctx, sdkext.MethodStorageSearch, sdkext.StorageSearchParams{
		AgentID: agentID, Query: query, Limit: limit,
	}, &res)
	return res.Entries, err
}

// ReadByScope implements storage.MemoryBackend.
func (b *StorageBackend) ReadByScope(agentID, sessionID string, scope memory.Scope, limit int) ([]memory.Entry, error) {
	return b.ReadByScopeContext(context.Background(), agentID, sessionID, scope, limit)
}

// ReadByScopeContext reads scoped entries while honoring caller cancellation.
func (b *StorageBackend) ReadByScopeContext(ctx context.Context, agentID, sessionID string, scope memory.Scope, limit int) ([]memory.Entry, error) {
	var res sdkext.StorageReadByScopeResult
	err := b.call(ctx, sdkext.MethodStorageReadByScope, sdkext.StorageReadByScopeParams{
		AgentID: agentID, SessionID: sessionID, Scope: scope, Limit: limit,
	}, &res)
	return res.Entries, err
}

// ReadGlobal implements storage.MemoryBackend.
func (b *StorageBackend) ReadGlobal(agentID string, limit int) ([]memory.Entry, error) {
	return b.ReadGlobalContext(context.Background(), agentID, limit)
}

// ReadGlobalContext reads recent entries while honoring caller cancellation.
func (b *StorageBackend) ReadGlobalContext(ctx context.Context, agentID string, limit int) ([]memory.Entry, error) {
	var res sdkext.StorageReadGlobalResult
	err := b.call(ctx, sdkext.MethodStorageReadGlobal, sdkext.StorageReadGlobalParams{
		AgentID: agentID, Limit: limit,
	}, &res)
	return res.Entries, err
}

// Prune implements storage.MemoryBackend.
func (b *StorageBackend) Prune(agentID string, before time.Time) (int64, error) {
	return b.PruneContext(context.Background(), agentID, before)
}

// PruneContext deletes old entries while honoring caller cancellation.
func (b *StorageBackend) PruneContext(ctx context.Context, agentID string, before time.Time) (int64, error) {
	var res sdkext.StoragePruneResult
	err := b.call(ctx, sdkext.MethodStoragePrune, sdkext.StoragePruneParams{
		AgentID: agentID, Before: before,
	}, &res)
	return res.RowsDeleted, err
}

// Close implements storage.MemoryBackend.
func (b *StorageBackend) Close() error { return b.c.Close() }
