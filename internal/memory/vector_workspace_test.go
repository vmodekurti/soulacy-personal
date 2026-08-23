// vector_workspace_test.go — the cross-tenant isolation contract for semantic
// (vector) memory.
//
// MU-025 names this case directly: cross-workspace learning must not occur
// "through aggregate caches or vector similarity queries". Vector search is
// the sharpest version of the problem, because the wrong fix looks like it
// works. A post-filter returns only the caller's rows, so a naive test passes
// — but the KNN budget was already spent on the neighbour's vectors, which
// means a tenant sharing an index with a busier one gets few results or none,
// and the neighbour's memories were read out of the store to decide that.
//
// The tests below are written so a post-filter fails them.
package memory

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/wsroot"
)

func TestVectorExportAndPurgeAreWorkspaceScoped(t *testing.T) {
	vs := newVectorStoreForTest(t)
	ctx := context.Background()
	writeVector(t, vs, "ws_delete", "assistant", "near delete vector")
	writeVector(t, vs, "ws_keep", "assistant", "near keep vector")

	var exported bytes.Buffer
	count, err := vs.ExportWorkspaceMetadataJSONL(ctx, "ws_delete", &exported)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || !strings.Contains(exported.String(), "ws_delete") || strings.Contains(exported.String(), "ws_keep") {
		t.Fatalf("scoped vector metadata export count=%d body=%s", count, exported.String())
	}
	if strings.Contains(exported.String(), "embedding") {
		t.Fatalf("vector export included embedding floats: %s", exported.String())
	}
	removed, err := vs.PurgeWorkspace(ctx, "ws_delete")
	if err != nil {
		t.Fatal(err)
	}
	if removed.Rows != 2 {
		t.Fatalf("removed %+v, want vector plus metadata rows", removed)
	}
	var deleted, kept int
	if err := vs.db.QueryRow(`SELECT COUNT(*) FROM memory_vector_meta WHERE workspace_id = ?`, "ws_delete").Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if err := vs.db.QueryRow(`SELECT COUNT(*) FROM memory_vector_meta WHERE workspace_id = ?`, "ws_keep").Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if deleted != 0 || kept != 1 {
		t.Fatalf("after purge deleted=%d kept=%d", deleted, kept)
	}
}

