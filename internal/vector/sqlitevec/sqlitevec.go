// Package sqlitevec wraps *memory.VectorStore to satisfy vector.Backend.
//
// No new dependencies: it reuses the sqlite-vec virtual table already embedded
// in the memory package. This adapter exists purely to provide the
// compile-time interface guarantee and adapt SearchResult → vector.Result.
//
// Pre-filtering rationale:
//
// The adapter calls SearchFiltered (with the agentID) rather than the legacy
// Search (which fetches all agents and post-filters in Go). A Go-level post-
// filter over-fetches globally and returns zero results for agents whose
// memories are sparse compared to others that dominate the vector index.
// Pushing the agent_id constraint into SQL lets sqlite-vec count only that
// agent's rows against the K budget, eliminating the starvation problem at
// the cost of a tiny subquery that is negligible next to embedding I/O.
package sqlitevec

import (
	"context"
	"fmt"

	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/vector"
)

// compile-time interface check
var _ vector.Backend = (*Store)(nil)

// Store wraps *memory.VectorStore.
type Store struct {
	vs *memory.VectorStore
}

// New wraps an existing *memory.VectorStore in the vector.Backend interface.
func New(vs *memory.VectorStore) *Store {
	return &Store{vs: vs}
}

// Write embeds entry.Content and inserts it into the sqlite-vec index.
func (s *Store) Write(ctx context.Context, entry memory.Entry) error {
	return s.vs.Write(ctx, entry)
}

// Search embeds query and returns the topK most similar entries.
// agentID is used as a SQL pre-filter via SearchFiltered so that KNN is
// scoped to only that agent's rows, eliminating the over-fetch heuristic
// that failed when other agents dominated the vector space.
func (s *Store) Search(ctx context.Context, agentID, query string, topK int) ([]vector.Result, error) {
	raw, err := s.vs.SearchFiltered(ctx, query, topK, agentID)
	if err != nil {
		return nil, fmt.Errorf("sqlitevec: search: %w", err)
	}

	results := make([]vector.Result, 0, len(raw))
	for _, r := range raw {
		results = append(results, vector.Result{
			Entry:    r.Entry,
			Distance: r.Distance,
		})
	}
	return results, nil
}

// Close is a no-op; the underlying *sql.DB is owned by memory.SQLiteArchive
// and closed via MemoryBackend.Close().
func (s *Store) Close() error { return nil }
