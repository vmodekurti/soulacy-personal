package mcpstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenMigratesExistingRegistryToSafeLegacyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE workspace_mcp_servers(
		workspace_id TEXT NOT NULL, id TEXT NOT NULL, transport TEXT NOT NULL DEFAULT '',
		command TEXT NOT NULL DEFAULT '', args TEXT NOT NULL DEFAULT '[]', env TEXT NOT NULL DEFAULT '{}',
		url TEXT NOT NULL DEFAULT '', headers TEXT NOT NULL DEFAULT '{}', inherit_env TEXT NOT NULL DEFAULT '[]',
		created_by TEXT NOT NULL DEFAULT '', updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY(workspace_id,id));
		INSERT INTO workspace_mcp_servers(workspace_id,id,transport,command) VALUES('ws_a','old','container','image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	servers, err := store.List(context.Background(), "ws_a")
	if err != nil || len(servers) != 1 {
		t.Fatalf("migrated servers = %+v, %v", servers, err)
	}
	if servers[0].ContainerNetwork != "public" || servers[0].ContainerWorkspace != "none" {
		t.Fatalf("legacy permissions changed unexpectedly: %+v", servers[0])
	}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// Server IDs are human-chosen slugs and collide across tenants by design. Two
// teams will both call theirs "github", and a single-column key would let the
// second one silently take over the first's definition — the same trap agent
// IDs and plugin IDs have already sprung here.
func TestTwoWorkspacesCanBothOwnAServerCalledGithub(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	if err := store.Put(ctx, Server{WorkspaceID: "ws_a", ID: "github", Command: "a-cmd"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, Server{WorkspaceID: "ws_b", ID: "github", Command: "b-cmd"}); err != nil {
		t.Fatal(err)
	}

	a, err := store.List(ctx, "ws_a")
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || a[0].Command != "a-cmd" {
		t.Fatalf("ws_a = %+v; its definition was overwritten by another tenant's", a)
	}
	b, _ := store.List(ctx, "ws_b")
	if len(b) != 1 || b[0].Command != "b-cmd" {
		t.Fatalf("ws_b = %+v", b)
	}
}

// A listing must never be usable to see another tenant's servers, and deleting
// an id another tenant owns must not touch it.
func TestOneWorkspaceCannotReadOrDeleteAnothersServer(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	if err := store.Put(ctx, Server{WorkspaceID: "ws_a", ID: "private", Command: "a"}); err != nil {
		t.Fatal(err)
	}

	if servers, _ := store.List(ctx, "ws_b"); len(servers) != 0 {
		t.Fatalf("ws_b saw %+v", servers)
	}
	if err := store.Delete(ctx, "ws_b", "private"); err != nil {
		t.Fatal(err)
	}
	if servers, _ := store.List(ctx, "ws_a"); len(servers) != 1 {
		t.Fatal("ws_b's delete removed ws_a's server")
	}
}

// The whole reason this store exists: the in-memory version lost a tenant's
// servers on the next restart, which is a feature that appears to work until
// the first deploy.
func TestAServerSurvivesReopening(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.db")
	ctx := context.Background()

	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Put(ctx, Server{
		WorkspaceID: "ws_a", ID: "mine", Transport: "stdio", Command: "mine-mcp",
		Args: []string{"--stdio"}, Env: map[string]string{"LOG_LEVEL": "debug"},
		InheritEnv: []string{"PATH"}, CreatedBy: "usr_alice",
	}); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()

	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })

	servers, err := second.List(ctx, "ws_a")
	if err != nil || len(servers) != 1 {
		t.Fatalf("after reopen: %+v err=%v", servers, err)
	}
	got := servers[0]
	if got.Command != "mine-mcp" || len(got.Args) != 1 || got.Args[0] != "--stdio" {
		t.Errorf("command/args lost: %+v", got)
	}
	if got.Env["LOG_LEVEL"] != "debug" || len(got.InheritEnv) != 1 {
		t.Errorf("env lost: %+v", got)
	}
	if got.CreatedBy != "usr_alice" {
		t.Errorf("author lost: %q", got.CreatedBy)
	}
}

func TestReviewedEnvironmentContractSurvivesReopening(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []EnvironmentItem{{Name: "EXA_API_KEY", Description: "Research search key", Secret: true}, {Name: "LLM_MODEL", Required: false}}
	if err := first.Put(context.Background(), Server{WorkspaceID: "ws_a", ID: "maverick", Environment: want}); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	servers, err := second.List(context.Background(), "ws_a")
	if err != nil || len(servers) != 1 || len(servers[0].Environment) != 2 {
		t.Fatalf("environment contract = %+v, err=%v", servers, err)
	}
	if got := servers[0].Environment[0]; got.Name != "EXA_API_KEY" || !got.Secret || got.Description != "Research search key" {
		t.Fatalf("environment item changed: %+v", got)
	}
}

