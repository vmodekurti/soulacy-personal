// memory_scope_test.go — how the engine reads an agent's archived memory when
// the backend behind sdk/storage.MemoryBackend may or may not be able to name
// a tenant.
//
// MemoryBackend is frozen for this SDK major version, so its read methods take
// no workspace and the shipped shims resolve them to the personal workspace.
// That is right for a single-tenant caller and wrong in both directions for a
// tenant: it returns the personal workspace's memories — a cross-tenant read —
// while hiding the caller's own. This file pins which surface is used, and
// what happens when the tenant-aware one is absent.
package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/storage"
	"github.com/soulacy/soulacy/internal/wsroot"
	sdkstorage "github.com/soulacy/soulacy/sdk/storage"
)

// frozenArchive implements only the frozen MemoryBackend — the shape of an
// external sidecar backend (internal/extstorage) that predates workspaces.
type frozenArchive struct {
	entries []memory.Entry
}

func (f *frozenArchive) Archive(entry memory.Entry) error {
	f.entries = append(f.entries, entry)
	return nil
}
func (f *frozenArchive) Search(agentID, query string, limit int) ([]memory.Entry, error) {
	return f.entries, nil
}
func (f *frozenArchive) ReadByScope(agentID, sessionID string, scope memory.Scope, limit int) ([]memory.Entry, error) {
	return f.entries, nil
}
func (f *frozenArchive) ReadGlobal(agentID string, limit int) ([]memory.Entry, error) {
	return f.entries, nil
}
func (f *frozenArchive) Prune(agentID string, before time.Time) (int64, error) { return 0, nil }
func (f *frozenArchive) Close() error                                          { return nil }

var _ storage.MemoryBackend = (*frozenArchive)(nil)

// scopedArchive implements the optional tenant-aware surface as well, and
// records which surface each call arrived through.
type scopedArchive struct {
	frozenArchive
	byWorkspace  map[string][]memory.Entry
	unscopedRead int
}

func newScopedArchive() *scopedArchive {
	return &scopedArchive{byWorkspace: map[string][]memory.Entry{}}
}

func (s *scopedArchive) put(workspaceID, content string) {
	s.byWorkspace[workspaceID] = append(s.byWorkspace[workspaceID],
		memory.Entry{WorkspaceID: workspaceID, ID: workspaceID + "-" + content, Content: content})
}

func (s *scopedArchive) ReadGlobal(agentID string, limit int) ([]memory.Entry, error) {
	s.unscopedRead++
	return s.byWorkspace[wsroot.PersonalWorkspaceID], nil
}

func (s *scopedArchive) Search(agentID, query string, limit int) ([]memory.Entry, error) {
	s.unscopedRead++
	return s.byWorkspace[wsroot.PersonalWorkspaceID], nil
}

func (s *scopedArchive) SearchInWorkspace(workspaceID, agentID, query string, limit int) ([]memory.Entry, error) {
	return s.byWorkspace[workspaceID], nil
}

func (s *scopedArchive) ReadByScopeInWorkspace(workspaceID, agentID, sessionID string, scope memory.Scope, limit int) ([]memory.Entry, error) {
	return s.byWorkspace[workspaceID], nil
}

func (s *scopedArchive) ReadGlobalInWorkspace(workspaceID, agentID string, limit int) ([]memory.Entry, error) {
	return s.byWorkspace[workspaceID], nil
}

var _ sdkstorage.WorkspaceMemoryBackend = (*scopedArchive)(nil)

// A tenant-aware backend is asked through its tenant-aware surface, and the
// frozen one is never touched — reaching it would mean serving the personal
// workspace's memories to a tenant.
func TestMemoryReadsUseTheTenantAwareSurface(t *testing.T) {
	archive := newScopedArchive()
	archive.put(wsroot.PersonalWorkspaceID, "personal note")
	archive.put("ws_a", "A's note")
	archive.put("ws_b", "B's note")
	e := &Engine{archive: archive}

	for _, tc := range []struct{ workspace, want string }{
		{"ws_a", "A's note"},
		{"ws_b", "B's note"},
		{wsroot.PersonalWorkspaceID, "personal note"},
	} {
		entries, err := e.MemoryList(tc.workspace, "assistant", 10)
		if err != nil {
			t.Fatalf("list %s: %v", tc.workspace, err)
		}
		if len(entries) != 1 || entries[0].Content != tc.want {
			t.Fatalf("list %s returned %+v, want %q", tc.workspace, entries, tc.want)
		}
		found, err := e.MemorySearch(tc.workspace, "assistant", "note", 10)
		if err != nil {
			t.Fatalf("search %s: %v", tc.workspace, err)
		}
		if len(found) != 1 || found[0].Content != tc.want {
			t.Fatalf("search %s returned %+v, want %q", tc.workspace, found, tc.want)
		}
	}
	if archive.unscopedRead != 0 {
		t.Fatalf("the frozen unscoped read surface was used %d times", archive.unscopedRead)
	}
}

