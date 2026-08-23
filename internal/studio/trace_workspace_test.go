// trace_workspace_test.go — the cross-tenant isolation contract for Studio
// build traces.
//
// A build trace carries the originating intent — the user's own words — and a
// full snapshot of every draft the loop produced. It was reachable by id alone
// from any request, and `Latest()` with no argument returned whichever build
// was newest across the whole deployment.
package studio

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/wsroot"
)

// The read that mattered most: with no workspace, "give me the latest build"
// handed one tenant another tenant's in-flight intent and drafts.
func TestLatestNeverReturnsAnotherWorkspacesBuild(t *testing.T) {
	st := NewBuildTraceStore(50, "")
	alpha := st.New("ws_a", "build an agent that reconciles the alpha ledger")
	beta := st.New("ws_b", "build an agent that publishes the beta changelog")

	gotA, ok := st.Latest("ws_a")
	if !ok || gotA.ID != alpha.ID {
		t.Fatalf("ws_a latest = %+v (ok=%v), want its own", gotA, ok)
	}
	gotB, ok := st.Latest("ws_b")
	if !ok || gotB.ID != beta.ID {
		t.Fatalf("ws_b latest = %+v (ok=%v), want its own", gotB, ok)
	}
	// A workspace that has never built anything gets nothing, not the newest
	// build in the deployment.
	if got, ok := st.Latest("ws_c"); ok {
		t.Fatalf("a workspace with no builds received another's trace: %+v", got.Dump())
	}
}

// A trace id another workspace owns must be indistinguishable from an id that
// never existed, so ids cannot be probed.
func TestATraceIDFromAnotherWorkspaceIsAMiss(t *testing.T) {
	st := NewBuildTraceStore(50, "")
	owned := st.New("ws_a", "build an agent that reconciles the alpha ledger")

	if got, ok := st.Get("ws_b", owned.ID); ok {
		t.Fatalf("another workspace resolved a trace it does not own: %+v", got.Dump())
	}
	if _, ok := st.Get("ws_b", "bt-does-not-exist"); ok {
		t.Fatal("an unknown id resolved")
	}
	if got, ok := st.Get("ws_a", owned.ID); !ok || got.ID != owned.ID {
		t.Fatalf("the owner could not read its own trace: %+v %v", got, ok)
	}
}

// A listing shows one tenant's builds. The intent line alone is enough to leak
// what another team is building.
func TestListingIsScopedToOneWorkspace(t *testing.T) {
	st := NewBuildTraceStore(50, "")
	st.New("ws_a", "reconcile the alpha ledger")
	st.New("ws_a", "reconcile the alpha ledger again")
	st.New("ws_b", "publish the beta changelog")

	listed := st.List("ws_a")
	if len(listed) != 2 {
		t.Fatalf("ws_a listed %d traces, want its own 2: %+v", len(listed), listed)
	}
	for _, summary := range listed {
		if summary.Intent == "publish the beta changelog" {
			t.Fatal("a listing exposed another workspace's build intent")
		}
	}
	if len(st.List("ws_c")) != 0 {
		t.Fatal("a workspace with no builds saw someone else's")
	}
}

// Product invariant 7: a single-user installation's traces stay where they
// were, and a tenant's JSONL lands in its own directory rather than beside
// them.
func TestTraceFilesAreNamespacedAndPersonalIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	st := NewBuildTraceStore(50, dir)

	personal := st.New(wsroot.PersonalWorkspaceID, "a personal build")
	tenant := st.New("ws_a", "a tenant build")
	if err := personal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tenant.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, personal.ID+".jsonl")); err != nil {
		t.Fatalf("the personal trace is not at its historical path: %v", err)
	}
	namespaced := filepath.Join(dir, wsroot.NamespaceDir, "ws_a", tenant.ID+".jsonl")
	if _, err := os.Stat(namespaced); err != nil {
		t.Fatalf("a tenant's trace was not namespaced: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, tenant.ID+".jsonl")); err == nil {
		t.Fatal("a tenant's trace was written into the personal directory")
	}
	if got := st.Dir(wsroot.PersonalWorkspaceID); got != dir {
		t.Fatalf("personal dir = %q, want %q", got, dir)
	}
	if got := st.Dir("ws_a"); got == dir {
		t.Fatal("a tenant was told its traces live in the shared root")
	}
}

