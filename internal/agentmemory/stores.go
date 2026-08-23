package agentmemory

// stores.go — one CompositeStore per workspace.
//
// Brain memory is keyed by agent ID: `<baseDir>/<agentID>/episodic.jsonl`,
// `<baseDir>/<agentID>/procedural.md`, and rows keyed `(agent_id, version)` in
// `<baseDir>/rulebook.db`. Agent IDs are unique within a workspace, not across
// the deployment, so a single shared baseDir does not merely risk a leak — two
// tenants with an agent called "researcher" would be appending to one episodic
// log and overwriting one procedural rulebook.
//
// The lock table is the sharpest edge. `rulebook_locks` is keyed on agent_id
// alone, and a lock is not a record but a *control*: it refuses every write,
// automatic and manual. Under one shared database, freezing an agent in one
// workspace freezes the identically-named agent in every other workspace, and
// an unlock by a stranger silently thaws yours. The key of a lock is also its
// mutual-exclusion domain, so getting the key wrong is not an information
// problem, it is a control-plane one.
//
// Scoping is therefore by root directory rather than by a column: each
// workspace gets its own files *and its own rulebook.db*, which puts the lock
// domain inside the tenant boundary by construction. Personal resolves to the
// original baseDir, so a single-user installation's files do not move
// (product invariant 7).

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/soulacy/soulacy/internal/workspacepurge"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// SemanticStores resolves one workspace's semantic backend. It is a function
// rather than a single store because the semantic tier is a shared vector
// database that scopes internally: the resolver binds the workspace once, at
// the point where the composite store for that workspace is built.
type SemanticStores func(workspaceID string) SemanticStore

// Stores is the per-workspace registry of composite brain-memory stores.
type Stores struct {
	base     string
	layout   wsroot.Layout
	mu       sync.Mutex
	semantic SemanticStores
	stores   map[string]*CompositeStore
}

func (s *Stores) SetWorkspaceLayoutRoot(root string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.layout = wsroot.NewLayout(root)
}

// NewStores takes the directory a single-tenant installation already uses.
// Other workspaces are namespaced beneath it by wsroot.Dir.
func NewStores(base string) *Stores {
	if strings.TrimSpace(base) == "" {
		return nil
	}
	return &Stores{base: base, stores: map[string]*CompositeStore{}}
}

// SetSemanticStores attaches the production semantic backend resolver. It is
// intended for startup composition, before the gateway accepts traffic, and it
// also updates any store already created — brain memory is wired before the
// vector store, so the personal store usually exists by the time this is
// called.
func (s *Stores) SetSemanticStores(resolve SemanticStores) {
	if s == nil || resolve == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.semantic = resolve
	for workspaceID, store := range s.stores {
		if store == nil {
			continue
		}
		store.SetSemanticStore(resolve(workspaceID))
	}
}

// For returns one workspace's composite store, creating and caching it on
// first use.
//
// A workspace whose directory cannot be created gets nil, and callers treat
// nil exactly as they treat brain memory being disabled: they skip. That is
// the important half — the alternative, falling back to the shared base
// directory, would write one tenant's episodic history into another's.
func (s *Stores) For(workspaceID string) *CompositeStore {
	if s == nil {
		return nil
	}
	workspaceID = wsroot.Normalize(workspaceID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.stores[workspaceID]; ok {
		return existing
	}
	dir := s.layout.Dir(s.base, workspaceID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.stores[workspaceID] = nil
		return nil
	}
	var semantic SemanticStore
	if s.semantic != nil {
		semantic = s.semantic(workspaceID)
	}
	store := NewCompositeStore(dir, semantic)
	s.stores[workspaceID] = store
	return store
}

// Dir reports one workspace's brain-memory directory. Used by the gateway to
// name a file in an operator-facing response without exposing the shared root.
func (s *Stores) Dir(workspaceID string) string {
	if s == nil {
		return ""
	}
	return s.layout.Dir(s.base, wsroot.Normalize(workspaceID))
}

// PurgeWorkspace closes the tenant's SQLite rulebook before removing its
// three-layer memory tree. Holding the registry lock prevents a concurrent
// request from recreating the store between close and deletion.
func (s *Stores) PurgeWorkspace(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
	if s == nil {
		return workspacepurge.Removed{}, nil
	}
	workspaceID = wsroot.Normalize(workspaceID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if store, ok := s.stores[workspaceID]; ok && store != nil {
		if err := store.Close(); err != nil {
			return workspacepurge.Removed{}, err
		}
	}
	var (
		removed workspacepurge.Removed
		err     error
	)
	if s.layout.Root() != "" {
		removed, err = workspacepurge.PurgeLayoutTree(ctx, s.layout, s.base, workspaceID)
	} else {
		removed, err = workspacepurge.PurgeTree(ctx, s.base, workspaceID)
	}
	if err == nil {
		delete(s.stores, workspaceID)
	}
	return removed, err
}

// Close releases every workspace's rulebook database.
func (s *Stores) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for _, store := range s.stores {
		if store == nil {
			continue
		}
		if err := store.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
