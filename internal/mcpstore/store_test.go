package mcpstore

import (
	"context"
	"path/filepath"
	"testing"
)

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
