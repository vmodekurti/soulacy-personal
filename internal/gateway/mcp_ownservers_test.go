package gateway

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
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

func TestTeamWorkspaceMCPPolicyRejectsExecutableAndHostDestinations(t *testing.T) {
	s, _, _ := ownServerServer(t)
	s.config().Deployment.Mode = config.DeploymentModeTeam

	bad := []ownServerBody{
		{Transport: "stdio", Command: "/bin/sh", Args: []string{"-c", "id"}},
		{Transport: "http", URL: "http://example.com/mcp"},
		{Transport: "http", URL: "https://127.0.0.1/mcp"},
		{Transport: "http", URL: "https://token@example.com/mcp"},
		{Transport: "http", URL: "https://93.184.216.34/mcp", InheritEnv: []string{"DATABASE_URL"}},
		{Transport: "http", URL: "https://93.184.216.34/mcp", Env: map[string]string{"TOKEN": "x"}},
	}
	for i, body := range bad {
		if err := s.validateOwnMCPPolicy(body); err == nil {
			t.Errorf("unsafe definition %d was accepted: %+v", i, body)
		}
	}

	if err := s.validateOwnMCPPolicy(ownServerBody{Transport: "http", URL: "https://93.184.216.34/mcp"}); err != nil {
		t.Fatalf("public remote MCP definition was rejected: %v", err)
	}
}