// A trace read back off disk still says whose build it was, so a JSONL file
// that outlives the ring is not an anonymous record.
func TestADumpCarriesItsOwningWorkspace(t *testing.T) {
	st := NewBuildTraceStore(50, "")
	tr := st.New("ws_a", "reconcile the alpha ledger")
	if got := tr.Dump().WorkspaceID; got != "ws_a" {
		t.Fatalf("dump workspace = %q, want ws_a", got)
	}
	// An unworkspaced build is a single-tenant build, which is the personal
	// workspace — not an unowned trace.
	if got := st.New("", "a single-tenant build").Dump().WorkspaceID; got != wsroot.PersonalWorkspaceID {
		t.Fatalf("an unworkspaced build resolved to %q", got)
	}
}

// Retention stays globally bounded, so trace memory does not grow with the
// number of tenants. Eviction drops traces; it must never expose them, and it
// must not leave a workspace index pointing at a trace that is gone.
func TestGlobalEvictionKeepsPerWorkspaceIndexesConsistent(t *testing.T) {
	st := NewBuildTraceStore(2, "")
	first := st.New("ws_a", "alpha one")
	st.New("ws_b", "beta one")
	st.New("ws_b", "beta two") // evicts ws_a's only trace

	if _, ok := st.Get("ws_a", first.ID); ok {
		t.Fatal("an evicted trace was still resolvable")
	}
	if got, ok := st.Latest("ws_a"); ok {
		t.Fatalf("a workspace whose traces were all evicted got %+v", got.Dump())
	}
	if listed := st.List("ws_a"); len(listed) != 0 {
		t.Fatalf("an evicted workspace still listed traces: %+v", listed)
	}
	if listed := st.List("ws_b"); len(listed) != 2 {
		t.Fatalf("ws_b listed %d traces, want 2: %+v", len(listed), listed)
	}
}

func TestCanonicalTracePurgeRemovesOnlyItsWorkspace(t *testing.T) {
	root := t.TempDir()
	traceBase := filepath.Join(root, "logs", "studio-builds")
	st := NewBuildTraceStore(50, traceBase)
	st.SetWorkspaceLayoutRoot(root)

	alpha := st.New("ws_a", "alpha intent")
	beta := st.New("ws_b", "beta intent")
	if err := alpha.Close(); err != nil {
		t.Fatal(err)
	}
	if err := beta.Close(); err != nil {
		t.Fatal(err)
	}
	alphaPath := filepath.Join(st.Dir("ws_a"), alpha.ID+".jsonl")
	betaPath := filepath.Join(st.Dir("ws_b"), beta.ID+".jsonl")

	removed, err := st.PurgeWorkspace(context.Background(), "ws_a")
	if err != nil {
		t.Fatal(err)
	}
	if removed.Rows == 0 || removed.Bytes == 0 {
		t.Fatalf("purge reported no removed files: %+v", removed)
	}
	if _, err := os.Stat(alphaPath); !os.IsNotExist(err) {
		t.Fatalf("purged workspace trace still exists: %v", err)
	}
	if _, ok := st.Get("ws_a", alpha.ID); ok {
		t.Fatal("purged trace survived in memory")
	}
	if _, err := os.Stat(betaPath); err != nil {
		t.Fatalf("neighboring workspace trace was changed: %v", err)
	}
	if got, ok := st.Get("ws_b", beta.ID); !ok || got.ID != beta.ID {
		t.Fatal("neighboring workspace trace was removed from memory")
	}
}

func TestPersonalTracePurgeIsRefused(t *testing.T) {
	dir := t.TempDir()
	st := NewBuildTraceStore(50, dir)
	personal := st.New(wsroot.PersonalWorkspaceID, "personal intent")
	if err := personal.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, personal.ID+".jsonl")

	if _, err := st.PurgeWorkspace(context.Background(), wsroot.PersonalWorkspaceID); err == nil {
		t.Fatal("personal trace directory was accepted as a tenant purge target")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a refused personal purge changed the filesystem: %v", err)
	}
	if _, ok := st.Get(wsroot.PersonalWorkspaceID, personal.ID); !ok {
		t.Fatal("a refused personal purge removed the trace from memory")
	}
}
