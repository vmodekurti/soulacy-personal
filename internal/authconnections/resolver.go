package authconnections

import (
	"context"
	"errors"
	"fmt"
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
}

func NewResolver(store *Store, vault credentials.Vault) *Resolver {
	return &Resolver{store: store, vault: vault}
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
	key := "browser_storage_state"
	if connection.Kind == KindOAuth {
		key = "oauth_refresh_token"
	}
	secret, err := r.vault.ReadBlob(ctx, workspaceID, vaultNamespace(connection.ID), key)
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
