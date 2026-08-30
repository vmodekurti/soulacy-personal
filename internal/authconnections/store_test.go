package authconnections

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/credentials"
)

func TestVisibilityAndExplicitGrant(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "connections.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	private, err := store.Create(ctx, CreateInput{WorkspaceID: "ws", OwnerSubject: "alice", Scope: ScopeUser, Kind: KindBrowser, Name: "HBR", BaseURL: "https://hbr.org", AllowedDomains: []string{"hbr.org"}})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := store.Create(ctx, CreateInput{WorkspaceID: "ws", Scope: ScopeWorkspace, Kind: KindOAuth, Name: "Shared", BaseURL: "https://example.com", AllowedDomains: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := store.ListVisible(ctx, "ws", "alice")
	bob, _ := store.ListVisible(ctx, "ws", "bob")
	if len(alice) != 2 || len(bob) != 1 || bob[0].ID != shared.ID {
		t.Fatalf("visibility alice=%v bob=%v", alice, bob)
	}
	if err := store.ReplaceAgentGrants(ctx, "ws", private.ID, []string{"research", "research", ""}); err != nil {
		t.Fatal(err)
	}

	kms, err := credentials.NewLocalKMS()
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credentials.NewSQLiteVault(filepath.Join(t.TempDir(), "vault.db"), kms)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	state := []byte(`{"cookies":[{"name":"session","value":"secret","domain":"hbr.org"}],"origins":[]}`)
	if err := vault.WriteBlob(ctx, "ws", vaultNamespace(private.ID), "browser_storage_state", state); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSecret(ctx, "ws", private.ID, nil); err != nil {
		t.Fatal(err)
	}
	resolver := NewResolver(store, vault)
	if _, err := resolver.Resolve(ctx, "ws", "alice", "other", private.ID); !errors.Is(err, ErrNotGranted) {
		t.Fatalf("want not granted, got %v", err)
	}
	if _, err := resolver.Resolve(ctx, "ws", "bob", "research", private.ID); !errors.Is(err, ErrWrongOwner) {
		t.Fatalf("want wrong owner, got %v", err)
	}
	lease, err := resolver.Resolve(ctx, "ws", "alice", "research", private.ID)
	if err != nil || string(lease.BrowserState) != string(state) {
		t.Fatalf("lease=%+v err=%v", lease, err)
	}
}

func TestSyncAgentSelectionPreservesOtherAgents(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "connections.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.Create(ctx, CreateInput{WorkspaceID: "ws", Scope: ScopeWorkspace, Kind: KindBrowser, Name: "First"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(ctx, CreateInput{WorkspaceID: "ws", Scope: ScopeWorkspace, Kind: KindBrowser, Name: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceAgentGrants(ctx, "ws", first.ID, []string{"agent-a", "agent-b"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceAgentGrants(ctx, "ws", second.ID, []string{"agent-b"}); err != nil {
		t.Fatal(err)
	}

	if err := store.SyncAgentSelection(ctx, "ws", "agent-a", []string{first.ID, second.ID}, []string{second.ID}); err != nil {
		t.Fatal(err)
	}
	firstGrants, err := store.AgentGrants(ctx, "ws", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondGrants, err := store.AgentGrants(ctx, "ws", second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"agent-b"}; !equalStrings(firstGrants, want) {
		t.Fatalf("first grants = %v, want %v", firstGrants, want)
	}
	if want := []string{"agent-a", "agent-b"}; !equalStrings(secondGrants, want) {
		t.Fatalf("second grants = %v, want %v", secondGrants, want)
	}
	if err := store.SyncAgentSelection(ctx, "ws", "", []string{first.ID}, nil); err == nil {
		t.Fatal("empty agent id must fail closed")
	}
}

func TestPurgeWorkspaceRemovesOnlyTargetWorkspace(t *testing.T) {
	store, err := Open(t.TempDir() + "/connections.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	for _, workspaceID := range []string{"ws_delete", "ws_keep"} {
		connection, err := store.Create(ctx, CreateInput{
			ID:             "shared-id",
			WorkspaceID:    workspaceID,
			OwnerSubject:   "user-1",
			Scope:          ScopeUser,
			Kind:           KindBrowser,
			Name:           "Research login",
			AllowedDomains: []string{"example.com"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ReplaceAgentGrants(ctx, workspaceID, connection.ID, []string{"agent-1"}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := store.PurgeWorkspace(ctx, "ws_delete")
	if err != nil {
		t.Fatal(err)
	}
	if removed.Rows != 2 {
		t.Fatalf("removed rows = %d, want connection plus grant", removed.Rows)
	}
	if _, err := store.Get(ctx, "ws_delete", "shared-id"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted workspace connection still visible: %v", err)
	}
	if _, err := store.Get(ctx, "ws_keep", "shared-id"); err != nil {
		t.Fatalf("neighbour workspace was affected: %v", err)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
