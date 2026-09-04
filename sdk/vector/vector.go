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
