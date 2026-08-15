// Package vector defines the contract for semantic-search backends
// (sqlite-vec, Qdrant, or any out-of-tree implementation).
//
// Compatibility: Backend is FROZEN per SDK major version; see the SDK README.
package vector

import (
	"context"

	"github.com/soulacy/soulacy/sdk/memory"
)

// Result is one hit from a semantic search.
type Result struct {
	Entry    memory.Entry
	Distance float64 // cosine or L2 distance; lower = more similar
}

// Backend is the interface satisfied by every vector-store implementation.
type Backend interface {
	// Write embeds entry.Content and stores it in the vector index.
	Write(ctx context.Context, entry memory.Entry) error

	// Search embeds query and returns the topK most similar entries.
	// An empty agentID searches across all agents.
	Search(ctx context.Context, agentID, query string, topK int) ([]Result, error)

	// Close releases all held resources (connections, goroutines, etc.).
	Close() error
}

// WorkspaceBackend is the tenant-aware search surface for a vector store.
//
// It is a separate interface rather than new methods on Backend because
// Backend is frozen for this SDK major version. A backend that does not
// implement this one keeps working exactly as before; a caller that needs
// tenant isolation type-asserts for it and refuses to serve multi-tenant
// traffic when the assertion fails, rather than silently searching across
// tenants.
//
// Write needs no counterpart: memory.Entry carries WorkspaceID, so a write is
// already tenant-aware for any backend that persists the field.
//
// Vector search is the sharpest case for this. A post-filter would not merely
// return another tenant's rows to be discarded — it would spend the topK
// budget on them, so a tenant sharing an index with a busier one gets few
// results or none, and the neighbour's vectors are read out of the store in
// order to decide that. The workspace has to reach the engine's pre-filter.
type WorkspaceBackend interface {
	Backend

	// SearchInWorkspace embeds query and returns the topK most similar entries
	// within one workspace. An empty agentID searches every agent in that
	// workspace, and only that workspace.
	SearchInWorkspace(ctx context.Context, workspaceID, agentID, query string, topK int) ([]Result, error)
}