func TestTeamWorkspaceWithholdsLegacyStdioDefinition(t *testing.T) {
	s, store, _ := ownServerServer(t)
	s.config().Deployment.Mode = config.DeploymentModeTeam
	if err := store.Put(context.Background(), mcpstore.Server{
		WorkspaceID: wsroot.PersonalWorkspaceID, ID: "legacy", Transport: "stdio", Command: "/bin/sh",
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.workspaceOwnedServers(wsroot.PersonalWorkspaceID); len(got) != 0 {
		t.Fatalf("legacy executable definition was loaded in Team mode: %+v", got)
	}
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

func TestContainerServerReceivesOnlyItsCanonicalWorkspaceRoot(t *testing.T) {
	s, store, _ := ownServerServer(t)
	root := t.TempDir()
	s.SetWorkspaceLayoutRoot(root)
	workspaceID := "ws_tenant_one"
	if err := store.Put(context.Background(), mcpstore.Server{
		WorkspaceID:        workspaceID,
		ID:                 "isolated",
		Transport:          "container",
		Command:            "example.invalid/mcp@sha256:" + strings.Repeat("a", 64),
		ContainerNetwork:   "none",
		ContainerWorkspace: "read",
	}); err != nil {
		t.Fatal(err)
	}

	server, ok := s.workspaceOwnedServers(workspaceID)["isolated"]
	if !ok {
		t.Fatal("container server was not resolved")
	}
	want := filepath.Join(root, wsroot.WorkspaceDir, workspaceID)
	if server.WorkDir != want {
		t.Fatalf("container workspace root = %q, want %q", server.WorkDir, want)
	}
	wantData := filepath.Join(want, ".mcp-data", "isolated")
	if server.ContainerDataDir != wantData {
		t.Fatalf("container private data = %q, want %q", server.ContainerDataDir, wantData)
	}
	if info, err := os.Stat(wantData); err != nil || !info.IsDir() {
		t.Fatalf("private data directory was not created: %v", err)
	}
}

func TestSafeContainerLocalSettingIsNarrow(t *testing.T) {
	for _, value := range []string{"sqlite:////data/maverick.db", "sqlite:////data/server-1.db"} {
		if !safeContainerLocalSetting("DATABASE_URL", value) {
			t.Fatalf("generated local database rejected: %q", value)
		}
	}
	for _, value := range []string{
		"postgres://user:pass@db/app", "sqlite:////etc/passwd", "sqlite:////data/../other.db",
		"sqlite:////data/nested/other.db", "sqlite:////data/no-extension",
	} {
		if safeContainerLocalSetting("DATABASE_URL", value) {
			t.Fatalf("unsafe or user-supplied database accepted: %q", value)
		}
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

func TestWorkspaceAdminCanConfigureReviewedMCPSettingsWithoutSecretDisclosure(t *testing.T) {
	s, store, vault := ownServerServer(t)
	ws := wsroot.PersonalWorkspaceID
	if err := store.Put(context.Background(), mcpstore.Server{
		WorkspaceID: ws, ID: "maverick", Transport: "container",
		Command: "example.invalid/maverick@sha256:" + strings.Repeat("a", 64),
		Env:     map[string]string{"EXA_API_KEY": ""},
		Environment: []mcpstore.EnvironmentItem{
			{Name: "LLM_PROVIDER", Description: "Provider", Required: false},
			{Name: "EXA_API_KEY", Description: "Research key", Secret: true},
		},
		ContainerNetwork: "public", ContainerWorkspace: "none",
	}); err != nil {
		t.Fatal(err)
	}
	status, response := gatewayJSON(t, s, "PUT", "/api/v1/mcp/own/maverick/settings", "secret",
		`{"settings":{"LLM_PROVIDER":"openai","EXA_API_KEY":"exa-sensitive"}}`)
	if status != 200 {
		t.Fatalf("configure status=%d body=%v", status, response)
	}
	value, err := vault.Get(context.Background(), ws, mcp.CredentialNamespace("maverick"), "EXA_API_KEY")
	if err != nil || string(value) != "exa-sensitive" {
		t.Fatalf("vault value=%q err=%v", value, err)
	}
	servers, _ := store.List(context.Background(), ws)
	if servers[0].Env["LLM_PROVIDER"] != "openai" || servers[0].Env["EXA_API_KEY"] != "" {
		t.Fatalf("stored environment leaked or lost a setting: %+v", servers[0].Env)
	}
	_, raw := gatewayRaw(t, s, "GET", "/api/v1/mcp/own", "secret", "")
	if strings.Contains(raw, "exa-sensitive") || !strings.Contains(raw, `"configured":true`) {
		t.Fatalf("listing leaked the value or hid its status: %s", raw)
	}
}

func TestLegacyMCPSettingsExposeOnlyUnsetCredentialPlaceholders(t *testing.T) {
	s, store, _ := ownServerServer(t)
	if err := store.Put(context.Background(), mcpstore.Server{
		WorkspaceID: wsroot.PersonalWorkspaceID, ID: "maverick",
		Env: map[string]string{
			"DATABASE_URL": "sqlite:////data/maverick.db",
			"EXA_API_KEY":  "",
			"LLM_API_KEY":  "",
		},
	}); err != nil {
		t.Fatal(err)
	}
	_, raw := gatewayRaw(t, s, "GET", "/api/v1/mcp/own", "secret", "")
	if strings.Contains(raw, `"name":"DATABASE_URL"`) {
		t.Fatalf("configured generated database was offered as a missing credential: %s", raw)
	}
	if !strings.Contains(raw, `"name":"EXA_API_KEY"`) ||
		!strings.Contains(raw, `"name":"LLM_API_KEY"`) {
		t.Fatalf("legacy API-key placeholders were not exposed: %s", raw)
	}
}

func TestWorkspaceMCPConfigurationRejectsUndeclaredSettings(t *testing.T) {
	s, store, _ := ownServerServer(t)
	if err := store.Put(context.Background(), mcpstore.Server{
		WorkspaceID: wsroot.PersonalWorkspaceID, ID: "maverick",
		Environment: []mcpstore.EnvironmentItem{{Name: "EXA_API_KEY", Secret: true}},
	}); err != nil {
		t.Fatal(err)
	}
	status, _ := gatewayJSON(t, s, "PUT", "/api/v1/mcp/own/maverick/settings", "secret",
		`{"settings":{"UNREVIEWED_SECRET":"nope"}}`)
	if status != 400 {
		t.Fatalf("undeclared setting status=%d, want 400", status)
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