// A backend with no tenant-aware surface refuses a tenant rather than
// answering with the personal workspace's memories.
func TestABackendThatCannotNameATenantRefusesRatherThanSubstitutePersonal(t *testing.T) {
	archive := &frozenArchive{entries: []memory.Entry{{ID: "p1", Content: "personal note"}}}
	e := &Engine{archive: archive}

	if _, err := e.MemoryList("ws_a", "assistant", 10); !errors.Is(err, ErrMemoryArchiveNotTenantAware) {
		t.Fatalf("tenant list returned %v, want ErrMemoryArchiveNotTenantAware", err)
	}
	if _, err := e.MemorySearch("ws_a", "assistant", "note", 10); !errors.Is(err, ErrMemoryArchiveNotTenantAware) {
		t.Fatalf("tenant search returned %v, want ErrMemoryArchiveNotTenantAware", err)
	}

	// Personal is exactly what the frozen methods mean, so it still works —
	// product invariant 7. A refusal here would break every single-user
	// deployment on a backend that was never wrong for them.
	entries, err := e.MemoryList(wsroot.PersonalWorkspaceID, "assistant", 10)
	if err != nil {
		t.Fatalf("personal list on a frozen backend: %v", err)
	}
	if len(entries) != 1 || entries[0].Content != "personal note" {
		t.Fatalf("personal list returned %+v", entries)
	}
	if entries, err = e.MemoryList("", "assistant", 10); err != nil || len(entries) != 1 {
		t.Fatalf("an empty workspace must mean personal: %+v err=%v", entries, err)
	}
}

// No archive at all stays a quiet empty result: memory archiving is optional,
// and a deployment without it is not a deployment with a broken boundary.
func TestNoArchiveIsEmptyRatherThanAnError(t *testing.T) {
	e := &Engine{}
	entries, err := e.MemoryList("ws_a", "assistant", 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("nil archive: entries=%+v err=%v", entries, err)
	}
}

type contextArchive struct {
	frozenArchive
	searchCalled bool
	listCalled   bool
}

func (a *contextArchive) ArchiveContext(ctx context.Context, entry memory.Entry) error {
	return ctx.Err()
}
func (a *contextArchive) SearchContext(ctx context.Context, agentID, query string, limit int) ([]memory.Entry, error) {
	a.searchCalled = true
	return nil, ctx.Err()
}
func (a *contextArchive) ReadByScopeContext(ctx context.Context, agentID, sessionID string, scope memory.Scope, limit int) ([]memory.Entry, error) {
	return nil, ctx.Err()
}
func (a *contextArchive) ReadGlobalContext(ctx context.Context, agentID string, limit int) ([]memory.Entry, error) {
	a.listCalled = true
	return nil, ctx.Err()
}
func (a *contextArchive) PruneContext(ctx context.Context, agentID string, before time.Time) (int64, error) {
	return 0, ctx.Err()
}

var _ sdkstorage.ContextMemoryBackend = (*contextArchive)(nil)

func TestMemoryReadsPropagateCallerCancellation(t *testing.T) {
	archive := &contextArchive{}
	e := &Engine{archive: archive}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := e.MemorySearchContext(ctx, wsroot.PersonalWorkspaceID, "assistant", "note", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("search error = %v, want context canceled", err)
	}
	if _, err := e.MemoryListContext(ctx, wsroot.PersonalWorkspaceID, "assistant", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("list error = %v, want context canceled", err)
	}
	if !archive.searchCalled || !archive.listCalled {
		t.Fatalf("context surface calls: search=%v list=%v", archive.searchCalled, archive.listCalled)
	}
}
