package gateway

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/auth/apikeys"
	mobilechan "github.com/soulacy/soulacy/internal/channels/mobile"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/runtime"
)

func TestLegacyCompanionRowsMoveToTheOwner(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	keys, err := apikeys.NewSQLiteStore(filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { keys.Close() })
	s.SetAPIKeyStore(keys)
	store, _ := mobileFixture(t, s)
	facts, err := memory.OpenFactSQLite(filepath.Join(t.TempDir(), "facts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { facts.Close() })
	s.engine.SetAdaptiveMemory(memory.NewLocalAdaptive(facts, nil, nil, memory.LocalOptions{}), runtime.AdaptiveMemoryOptions{Enabled: true})
	ctx := context.Background()

	// A phone paired before identities existed: legacy key, rows under its id.
	_, legacy, err := keys.Create(ctx, "mobile-companion", []string{"chat"})
	if err != nil {
		t.Fatal(err)
	}
	tok := "1111111111111111111111111111111111111111111111111111111111111111"
	if err := store.UpsertDevice(ctx, "personal", legacy.ID, mobilechan.Device{ID: "old-phone", UserID: legacy.ID, PushToken: tok, NotificationsEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := facts.Insert(ctx, memory.Fact{Workspace: memory.DefaultWorkspace, Owner: legacy.ID, AgentID: "helper", Category: memory.FactPreference, Content: "User prefers tea", Source: "manual", Status: memory.FactStatusActive}); err != nil {
		t.Fatal(err)
	}
	// A household member's key carries a subject and must be left alone.
	_, _, err = keys.CreateFor(ctx, "mobile-companion (Priya)", []string{"chat"}, "priya", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDevice(ctx, "personal", "priya", mobilechan.Device{ID: "priya-phone", UserID: "priya", PushToken: tok, NotificationsEnabled: true}); err != nil {
		t.Fatal(err)
	}

	s.migrateLegacyCompanionIdentities(ctx)
	s.migrateLegacyCompanionIdentities(ctx) // idempotent

	// The owner can now re-register the old phone; the legacy id cannot.
	if err := store.UpsertDevice(ctx, "personal", "admin", mobilechan.Device{ID: "old-phone", UserID: "admin", PushToken: tok, NotificationsEnabled: true}); err != nil {
		t.Fatalf("owner must own the migrated phone: %v", err)
	}
	if err := store.UpsertDevice(ctx, "personal", legacy.ID, mobilechan.Device{ID: "old-phone", UserID: legacy.ID, PushToken: tok, NotificationsEnabled: true}); err != mobilechan.ErrDeviceOwnership {
		t.Fatalf("legacy id must no longer own the phone: %v", err)
	}
	if got, _ := facts.List(ctx, memory.FactScope{Workspace: memory.DefaultWorkspace, Owner: "admin"}, "active", 10); len(got) != 1 {
		t.Fatalf("memory must follow the owner, got %d facts", len(got))
	}
	if err := store.UpsertDevice(ctx, "personal", "priya", mobilechan.Device{ID: "priya-phone", UserID: "priya", PushToken: tok, NotificationsEnabled: true}); err != nil {
		t.Fatalf("household member untouched: %v", err)
	}
}
