// isolation_test.go — the cross-tenant isolation contract for agent brain
// memory. internal/ownership/catalog.go names this file as the isolation
// evidence for internal/agentmemory/store.go and for the rulebook_versions and
// rulebook_locks tables.
//
// Every store in this package is keyed by agent ID alone, and agent IDs are
// unique within a workspace rather than across the deployment. There is no
// predicate to add — the key has no room for a tenant — so isolation is by
// root directory, one CompositeStore per workspace.
package agentmemory

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/soulacy/soulacy/internal/wsroot"
)

func newTestStores(t *testing.T) (*Stores, string) {
	t.Helper()
	base := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	stores := NewStores(base)
	t.Cleanup(func() { _ = stores.Close() })
	return stores, base
}

func storeFor(t *testing.T, stores *Stores, workspaceID string) *CompositeStore {
	t.Helper()
	store := stores.For(workspaceID)
	if store == nil {
		t.Fatalf("no brain store for workspace %q", workspaceID)
	}
	return store
}

// Episodic records are verbatim task inputs and replies, and procedural rules
// are the operating instructions a team wrote for its own agents. Two tenants
// running an agent called "researcher" must not share either.
func TestAgentsWithTheSameIDInTwoWorkspacesDoNotShareMemory(t *testing.T) {
	stores, _ := newTestStores(t)
	a := storeFor(t, stores, "ws_a")
	b := storeFor(t, stores, "ws_b")

	if err := a.Write(Record{ID: "r1", AgentID: "researcher", Type: MemoryTypeEpisodic, Content: "A's confidential task"}); err != nil {
		t.Fatal(err)
	}
	if err := a.UpdateProcedural("researcher", "# A's rules"); err != nil {
		t.Fatal(err)
	}

	records, err := b.EpisodicRecords("researcher", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("one workspace's episodic history was readable from another: %+v", records)
	}
	if rules := b.ProceduralRules("researcher"); rules != "" {
		t.Fatalf("one workspace's procedural rules were readable from another: %q", rules)
	}

	// B writing its own rules must not overwrite A's.
	if err := b.UpdateProcedural("researcher", "# B's rules"); err != nil {
		t.Fatal(err)
	}
	if rules := a.ProceduralRules("researcher"); rules != "# A's rules" {
		t.Fatalf("a neighbour's write overwrote this workspace's rules: %q", rules)
	}
}

// The rulebook lock is the sharpest edge in this package, and it is not an
// information-disclosure problem.
//
// A lock is a *control*: it refuses every write to an agent's rules, automatic
// and manual alike. Keyed on agent_id in one shared database, freezing an agent
// in one workspace would freeze the identically-named agent in every other
// workspace — and a stranger's unlock would silently thaw yours. The key of a
// lock is also its mutual-exclusion domain.
func TestARulebookLockDoesNotReachIntoAnotherWorkspace(t *testing.T) {
	stores, _ := newTestStores(t)
	a := storeFor(t, stores, "ws_a")
	b := storeFor(t, stores, "ws_b")

	if _, err := a.UpdateProceduralVersioned("researcher", "# A v1", "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.UpdateProceduralVersioned("researcher", "# B v1", "manual"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetRulebookLocked("researcher", true); err != nil {
		t.Fatal(err)
	}

	if !a.RulebookLocked("researcher") {
		t.Fatal("the lock did not take effect in its own workspace")
	}
	if b.RulebookLocked("researcher") {
		t.Fatal("one workspace's lock froze another workspace's agent")
	}
	if _, err := b.UpdateProceduralVersioned("researcher", "# B v2", "manual"); err != nil {
		t.Fatalf("a neighbour's lock refused this workspace's write: %v", err)
	}
	// The lock still holds where it was set — the test above must not pass by
	// the lock simply not working.
	if _, err := a.UpdateProceduralVersioned("researcher", "# A v2", "manual"); err != ErrRulebookLocked {
		t.Fatalf("the lock stopped working in its own workspace: %v", err)
	}

	// And an unlock by the neighbour does not release it.
	if err := b.SetRulebookLocked("researcher", false); err != nil {
		t.Fatal(err)
	}
	if !a.RulebookLocked("researcher") {
		t.Fatal("a neighbour's unlock released this workspace's lock")
	}
}

// Version numbers are per agent per workspace. A shared history would make one
// tenant's v2 the successor of another tenant's v1, and a rollback would then
// re-apply a stranger's rules as this agent's own.
func TestRulebookVersionsAndRollbackStayWithinTheWorkspace(t *testing.T) {
	stores, _ := newTestStores(t)
	a := storeFor(t, stores, "ws_a")
	b := storeFor(t, stores, "ws_b")

	if v, err := a.UpdateProceduralVersioned("researcher", "# A v1", "manual"); err != nil || v != 1 {
		t.Fatalf("A v1: version=%d err=%v", v, err)
	}
	if v, err := a.UpdateProceduralVersioned("researcher", "# A v2", "manual"); err != nil || v != 2 {
		t.Fatalf("A v2: version=%d err=%v", v, err)
	}
	v, err := b.UpdateProceduralVersioned("researcher", "# B v1", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Fatalf("the second workspace's first version was numbered %d — history is shared", v)
	}

	versions, err := b.RulebookVersions("researcher")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 {
		t.Fatalf("workspace B sees %d versions, want 1", len(versions))
	}

	// A rollback in B has nothing of A's to reach for.
	if _, err := b.RulebookVersion("researcher", 2); err == nil {
		t.Fatal("a neighbour's rulebook version was readable")
	}
	if _, err := a.RollbackProcedural("researcher", 1); err != nil {
		t.Fatal(err)
	}
	if rules := a.ProceduralRules("researcher"); rules != "# A v1" {
		t.Fatalf("rollback restored the wrong rules: %q", rules)
	}
	if rules := b.ProceduralRules("researcher"); rules != "# B v1" {
		t.Fatalf("a neighbour's rollback changed this workspace's rules: %q", rules)
	}
}

