package authconnections

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/soulacy/soulacy/internal/credentials"
)

var (
	ErrNotGranted = errors.New("authenticated connection is not granted to this agent")
	ErrWrongOwner = errors.New("private authenticated connection belongs to another user")
	ErrNotReady   = errors.New("authenticated connection requires sign-in")
)

// Lease is handed only to a trusted execution adapter. State must never be
// placed in a prompt, tool result, action log, or client response.
type Lease struct {
	ConnectionID    string
	Kind            string
	AllowedDomains  []string
	BrowserState    []byte
	OAuthRefreshKey []byte
}

type Resolver struct {
	store *Store
	vault credentials.Vault

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewResolver(store *Store, vault credentials.Vault) *Resolver {
	return &Resolver{store: store, vault: vault, locks: map[string]*sync.Mutex{}}
}

// browserStateKey is the vault key the capture endpoint writes browser sessions to.
const browserStateKey = "browser_storage_state"

// maxBrowserStateBytes mirrors the 1 MiB limit the capture endpoint enforces.
const maxBrowserStateBytes = 1 << 20

func (r *Resolver) connectionLock(id string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locks == nil {
		r.locks = map[string]*sync.Mutex{}
	}
	l, ok := r.locks[id]
	if !ok {
		l = &sync.Mutex{}
		r.locks[id] = l
	}
	return l
}

// UpdateBrowserState lets a trusted adapter fold cookies a website renewed back
// into the encrypted session, so sessions that renew on activity stay alive. The
// read-modify-write is serialized per connection so concurrent fetches cannot
// overwrite each other's update. update receives the current state and returns
// the new state and whether it changed; unchanged state is not rewritten. The
// connection's status and approved domains are untouched.
func (r *Resolver) UpdateBrowserState(ctx context.Context, workspaceID, connectionID string, update func(current []byte) ([]byte, bool, error)) error {
	if r == nil || r.store == nil || r.vault == nil {
		return ErrNotReady
	}
	lock := r.connectionLock(connectionID)
	lock.Lock()
	defer lock.Unlock()
	connection, err := r.store.Get(ctx, workspaceID, connectionID)
	if err != nil {
		return err
	}
	if connection.Kind != KindBrowser || !connection.HasSecret || connection.Status != StatusReady {
		return ErrNotReady
	}
	current, err := r.vault.ReadBlob(ctx, vaultNamespace(connectionID), browserStateKey)
	if err != nil {
		return fmt.Errorf("authenticated connection secret: %w", err)
	}
	next, changed, err := update(current)
	if err != nil || !changed {
		return err
	}
	if len(next) == 0 || len(next) > maxBrowserStateBytes {
		return fmt.Errorf("updated session state has an invalid size")
	}
	return r.vault.WriteBlob(ctx, vaultNamespace(connectionID), browserStateKey, next)
}

// Describe returns only secret-free metadata for declared, granted
// connections. It lets the model choose the right connection without ever
// placing cookies or tokens in its context.
func (r *Resolver) Describe(ctx context.Context, workspaceID, subject, agentID string, connectionIDs []string) []Connection {
	if r == nil || r.store == nil {
		return nil
	}
	out := make([]Connection, 0, len(connectionIDs))
	now := time.Now()
	for _, connectionID := range connectionIDs {
		connection, err := r.store.Get(ctx, workspaceID, connectionID)
		if err != nil ||
			(connection.Scope == ScopeUser && connection.OwnerSubject != subject) ||
			!contains(connection.AgentIDs, agentID) ||
			connection.Status != StatusReady ||
			!connection.HasSecret ||
			(connection.ExpiresAt != nil && !connection.ExpiresAt.After(now)) {
			continue
		}
		out = append(out, connection)
	}
	return out
}

// MarkNeedsAuthentication records an upstream 401/403 so every surface can
// offer a reconnect instead of repeatedly running a known-expired session.
func (r *Resolver) MarkNeedsAuthentication(ctx context.Context, workspaceID, connectionID string) {
	if r == nil || r.store == nil {
		return
	}
	_ = r.store.SetStatus(ctx, workspaceID, connectionID, StatusExpired)
}

// Resolve verifies tenant, owner, status, expiry, and explicit agent grant
// before decrypting. A missing grant fails closed even for workspace-scoped
// connections: installing a shared login does not make every agent trusted.
func (r *Resolver) Resolve(ctx context.Context, workspaceID, subject, agentID, connectionID string) (Lease, error) {
	if r == nil || r.store == nil || r.vault == nil {
		return Lease{}, ErrNotReady
	}
	connection, err := r.store.Get(ctx, workspaceID, connectionID)
	if err != nil {
		return Lease{}, err
	}
	if connection.Scope == ScopeUser && connection.OwnerSubject != subject {
		return Lease{}, ErrWrongOwner
	}
	if connection.Status != StatusReady || !connection.HasSecret {
		return Lease{}, ErrNotReady
	}
	if connection.ExpiresAt != nil && !connection.ExpiresAt.After(time.Now()) {
		_ = r.store.SetStatus(ctx, workspaceID, connectionID, StatusExpired)
		return Lease{}, ErrNotReady
	}
	granted := false
	for _, id := range connection.AgentIDs {
		if id == agentID {
			granted = true
			break
		}
	}
	if !granted {
		return Lease{}, ErrNotGranted
	}
	lease := Lease{ConnectionID: connection.ID, Kind: connection.Kind, AllowedDomains: append([]string(nil), connection.AllowedDomains...)}
	key := browserStateKey
	if connection.Kind == KindOAuth {
		key = "oauth_refresh_token"
	}
	secret, err := r.vault.ReadBlob(ctx, vaultNamespace(connection.ID), key)
	if err != nil {
		return Lease{}, fmt.Errorf("authenticated connection secret: %w", err)
	}
	if connection.Kind == KindOAuth {
		lease.OAuthRefreshKey = secret
	} else {
		lease.BrowserState = secret
	}
	return lease, nil
}

func vaultNamespace(id string) string { return "authenticated_connection_" + id }

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
