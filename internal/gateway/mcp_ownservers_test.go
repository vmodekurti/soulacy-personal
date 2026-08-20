package gateway

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/mcpstore"
	"github.com/soulacy/soulacy/internal/wsroot"
)

func ownServerServer(t *testing.T) (*Server, *mcpstore.Store, *memVault) {
	t.Helper()
	store, err := mcpstore.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	vault := newMemVault()
	s := newTestGateway(t, "secret")
	s.SetCredentialVault(vault)
	s.SetMCPServerStore(store)
	return s, store, vault
}

// The store is plain SQLite on the gateway's disk. A tenant's token must not
// be in it — it goes to the per-workspace vault, under the same namespace the
// operator's template servers use.
func TestASubmittedTokenIsDivertedToTheVaultNotTheDatabase(t *testing.T) {
	s, store, vault := ownServerServer(t)
	ctx := context.Background()
	ws := wsroot.PersonalWorkspaceID

	body := `{"transport":"stdio","command":"mine-mcp",` +
		`"env":{"API_KEY":"tenant-secret","LOG_LEVEL":"debug"}}`
	status, resp := gatewayJSON(t, s, "PUT", "/api/v1/mcp/own/mine", "secret", body)
	if status != 200 {
		t.Fatalf("status = %d body=%v", status, resp)
	}

	stored, err := store.List(ctx, ws)
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored = %+v err=%v", stored, err)
	}
	if got := stored[0].Env["API_KEY"]; got != "" {
		t.Fatalf("the token was written into the database: %q", got)
	}
	if stored[0].Env["LOG_LEVEL"] != "debug" {
		t.Errorf("ordinary configuration was diverted too: %v", stored[0].Env)
	}
	// The KEY survives, or nothing records that the server wants it.
	if _, declared := stored[0].Env["API_KEY"]; !declared {
		t.Error("the credential's name was dropped, so nothing can report it as outstanding")
	}

	value, err := vault.Get(ctx, ws, mcp.CredentialNamespace("mine"), "API_KEY")
	if err != nil || string(value) != "tenant-secret" {
		t.Fatalf("vault value = %q err=%v; the token went nowhere", value, err)
	}
}

// The value has to come back for the server to actually run.
func TestTheStoredServerIsRebuiltWithItsSecret(t *testing.T) {
	s, _, _ := ownServerServer(t)
	body := `{"transport":"stdio","command":"mine-mcp","env":{"API_KEY":"tenant-secret"}}`
	if status, resp := gatewayJSON(t, s, "PUT", "/api/v1/mcp/own/mine", "secret", body); status != 200 {
		t.Fatalf("status=%d body=%v", status, resp)
	}

	servers := s.workspaceOwnedServers(wsroot.PersonalWorkspaceID)
	sc, ok := servers["mine"]
	if !ok {
		t.Fatal("the stored server did not come back")
	}
	if sc.Env["API_KEY"] != "tenant-secret" {
		t.Fatalf("API_KEY = %q, want the value from the vault", sc.Env["API_KEY"])
	}
}

// The listing cannot return a credential because the ROW cannot hold one — the
// guarantee is the diversion on write, not a mask on read. It does return the
// tenant's ordinary configuration, which a blanket mask would have hidden from
// the person who typed it while protecting nothing.
func TestListingOwnServersReturnsConfigButNeverACredential(t *testing.T) {
	s, _, _ := ownServerServer(t)
	body := `{"transport":"stdio","command":"mine-mcp",` +
		`"env":{"API_KEY":"tenant-secret","LOG_LEVEL":"debug"}}`
	gatewayJSON(t, s, "PUT", "/api/v1/mcp/own/mine", "secret", body)

	_, resp := gatewayJSON(t, s, "GET", "/api/v1/mcp/own", "secret", "")
	raw, _ := resp["servers"].([]any)
	if len(raw) != 1 {
		t.Fatalf("servers = %v", resp["servers"])
	}
	entry, _ := raw[0].(map[string]any)
	env, _ := entry["env"].(map[string]any)
	if value, _ := env["API_KEY"].(string); value != "" {
		t.Fatalf("the listing returned a credential: %q", value)
	}
	if value, _ := env["LOG_LEVEL"].(string); value != "debug" {
		t.Errorf("the tenant's own configuration was hidden from them: %q", value)
	}
}

// Deleting removes the definition, so the server stops being started.
func TestDeletingAnOwnServerStopsIt(t *testing.T) {
	s, _, _ := ownServerServer(t)
	gatewayJSON(t, s, "PUT", "/api/v1/mcp/own/mine", "secret",
		`{"transport":"stdio","command":"mine-mcp"}`)
	if len(s.workspaceOwnedServers(wsroot.PersonalWorkspaceID)) != 1 {
		t.Fatal("setup: server not stored")
	}

	if status, resp := gatewayJSON(t, s, "DELETE", "/api/v1/mcp/own/mine", "secret", ""); status != 200 {
		t.Fatalf("delete status=%d body=%v", status, resp)
	}
	if servers := s.workspaceOwnedServers(wsroot.PersonalWorkspaceID); len(servers) != 0 {
		t.Fatalf("the server is still resolved after deletion: %v", servers)
	}
}
