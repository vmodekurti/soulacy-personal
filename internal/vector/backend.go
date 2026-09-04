// Package vector defines the provider-agnostic interface for Soulacy's
// semantic (vector) memory tier.
//
// Two implementations ship out of the box:
//
//	internal/vector/sqlitevec — sqlite-vec virtual table (zero-dependency, default)
//	internal/vector/qdrant    — Qdrant via REST API (production scale-out)
//
// Active backend is selected at startup from config.yaml:
//
//	memory:
//	  vector_db: sqlite-vec   # or "qdrant"
//	  vector_url: ""          # Qdrant URL when vector_db == "qdrant"
//
// Engine and memory subsystem code imports only this package; they never
// reference a concrete implementation directly.
package vector

import sdkvector "github.com/soulacy/soulacy/sdk/vector"

// Canonical vector contract types live in the versioned SDK (Story E9).
type (
	// Result is one hit from a semantic search.
	Result = sdkvector.Result
	// Backend is the interface satisfied by every vector-store implementation.
	Backend = sdkvector.Backend
)
