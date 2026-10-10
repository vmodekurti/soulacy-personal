package authconnections

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

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
	if err := vault.WriteBlob(ctx, vaultNamespace(private.ID), "browser_storage_state", state); err != nil {
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

func TestUnreadableSessionExpiresConnection(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "connections.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	conn, err := store.Create(ctx, CreateInput{
		WorkspaceID: "ws", OwnerSubject: "alice", Scope: ScopeUser,
		Kind: KindBrowser, Name: "HBR", BaseURL: "https://hbr.org",
		AllowedDomains: []string{"hbr.org"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceAgentGrants(ctx, "ws", conn.ID, []string{"research"}); err != nil {
		t.Fatal(err)
	}
	firstKMS, _ := credentials.NewPassthroughKMS([]byte("11111111111111111111111111111111"))
	vaultPath := filepath.Join(dir, "vault.db")
	firstVault, err := credentials.NewSQLiteVault(vaultPath, firstKMS)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstVault.WriteBlob(ctx, vaultNamespace(conn.ID), browserStateKey, []byte(`{"cookies":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSecret(ctx, "ws", conn.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := firstVault.Close(); err != nil {
		t.Fatal(err)
	}

	secondKMS, _ := credentials.NewPassthroughKMS([]byte("22222222222222222222222222222222"))
	secondVault, err := credentials.NewSQLiteVault(vaultPath, secondKMS)
	if err != nil {
		t.Fatal(err)
	}
	defer secondVault.Close()
	resolver := NewResolver(store, secondVault)
	if _, err := resolver.Resolve(ctx, "ws", "alice", "research", conn.ID); !errors.Is(err, ErrSessionUnreadable) {
		t.Fatalf("Resolve error = %v, want ErrSessionUnreadable", err)
	}
	got, err := store.Get(ctx, "ws", conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusExpired {
		t.Fatalf("connection status = %q, want %q", got.Status, StatusExpired)
	}
	if visible := resolver.Describe(ctx, "ws", "alice", "research", []string{conn.ID}); len(visible) != 0 {
		t.Fatalf("unreadable connection remains available to agents: %+v", visible)
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

func TestUpdateBrowserStateIsSerializedAndSkipsUnchanged(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "connections.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	conn, err := store.Create(ctx, CreateInput{WorkspaceID: "ws", OwnerSubject: "alice", Scope: ScopeUser, Kind: KindBrowser, Name: "Gartner", BaseURL: "https://gartner.com", AllowedDomains: []string{"gartner.com"}})
	if err != nil {
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
	if err := vault.WriteBlob(ctx, vaultNamespace(conn.ID), browserStateKey, []byte("0")); err != nil {
		t.Fatal(err)
	}
	resolver := NewResolver(store, vault)
	// Not ready until the secret is marked: the update must refuse.
	if err := resolver.UpdateBrowserState(ctx, "ws", conn.ID, func(c []byte) ([]byte, bool, error) { return []byte("1"), true, nil }); !errors.Is(err, ErrNotReady) {
		t.Fatalf("want ErrNotReady before the secret is marked, got %v", err)
	}
	if err := store.MarkSecret(ctx, "ws", conn.ID, nil); err != nil {
		t.Fatal(err)
	}
	const workers = 20
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := resolver.UpdateBrowserState(ctx, "ws", conn.ID, func(c []byte) ([]byte, bool, error) {
				n, _ := strconv.Atoi(string(c))
				return []byte(strconv.Itoa(n + 1)), true, nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, _ := vault.ReadBlob(ctx, vaultNamespace(conn.ID), browserStateKey)
	if string(got) != strconv.Itoa(workers) {
		t.Fatalf("lost updates: state = %s, want %d", got, workers)
	}
	before, err := store.Get(ctx, "ws", conn.ID)
	if err != nil || before.LastValidatedAt == nil {
		t.Fatalf("connection before unchanged update = %+v, err=%v", before, err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := resolver.UpdateBrowserState(ctx, "ws", conn.ID, func(c []byte) ([]byte, bool, error) { return []byte("999"), false, nil }); err != nil {
		t.Fatal(err)
	}
	if again, _ := vault.ReadBlob(ctx, vaultNamespace(conn.ID), browserStateKey); string(again) != strconv.Itoa(workers) {
		t.Fatalf("an unchanged update must not write, got %s", again)
	}
	after, err := store.Get(ctx, "ws", conn.ID)
	if err != nil || after.LastValidatedAt == nil || !after.LastValidatedAt.After(*before.LastValidatedAt) {
		t.Fatalf("successful unchanged update did not refresh health: before=%v after=%v err=%v", before.LastValidatedAt, after.LastValidatedAt, err)
	}
	if err := store.SetStatus(ctx, "ws", conn.ID, StatusExpired); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkValidated(ctx, "ws", conn.ID); !errors.Is(err, ErrNotReady) {
		t.Fatalf("expired connection was revived by a late validation: %v", err)
	}
}