// Deleting a workspace must take its servers with it, and only its own.
func TestPurgeRemovesOnlyThatWorkspacesServers(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	_ = store.Put(ctx, Server{WorkspaceID: "ws_a", ID: "one", Command: "x"})
	_ = store.Put(ctx, Server{WorkspaceID: "ws_a", ID: "two", Command: "y"})
	_ = store.Put(ctx, Server{WorkspaceID: "ws_b", ID: "one", Command: "z"})

	n, err := store.PurgeWorkspace(ctx, "ws_a")
	if err != nil || n != 2 {
		t.Fatalf("purged %d err=%v", n, err)
	}
	if servers, _ := store.List(ctx, "ws_b"); len(servers) != 1 {
		t.Fatal("purging ws_a removed ws_b's server")
	}
}

// An unopened store means "this workspace has no servers of its own", never
// "fall back to something shared".
func TestAnUnavailableStoreIsNotAFallback(t *testing.T) {
	var store *Store
	if _, err := store.List(context.Background(), "ws_a"); err == nil {
		t.Fatal("a nil store answered a listing")
	}
	if err := store.Put(context.Background(), Server{WorkspaceID: "ws_a", ID: "x"}); err == nil {
		t.Fatal("a nil store accepted a write")
	}
}

func TestInstallRequestsAreDurableScopedAndDecidedOnce(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	requestedAt := time.Now().UTC().Truncate(time.Second)
	for _, request := range []InstallRequest{
		{ID: "mcp_req_a", WorkspaceID: "ws_a", SourceURL: "https://github.com/acme/a", Reason: "agent a", Network: "public", WorkspaceAccess: "none", RequestedBy: "usr_alice", RequestedAt: requestedAt},
		{ID: "mcp_req_b", WorkspaceID: "ws_a", SourceURL: "https://github.com/acme/b", Network: "none", WorkspaceAccess: "read", RequestedBy: "usr_bob", RequestedAt: requestedAt.Add(time.Second)},
		{ID: "mcp_req_c", WorkspaceID: "ws_b", SourceURL: "https://github.com/acme/c", RequestedBy: "usr_alice", RequestedAt: requestedAt},
	} {
		if err := store.CreateInstallRequest(ctx, request); err != nil {
			t.Fatal(err)
		}
	}

	alice, err := store.ListInstallRequests(ctx, "ws_a", "usr_alice")
	if err != nil || len(alice) != 1 || alice[0].ID != "mcp_req_a" {
		t.Fatalf("alice requests = %+v, err=%v", alice, err)
	}
	all, err := store.ListInstallRequests(ctx, "ws_a", "")
	if err != nil || len(all) != 2 {
		t.Fatalf("admin requests = %+v, err=%v", all, err)
	}
	if _, err := store.GetInstallRequest(ctx, "ws_b", "mcp_req_a"); err != sql.ErrNoRows {
		t.Fatalf("cross-workspace request read = %v", err)
	}
	if err := store.DecideInstallRequest(ctx, "ws_a", "mcp_req_a", RequestInstalled, "usr_admin", "reviewed", "server-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.DecideInstallRequest(ctx, "ws_a", "mcp_req_a", RequestDenied, "usr_admin", "again", ""); err != sql.ErrNoRows {
		t.Fatalf("second decision = %v, want sql.ErrNoRows", err)
	}
	got, err := store.GetInstallRequest(ctx, "ws_a", "mcp_req_a")
	if err != nil || got.Status != RequestInstalled || got.InstalledServerID != "server-a" || got.DecidedBy != "usr_admin" || got.DecidedAt == nil {
		t.Fatalf("decided request = %+v, err=%v", got, err)
	}
}

func TestPurgeWorkspaceAlsoRemovesInstallRequests(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	_ = store.Put(ctx, Server{WorkspaceID: "ws_a", ID: "one"})
	_ = store.CreateInstallRequest(ctx, InstallRequest{ID: "mcp_req_a", WorkspaceID: "ws_a", SourceURL: "https://github.com/acme/a", RequestedBy: "usr_a"})
	_ = store.CreateInstallRequest(ctx, InstallRequest{ID: "mcp_req_b", WorkspaceID: "ws_b", SourceURL: "https://github.com/acme/b", RequestedBy: "usr_b"})

	n, err := store.PurgeWorkspace(ctx, "ws_a")
	if err != nil || n != 2 {
		t.Fatalf("purged %d rows, err=%v", n, err)
	}
	if requests, _ := store.ListInstallRequests(ctx, "ws_a", ""); len(requests) != 0 {
		t.Fatalf("purged workspace retained requests: %+v", requests)
	}
	if requests, _ := store.ListInstallRequests(ctx, "ws_b", ""); len(requests) != 1 {
		t.Fatalf("other workspace request changed: %+v", requests)
	}
}