// Product invariant 7: a single-user installation's memory files do not move,
// and the two ways of naming personal resolve to one store.
func TestPersonalBrainMemoryStaysAtTheOriginalRoot(t *testing.T) {
	stores, base := newTestStores(t)

	personal := storeFor(t, stores, "")
	if err := personal.UpdateProcedural("researcher", "# personal rules"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "researcher", "procedural.md")); err != nil {
		t.Fatalf("personal rules are not at their historical path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, wsroot.NamespaceDir)); !os.IsNotExist(err) {
		t.Fatalf("a personal write created a tenant namespace directory: %v", err)
	}
	if explicit := storeFor(t, stores, wsroot.PersonalWorkspaceID); explicit != personal {
		t.Fatal("the empty and explicit personal IDs resolved to different stores")
	}

	// A tenant's rulebook database is its own file, which is what puts the lock
	// domain inside the tenant boundary.
	storeFor(t, stores, "ws_a").SetRulebookLocked("researcher", true)
	if _, err := os.Stat(filepath.Join(base, wsroot.NamespaceDir, "ws_a", "rulebook.db")); err != nil {
		t.Fatalf("the tenant's rulebook database is not under its own directory: %v", err)
	}
}

// A workspace whose directory cannot be created gets no store at all. The
// alternative — falling back to the shared base — would file one tenant's
// episodic history and rules under another's, which is worse than the feature
// being unavailable.
func TestAnUncreatableWorkspaceDirectoryYieldsNoStoreRatherThanTheSharedRoot(t *testing.T) {
	base := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	// A regular file where the namespace directory needs to be: MkdirAll of
	// any path beneath it must fail.
	if err := os.WriteFile(filepath.Join(base, wsroot.NamespaceDir), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	stores := NewStores(base)
	t.Cleanup(func() { _ = stores.Close() })

	if store := stores.For("ws_a"); store != nil {
		t.Fatal("a workspace whose directory could not be created was given a store")
	}
	// Repeated calls stay nil rather than retrying into a shared fallback.
	if store := stores.For("ws_a"); store != nil {
		t.Fatal("the second attempt fell back to a shared store")
	}
	// Personal is unaffected: its directory is the base, which exists.
	if store := stores.For(wsroot.PersonalWorkspaceID); store == nil {
		t.Fatal("personal brain memory was disabled by another workspace's failure")
	}
}

// Version assignment is a read-then-write transaction: Append reads
// MAX(version) and inserts version+1. Under a deferred transaction two
// concurrent writers for the same agent both take a read lock and then both
// try to upgrade, and SQLite fails the loser with "database is locked" instead
// of letting it wait — a spurious failure on a perfectly ordinary auto_update
// racing a manual edit.
//
// Every write must therefore either succeed with its own version number or
// fail for a real reason (a lock on the rulebook), never for a lock on the
// database.
func TestConcurrentRulebookWritesGetDistinctVersions(t *testing.T) {
	// Deliberately against RuleLog.Append rather than through
	// CompositeStore.UpdateProceduralVersioned. The composite writes
	// procedural.md first, under the ProceduralStore mutex, which staggers the
	// callers just enough to make the collision rare — a test driven through
	// that path passes with the bug present, which is worse than no test. The
	// contention belongs to Append, so that is what this exercises.
	log, err := OpenRuleLog(filepath.Join(t.TempDir(), "rulebook.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })

	const writers = 8
	const rounds = 6
	for round := 0; round < rounds; round++ {
		agentID := fmt.Sprintf("researcher-%d", round)
		var wg sync.WaitGroup
		versions := make([]int, writers)
		errs := make([]error, writers)
		start := make(chan struct{})
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				<-start
				versions[index], errs[index] = log.Append(agentID, fmt.Sprintf("# rules %d", index), "manual")
			}(i)
		}
		close(start)
		wg.Wait()

		seen := map[int]bool{}
		for index, err := range errs {
			if err != nil {
				t.Fatalf("round %d writer %d failed: %v", round, index, err)
			}
			if seen[versions[index]] {
				t.Fatalf("round %d writer %d reused version %d", round, index, versions[index])
			}
			seen[versions[index]] = true
		}
		history, err := log.Versions(agentID)
		if err != nil {
			t.Fatal(err)
		}
		if len(history) != writers {
			t.Fatalf("round %d history has %d versions, want %d", round, len(history), writers)
		}
	}
}