// An interrupted model migration holds derived tenant data outside the live
// vec0 table. Workspace erasure must cover that checkpoint too; otherwise a
// deleted tenant's embedding survives until an operator resumes or restarts
// the migration.
func TestVectorPurgeRemovesInterruptedReindexStaging(t *testing.T) {
	vs := newVectorStoreForTest(t)
	ctx := context.Background()
	writeVector(t, vs, "ws_delete", "assistant", "delete staged memory")
	writeVector(t, vs, "ws_keep", "assistant", "keep staged memory")

	_, err := ReindexVectorStore(ctx, vs.db, &fixedDimEmbedder{dims: 3, failAfter: 1}, VectorReindexOptions{
		TargetDims: 3, ModelID: "test/embed-3", BatchSize: 1,
	})
	if err == nil {
		t.Fatal("provider interruption did not leave a resumable checkpoint")
	}

	removed, err := vs.PurgeWorkspace(ctx, "ws_delete")
	if err != nil {
		t.Fatal(err)
	}
	if removed.Rows != 3 {
		t.Fatalf("removed %+v, want live vector, metadata, and staged embedding", removed)
	}
	var deleted, kept int
	if err := vs.db.QueryRow(`SELECT COUNT(*) FROM memory_vector_reindex_stage WHERE workspace_id = 'ws_delete'`).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if err := vs.db.QueryRow(`SELECT COUNT(*) FROM memory_vector_meta WHERE workspace_id = 'ws_keep'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if deleted != 0 || kept != 1 {
		t.Fatalf("after purge staged deleted=%d kept metadata=%d", deleted, kept)
	}
}

// axisEmbedder places content at a controlled distance from the query so the
// KNN ordering in these tests is deterministic rather than incidental.
//
// This matters more than it looks. If every vector is identical, sqlite-vec
// returns an arbitrary k of them and a post-filter implementation passes by
// luck — which would make the test below worthless. Content marked "near" sits
// almost on the query axis; "far" sits well off it but still within range.
type axisEmbedder struct{ seq int }

func (a *axisEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	switch {
	case strings.Contains(text, "near"):
		// A fan of vectors hugging the query axis. Each is strictly closer to
		// the query than anything marked "far".
		a.seq++
		return []float32{1, float32(a.seq) * 0.0001, 0, 0}, nil
	case strings.Contains(text, "far"):
		return []float32{0.80, 0.60, 0, 0}, nil
	case strings.Contains(text, "beta"):
		return []float32{0, 1, 0, 0}, nil
	default: // the query itself, and anything unmarked
		return []float32{1, 0, 0, 0}, nil
	}
}

func newVectorStoreForTest(t *testing.T) *VectorStore {
	t.Helper()
	archive := newTestArchive(t)
	vs, err := NewVectorStore(archive.DB(), &axisEmbedder{}, 4)
	if err != nil {
		t.Skipf("sqlite-vec unavailable: %v", err)
	}
	return vs
}

func writeVector(t *testing.T, vs *VectorStore, workspaceID, agentID, content string) {
	t.Helper()
	if err := vs.Write(context.Background(), Entry{
		WorkspaceID: workspaceID, AgentID: agentID, SessionID: "s1",
		Scope: ScopeAgent, Content: content, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("write %s: %v", workspaceID, err)
	}
}

// A memory with no owner is one every tenant can recall.
func TestVectorWriteWithoutAWorkspaceIsRefused(t *testing.T) {
	vs := newVectorStoreForTest(t)
	err := vs.Write(context.Background(), Entry{
		AgentID: "bot", SessionID: "s1", Scope: ScopeAgent,
		Content: "orphan", CreatedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("a vector memory was written with no workspace")
	}
	if _, err := vs.Search(context.Background(), "", "anything", 5); err == nil {
		t.Fatal("a vector search ran with no workspace")
	}
}

// The core case, written so a post-filter cannot pass it.
//
// The neighbouring tenant holds many memories that are all *closer* to the
// query than the caller's single one. With a post-filter and topK=5, the five
// nearest rows are all the neighbour's, they are discarded in Go, and the
// caller receives nothing. Only a pre-filter — where the neighbour's vectors
// were never candidates — returns the caller's own memory.
func TestANeighbouringTenantCannotConsumeTheKNNBudget(t *testing.T) {
	vs := newVectorStoreForTest(t)
	// Twenty neighbour memories, every one of them strictly nearer the query
	// than the quiet tenant's single memory.
	for i := 0; i < 20; i++ {
		writeVector(t, vs, "ws_busy", "assistant", "near neighbour memory")
	}
	writeVector(t, vs, "ws_quiet", "assistant", "far the quiet tenant's only memory")

	results, err := vs.Search(context.Background(), "ws_quiet", "query", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("the quiet tenant got %d results, want its own 1 — a busier tenant consumed the KNN budget", len(results))
	}
	if !strings.Contains(results[0].Entry.Content, "quiet tenant") {
		t.Fatalf("the quiet tenant read a neighbour's memory: %q", results[0].Entry.Content)
	}
}

// And the plain confidentiality property: a query that matches another
// tenant's content returns nothing.
func TestVectorSearchCannotReachAnotherWorkspace(t *testing.T) {
	vs := newVectorStoreForTest(t)
	writeVector(t, vs, "ws_a", "assistant", "near the acquisition target is Northwind")
	writeVector(t, vs, "ws_b", "assistant", "far unrelated")

	hits, err := vs.Search(context.Background(), "ws_b", "query", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range hits {
		if strings.Contains(hit.Entry.Content, "Northwind") {
			t.Fatalf("a vector query returned another workspace's memory: %q", hit.Entry.Content)
		}
	}
	// The owner still recalls its own.
	own, err := vs.Search(context.Background(), "ws_a", "query", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(own) == 0 {
		t.Fatal("the owning workspace lost its own memory")
	}
}

// The agent filter narrows within a workspace; it must not widen across one.
func TestVectorAgentFilterDoesNotWidenTheTenantPredicate(t *testing.T) {
	vs := newVectorStoreForTest(t)
	writeVector(t, vs, "ws_a", "shared-agent", "near owned by ws_a")
	writeVector(t, vs, "ws_b", "shared-agent", "near owned by ws_b")

	hits, err := vs.SearchFiltered(context.Background(), "ws_b", "query", 10, "shared-agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Entry.Content, "ws_b") {
		t.Fatalf("naming a shared agent crossed the tenant boundary: %+v", hits)
	}
}

// A retention sweep in one tenant must not delete another's recall.
func TestVectorPruneCannotReachAnotherWorkspace(t *testing.T) {
	vs := newVectorStoreForTest(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-48 * time.Hour)
	for _, workspaceID := range []string{"ws_a", "ws_b"} {
		if err := vs.Write(ctx, Entry{
			WorkspaceID: workspaceID, AgentID: "assistant", SessionID: "s1",
			Scope: ScopeAgent, Content: "near old memory", CreatedAt: old,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := vs.Prune(ctx, "ws_a", "assistant", time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	gone, err := vs.Search(ctx, "ws_a", "query", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 0 {
		t.Fatalf("the owner's prune did not take effect: %+v", gone)
	}
	kept, err := vs.Search(ctx, "ws_b", "query", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 {
		t.Fatal("another workspace's prune deleted this one's memory")
	}
}

// A vector index written before tenancy keeps working: rows are assigned to
// the personal workspace, which is what a single-user installation's memories
// were. Getting this wrong would not leak — the memories would simply stop
// being recallable.
func TestExistingVectorMemoriesMigrateToPersonal(t *testing.T) {
	vs := newVectorStoreForTest(t)
	ctx := context.Background()
	writeVector(t, vs, wsroot.PersonalWorkspaceID, "assistant", "near written before tenants")
	if _, err := vs.db.Exec(`UPDATE memory_vector_meta SET workspace_id = ''`); err != nil {
		t.Fatal(err)
	}
	// Re-running the schema pass is what a reopen does.
	if err := vs.ensureSchema(); err != nil {
		t.Fatal(err)
	}
	hits, err := vs.Search(ctx, wsroot.PersonalWorkspaceID, "query", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("a pre-existing vector memory became unrecallable: %+v", hits)
	}
}
