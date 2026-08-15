// workspace_test.go — the cross-tenant isolation contract for agent memory.
//
// internal/ownership/catalog.go names this file as the isolation evidence for
// the memories table and the memory repositories.
package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/wsroot"
)

func entry(workspaceID, agentID, sessionID, content string) Entry {
	return Entry{
		WorkspaceID: workspaceID, AgentID: agentID, SessionID: sessionID,
		Scope: ScopeSession, Content: content,
	}
}

// A memory with no owner is a memory every tenant can read, so a write without
// a workspace fails rather than defaulting.
func TestWritesWithoutAWorkspaceAreRefused(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Write(entry("", "bot", "s1", "orphan")); !errors.Is(err, ErrWorkspaceRequired) {
		t.Fatalf("write without a workspace = %v, want ErrWorkspaceRequired", err)
	}
	if _, err := store.Read("", "bot", "s1", ScopeSession, 10); !errors.Is(err, ErrWorkspaceRequired) {
		t.Fatalf("read without a workspace = %v", err)
	}
	if _, err := store.Search("", "bot", "orphan", 10); !errors.Is(err, ErrWorkspaceRequired) {
		t.Fatalf("search without a workspace = %v", err)
	}
	if err := store.PurgeSession("", "s1"); !errors.Is(err, ErrWorkspaceRequired) {
		t.Fatalf("purge without a workspace = %v", err)
	}

	archive, err := NewSQLiteArchive(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if err := archive.Archive(entry("", "bot", "s1", "orphan")); !errors.Is(err, ErrWorkspaceRequired) {
		t.Fatalf("archive without a workspace = %v", err)
	}
}

// A workspace ID becomes a directory name, so an unusable one is refused
// before it is joined into a path.
func TestUnusableWorkspaceIDsAreRefusedOnWrite(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"..", ".", "../escape", "ws/../../etc", "WS_UPPER"} {
		if err := store.Write(entry(bad, "bot", "s1", "x")); err == nil {
			t.Errorf("workspace ID %q was accepted", bad)
		}
	}
}

// The same agent and session ID in two workspaces are two separate
// conversations, and neither can read the other.
func TestFileMemoryIsIsolatedAcrossWorkspaces(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Write(entry("ws_a", "bot", "shared-session", "alpha secret")); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(entry("ws_b", "bot", "shared-session", "beta secret")); err != nil {
		t.Fatal(err)
	}

	a, err := store.Read("ws_a", "bot", "shared-session", ScopeSession, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || a[0].Content != "alpha secret" {
		t.Fatalf("ws_a read %+v", a)
	}
	b, err := store.Read("ws_b", "bot", "shared-session", ScopeSession, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 1 || b[0].Content != "beta secret" {
		t.Fatalf("ws_b read %+v", b)
	}

	// Search is the easiest place to leak, because it walks a directory.
	found, err := store.Search("ws_b", "bot", "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("search crossed the tenant boundary: %+v", found)
	}

	// A personal search must not descend into the workspace namespace.
	if err := store.Write(entry(wsroot.PersonalWorkspaceID, "bot", "shared-session", "personal note")); err != nil {
		t.Fatal(err)
	}
	personal, err := store.Search(wsroot.PersonalWorkspaceID, "bot", "secret", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(personal) != 0 {
		t.Fatalf("a personal search read tenant memory: %+v", personal)
	}
}

// Purging a session must not delete a same-named session belonging to another
// tenant. That failure is silent, immediate, and irreversible.
func TestPurgeSessionCannotReachAnotherWorkspace(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, workspace := range []string{wsroot.PersonalWorkspaceID, "ws_a", "ws_b"} {
		if err := store.Write(entry(workspace, "bot", "shared-session", workspace+" content")); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PurgeSession("ws_a", "shared-session"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		workspace string
		wantAny   bool
	}{
		{"ws_a", false},
		{"ws_b", true},
		{wsroot.PersonalWorkspaceID, true},
	} {
		got, err := store.Read(tc.workspace, "bot", "shared-session", ScopeSession, 10)
		if err != nil {
			t.Fatal(err)
		}
		if tc.wantAny && len(got) == 0 {
			t.Errorf("%s lost its memory to another workspace's purge", tc.workspace)
		}
		if !tc.wantAny && len(got) != 0 {
			t.Errorf("%s was not purged: %+v", tc.workspace, got)
		}
	}

	// And the reverse: a personal purge must not descend into the namespace.
	if err := store.PurgeSession(wsroot.PersonalWorkspaceID, "shared-session"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Read("ws_b", "bot", "shared-session", ScopeSession, 10); err != nil || len(got) == 0 {
		t.Fatalf("a personal purge deleted a tenant's memory: %+v %v", got, err)
	}
}

// Product invariant 7: a personal installation's memory stays where it was.
func TestPersonalMemoryPathIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Write(entry(wsroot.PersonalWorkspaceID, "bot", "s1", "note")); err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(dir, "bot", "s1.jsonl")
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("personal memory is not at its historical path %s: %v", expected, err)
	}
	if _, err := os.Stat(filepath.Join(dir, wsroot.NamespaceDir, "ws_a", "bot", "s1.jsonl")); err == nil {
		t.Fatal("a personal write landed in the workspace namespace")
	}
}

// Every archive query carries the tenant predicate, including the ones that
// read across sessions.
func TestArchiveQueriesAreWorkspaceScoped(t *testing.T) {
	archive, err := NewSQLiteArchive(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()

	for _, workspace := range []string{"ws_a", "ws_b"} {
		record := entry(workspace, "bot", "shared-session", workspace+" content")
		record.ID = workspace + "-1"
		if err := archive.Archive(record); err != nil {
			t.Fatal(err)
		}
	}

	scoped, err := archive.ReadByScope("ws_a", "bot", "shared-session", ScopeSession, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].WorkspaceID != "ws_a" {
		t.Fatalf("ReadByScope returned %+v", scoped)
	}

	global, err := archive.ReadGlobal("ws_a", "bot", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range global {
		if record.WorkspaceID != "ws_a" {
			t.Fatalf("ReadGlobal crossed the boundary: %+v", record)
		}
	}

	found, err := archive.Search("ws_b", "bot", "ws_a content", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("archive search crossed the boundary: %+v", found)
	}
}

// An archive written before tenants existed keeps working: the column is added
// in place and its rows are backfilled to the personal workspace, which is
// what a single-user installation's memory was.
func TestExistingArchiveMigratesToPersonalWorkspace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	archive, err := NewSQLiteArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := entry(wsroot.PersonalWorkspaceID, "bot", "s1", "written before tenants")
	legacy.ID = "legacy-1"
	if err := archive.Archive(legacy); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-migration row.
	if _, err := archive.DB().Exec(`UPDATE memories SET workspace_id='' WHERE id='legacy-1'`); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewSQLiteArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.ReadGlobal(wsroot.PersonalWorkspaceID, "bot", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Content, "before tenants") {
		t.Fatalf("a pre-existing memory was lost by the migration: %+v", got)
	}
}
