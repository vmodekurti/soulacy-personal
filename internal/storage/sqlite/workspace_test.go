// workspace_test.go — the cross-tenant isolation contract for the SQLite
// storage shims. internal/ownership/catalog.go names this file as the
// isolation evidence for internal/storage/sqlite/sqlite.go.
//
// The shims exist to satisfy two interfaces at once: the frozen
// sdk/storage.MemoryBackend, whose read methods have no workspace parameter,
// and the optional WorkspaceMemoryBackend, whose do. Which underlying
// workspace each frozen method resolves to is the whole contract, and it is
// exactly the kind of mapping that is invisible in review — every method body
// is one line.
package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/actionlog"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/wsroot"
	sdkstorage "github.com/soulacy/soulacy/sdk/storage"
	"go.uber.org/zap"
)

func newShim(t *testing.T) *MemoryArchive {
	t.Helper()
	archive, err := memory.NewSQLiteArchive(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatalf("NewSQLiteArchive: %v", err)
	}
	t.Cleanup(func() { _ = archive.Close() })
	return NewMemoryArchive(archive)
}

func archiveEntry(t *testing.T, shim *MemoryArchive, workspaceID, id, content string) {
	t.Helper()
	if err := shim.Archive(memory.Entry{
		WorkspaceID: workspaceID, ID: id, AgentID: "assistant", SessionID: "s1",
		Scope: memory.ScopeSession, Key: id, Content: content,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("archive %s: %v", id, err)
	}
}

// The three *InWorkspace reads return that workspace's entries and no others.
func TestScopedReadsReturnOnlyTheirWorkspace(t *testing.T) {
	shim := newShim(t)
	archiveEntry(t, shim, "ws_a", "a1", "alpha confidential")
	archiveEntry(t, shim, "ws_b", "b1", "beta confidential")
	archiveEntry(t, shim, wsroot.PersonalWorkspaceID, "p1", "personal note")

	for _, tc := range []struct{ workspace, want string }{
		{"ws_a", "alpha confidential"},
		{"ws_b", "beta confidential"},
		{wsroot.PersonalWorkspaceID, "personal note"},
	} {
		global, err := shim.ReadGlobalInWorkspace(tc.workspace, "assistant", 10)
		if err != nil {
			t.Fatalf("ReadGlobalInWorkspace %s: %v", tc.workspace, err)
		}
		if len(global) != 1 || global[0].Content != tc.want {
			t.Fatalf("ReadGlobalInWorkspace %s = %+v, want %q", tc.workspace, global, tc.want)
		}
		scoped, err := shim.ReadByScopeInWorkspace(tc.workspace, "assistant", "s1", memory.ScopeSession, 10)
		if err != nil {
			t.Fatalf("ReadByScopeInWorkspace %s: %v", tc.workspace, err)
		}
		if len(scoped) != 1 || scoped[0].Content != tc.want {
			t.Fatalf("ReadByScopeInWorkspace %s = %+v, want %q", tc.workspace, scoped, tc.want)
		}
		found, err := shim.SearchInWorkspace(tc.workspace, "assistant", "confidential", 10)
		if err != nil {
			t.Fatalf("SearchInWorkspace %s: %v", tc.workspace, err)
		}
		for _, entry := range found {
			if entry.WorkspaceID != tc.workspace {
				t.Fatalf("SearchInWorkspace %s returned %s's entry: %+v", tc.workspace, entry.WorkspaceID, entry)
			}
		}
	}
}

// The frozen methods mean personal, and nothing else. A caller holding only
// sdk/storage.MemoryBackend is by definition single-tenant; if these ever
// widened to "all workspaces" that caller would silently start reading every
// tenant's memory.
func TestTheFrozenReadsResolveToPersonalAndOnlyPersonal(t *testing.T) {
	shim := newShim(t)
	archiveEntry(t, shim, "ws_a", "a1", "alpha confidential")
	archiveEntry(t, shim, wsroot.PersonalWorkspaceID, "p1", "personal confidential")

	global, err := shim.ReadGlobal("assistant", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(global) != 1 || global[0].ID != "p1" {
		t.Fatalf("ReadGlobal = %+v, want only the personal entry", global)
	}
	scoped, err := shim.ReadByScope("assistant", "s1", memory.ScopeSession, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].ID != "p1" {
		t.Fatalf("ReadByScope = %+v, want only the personal entry", scoped)
	}
	found, err := shim.Search("assistant", "confidential", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range found {
		if entry.ID != "p1" {
			t.Fatalf("Search through the frozen surface returned a tenant's entry: %+v", entry)
		}
	}
}

// The action-log shim's path helpers are the same contract in file form: the
// personal path is the historical one, and a tenant's is somewhere else
// entirely rather than the same file with a filter.
func TestActionLogPathsSeparateTenantsFromPersonal(t *testing.T) {
	dir := t.TempDir()
	logger, err := actionlog.New(dir, filepath.Join(dir, "actions.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("actionlog.New: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	log := NewActionLog(logger)

	personal := log.EventFilePath("assistant")
	explicit := log.EventFilePathInWorkspace(wsroot.PersonalWorkspaceID, "assistant")
	tenant := log.EventFilePathInWorkspace("ws_a", "assistant")

	if personal != explicit {
		t.Fatalf("the two ways of naming personal gave different files: %q vs %q", personal, explicit)
	}
	if tenant == personal {
		t.Fatal("a tenant's events share the personal file")
	}
	if filepath.Dir(tenant) == filepath.Dir(personal) {
		t.Fatalf("a tenant's events are not namespaced: %q", tenant)
	}
}

// Compile-time: the shims must keep satisfying the optional tenant-aware
// interfaces. Losing one would not break any build that only needs the frozen
// interface — it would silently downgrade every tenant-aware caller to a
// refusal, or worse, to the personal fallback.
var (
	_ sdkstorage.WorkspaceMemoryBackend    = (*MemoryArchive)(nil)
	_ sdkstorage.WorkspaceActionLogBackend = (*ActionLog)(nil)
)
