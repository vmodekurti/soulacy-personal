// workspace_test.go — the cross-tenant isolation contract for knowledge bases,
// documents, chunks, embeddings, and ingestion jobs (MU-014).
package knowledge

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/wsroot"
)

func newWorkspaceStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "knowledge.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func makeKB(t *testing.T, store *Store, workspaceID, name string) *KB {
	t.Helper()
	kb, err := store.CreateKB(KB{
		WorkspaceID: workspaceID, Name: name, EmbeddingProvider: "test",
		EmbeddingModel: "test-model", Dim: 3,
	})
	if err != nil {
		t.Fatalf("create %s/%s: %v", workspaceID, name, err)
	}
	return kb
}

// A knowledge-base name is human-chosen, so two teams both calling one "docs"
// is expected. The old global UNIQUE(name) made the second team's create fail
// because the first had used the word.
func TestTwoWorkspacesMayUseTheSameKnowledgeBaseName(t *testing.T) {
	store, _ := newWorkspaceStore(t)
	a := makeKB(t, store, "ws_a", "docs")
	b := makeKB(t, store, "ws_b", "docs")
	if a.ID == b.ID {
		t.Fatal("two workspaces share one knowledge base")
	}

	// And each resolves its own by that name.
	gotA, err := store.GetKB("ws_a", "docs")
	if err != nil || gotA == nil || gotA.ID != a.ID {
		t.Fatalf("ws_a resolved %+v (%v)", gotA, err)
	}
	gotB, err := store.GetKB("ws_b", "docs")
	if err != nil || gotB == nil || gotB.ID != b.ID {
		t.Fatalf("ws_b resolved %+v (%v)", gotB, err)
	}
}

// A duplicate name inside one workspace is still a conflict.
func TestDuplicateNameInsideOneWorkspaceIsRejected(t *testing.T) {
	store, _ := newWorkspaceStore(t)
	makeKB(t, store, "ws_a", "docs")
	if _, err := store.CreateKB(KB{
		WorkspaceID: "ws_a", Name: "docs", EmbeddingProvider: "test",
		EmbeddingModel: "test-model", Dim: 3,
	}); err == nil {
		t.Fatal("a duplicate name inside one workspace was accepted")
	}
}

// Knowledge with no owner is knowledge every tenant can search.
func TestOperationsWithoutAWorkspaceAreRefused(t *testing.T) {
	store, _ := newWorkspaceStore(t)
	if _, err := store.CreateKB(KB{Name: "docs", EmbeddingProvider: "t", EmbeddingModel: "m", Dim: 3}); !errors.Is(err, ErrWorkspaceRequired) {
		t.Fatalf("create without a workspace = %v", err)
	}
	if _, err := store.ListKBs(""); !errors.Is(err, ErrWorkspaceRequired) {
		t.Fatalf("list without a workspace = %v", err)
	}
	if _, err := store.GetKB("", "docs"); !errors.Is(err, ErrWorkspaceRequired) {
		t.Fatalf("get without a workspace = %v", err)
	}
	if _, err := store.ListDocuments("", "kb"); !errors.Is(err, ErrWorkspaceRequired) {
		t.Fatalf("list documents without a workspace = %v", err)
	}
	if _, err := store.ListIngests("", "", 10); !errors.Is(err, ErrWorkspaceRequired) {
		t.Fatalf("list ingests without a workspace = %v", err)
	}
	if _, err := store.EnqueueIngest(IngestJob{KBName: "docs", Title: "t", SpoolPath: "/tmp/x"}); !errors.Is(err, ErrWorkspaceRequired) {
		t.Fatalf("enqueue without a workspace = %v", err)
	}
}

// A knowledge base another workspace owns must read as absent, and must not
// appear in a listing.
func TestKnowledgeBasesAreInvisibleAcrossWorkspaces(t *testing.T) {
	store, _ := newWorkspaceStore(t)
	makeKB(t, store, "ws_a", "confidential")

	got, err := store.GetKB("ws_b", "confidential")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("another workspace's knowledge base resolved: %+v", got)
	}
	listed, err := store.ListKBs("ws_b")
	if err != nil {
		t.Fatal(err)
	}
	for _, kb := range listed {
		if kb.Name == "confidential" {
			t.Fatal("another workspace's knowledge base appeared in a listing")
		}
	}
	// Deleting by a name you do not own must not remove the owner's.
	if err := store.DeleteKB("ws_b", "confidential"); err != nil {
		t.Fatalf("delete of an unowned knowledge base: %v", err)
	}
	if still, err := store.GetKB("ws_a", "confidential"); err != nil || still == nil {
		t.Fatal("another workspace's delete removed the owner's knowledge base")
	}
}

// Documents and chunks inherit ownership from the knowledge base, which was
// resolved through a workspace-scoped lookup — never from the payload.
func TestDocumentsAndChunksAreScopedToTheirKnowledgeBase(t *testing.T) {
	store, _ := newWorkspaceStore(t)
	a := makeKB(t, store, "ws_a", "docs")
	b := makeKB(t, store, "ws_b", "docs")

	doc, err := store.AddDocument(a, Document{Title: "secret plans"}, []Chunk{
		{Content: "alpha content", Vector: []float32{1, 0, 0}},
	})
	if err != nil {
		t.Fatal(err)
	}

	ownerDocs, err := store.ListDocuments("ws_a", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ownerDocs) != 1 || ownerDocs[0].ID != doc.ID {
		t.Fatalf("owner listing = %+v", ownerDocs)
	}
	// Even naming the other workspace's KB ID, the workspace predicate holds.
	foreign, err := store.ListDocuments("ws_b", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(foreign) != 0 {
		t.Fatalf("documents leaked to another workspace: %+v", foreign)
	}
	if empty, err := store.ListDocuments("ws_b", b.ID); err != nil || len(empty) != 0 {
		t.Fatalf("unexpected documents in the other workspace: %+v %v", empty, err)
	}

	// A delete naming another workspace must not remove the owner's document.
	if err := store.DeleteDocument("ws_b", a.ID, doc.ID); err != nil {
		t.Fatal(err)
	}
	if still, err := store.ListDocuments("ws_a", a.ID); err != nil || len(still) != 1 {
		t.Fatalf("another workspace's delete removed the owner's document: %+v %v", still, err)
	}
	if err := store.DeleteDocument("ws_a", a.ID, doc.ID); err != nil {
		t.Fatal(err)
	}
	if gone, err := store.ListDocuments("ws_a", a.ID); err != nil || len(gone) != 0 {
		t.Fatalf("the owner's delete did not take effect: %+v %v", gone, err)
	}
}

// Vector search resolves through the knowledge base, so a query cannot name a
// namespace: the only way to reach one is a workspace-scoped KB lookup.
func TestVectorSearchCannotReachAnotherWorkspace(t *testing.T) {
	store, _ := newWorkspaceStore(t)
	a := makeKB(t, store, "ws_a", "docs")
	b := makeKB(t, store, "ws_b", "docs")

	if _, err := store.AddDocument(a, Document{Title: "alpha"}, []Chunk{
		{Content: "alpha secret content", Vector: []float32{1, 0, 0}},
	}); err != nil {
		t.Fatal(err)
	}

	hits, err := store.Search(a, []float32{1, 0, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("the owner's own search found nothing")
	}
	// The other workspace's KB has its own vector table; the same query vector
	// returns nothing rather than the neighbour's chunk.
	foreignHits, err := store.Search(b, []float32{1, 0, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range foreignHits {
		if hit.Content == "alpha secret content" {
			t.Fatal("a vector query returned another workspace's chunk")
		}
	}
}

// Ingestion jobs carry their workspace, because a job outlives the request
// that created it and the tenant cannot be inferred when it later runs.
func TestIngestionJobsAreScoped(t *testing.T) {
	store, _ := newWorkspaceStore(t)
	makeKB(t, store, "ws_a", "docs")
	makeKB(t, store, "ws_b", "docs")

	job, err := store.EnqueueIngest(IngestJob{
		WorkspaceID: "ws_a", KBName: "docs", Title: "plans", SpoolPath: "/tmp/spool-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.WorkspaceID != "ws_a" {
		t.Fatalf("job workspace = %q", job.WorkspaceID)
	}

	mine, err := store.ListIngests("ws_a", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 {
		t.Fatalf("owner listing = %+v", mine)
	}
	theirs, err := store.ListIngests("ws_b", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(theirs) != 0 {
		t.Fatalf("another workspace saw the job: %+v", theirs)
	}
	if _, err := store.GetIngestForWorkspace("ws_b", job.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("another workspace read a job by id: %v", err)
	}
	got, err := store.GetIngestForWorkspace("ws_a", job.ID)
	if err != nil || got.ID != job.ID {
		t.Fatalf("owner could not read its job: %+v %v", got, err)
	}
}

func TestWorkspaceKnowledgeExportAndPurgeAreCompleteAndIsolated(t *testing.T) {
	store, _ := newWorkspaceStore(t)
	a := makeKB(t, store, "ws_a", "docs")
	b := makeKB(t, store, "ws_b", "docs")
	if _, err := store.AddDocument(a, Document{Title: "A plans"}, []Chunk{{Content: "alpha private", Vector: []float32{1, 0, 0}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddDocument(b, Document{Title: "B plans"}, []Chunk{{Content: "bravo private", Vector: []float32{0, 1, 0}}}); err != nil {
		t.Fatal(err)
	}

	spoolRoot := t.TempDir()
	spoolA, spoolB := filepath.Join(spoolRoot, "a.bin"), filepath.Join(spoolRoot, "b.bin")
	if err := os.WriteFile(spoolA, []byte("a upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spoolB, []byte("b upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, job := range []IngestJob{
		{ID: "job-a", WorkspaceID: "ws_a", KBName: "docs", Title: "a", SpoolPath: spoolA},
		{ID: "job-b", WorkspaceID: "ws_b", KBName: "docs", Title: "b", SpoolPath: spoolB},
	} {
		if _, err := store.EnqueueIngest(job); err != nil {
			t.Fatal(err)
		}
	}

	var exported bytes.Buffer
	count, err := store.ExportWorkspaceJSONL(context.Background(), "ws_a", &exported)
	if err != nil || count == 0 {
		t.Fatalf("export count=%d err=%v", count, err)
	}
	if !strings.Contains(exported.String(), "alpha private") || strings.Contains(exported.String(), "bravo private") {
		t.Fatalf("knowledge export crossed its workspace boundary: %s", exported.String())
	}
	if strings.Contains(exported.String(), spoolA) {
		t.Fatal("knowledge export disclosed an internal spool path")
	}
	var vectors bytes.Buffer
	vectorCount, err := store.ExportVectorMetadataJSONL(context.Background(), "ws_a", &vectors)
	if err != nil || vectorCount != 1 {
		t.Fatalf("vector metadata count=%d err=%v", vectorCount, err)
	}
	if !strings.Contains(vectors.String(), a.ID) || strings.Contains(vectors.String(), b.ID) {
		t.Fatalf("vector metadata crossed its workspace boundary: %s", vectors.String())
	}

	removed, err := store.PurgeWorkspace(context.Background(), "ws_a", spoolRoot)
	if err != nil {
		t.Fatal(err)
	}
	if removed.Rows == 0 || removed.Bytes != int64(len("a upload")) {
		t.Fatalf("unexpected purge report: %+v", removed)
	}
	if got, err := store.GetKB("ws_a", "docs"); err != nil || got != nil {
		t.Fatalf("deleted workspace knowledge survived: %+v %v", got, err)
	}
	if got, err := store.GetKB("ws_b", "docs"); err != nil || got == nil {
		t.Fatalf("other workspace knowledge was removed: %+v %v", got, err)
	}
	if _, err := os.Stat(spoolA); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted workspace spool survived: %v", err)
	}
	if _, err := os.Stat(spoolB); err != nil {
		t.Fatalf("other workspace spool was removed: %v", err)
	}
}

func TestKnowledgePurgeRefusesASpoolPathOutsideItsRoot(t *testing.T) {
	store, _ := newWorkspaceStore(t)
	outside := filepath.Join(t.TempDir(), "outside.bin")
	if err := os.WriteFile(outside, []byte("do not remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueIngest(IngestJob{
		ID: "job-outside", WorkspaceID: "ws_a", KBName: "docs", Title: "x", SpoolPath: outside,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := store.PurgeWorkspace(context.Background(), "ws_a", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "outside ingest root") {
		t.Fatalf("unsafe spool path was accepted: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("unsafe path was removed: %v", err)
	}
}

// A knowledge.db written before tenants existed keeps working: the global
// UNIQUE(name) is rebuilt away and every row is assigned to the personal
// workspace, which is what a single-user installation's knowledge was.
func TestExistingKnowledgeBaseMigratesToPersonal(t *testing.T) {
	store, path := newWorkspaceStore(t)
	kb := makeKB(t, store, wsroot.PersonalWorkspaceID, "legacy")
	if _, err := store.AddDocument(kb, Document{Title: "old doc"}, []Chunk{
		{Content: "written before tenants", Vector: []float32{0, 1, 0}},
	}); err != nil {
		t.Fatal(err)
	}
	// Simulate rows from before the column existed.
	for _, table := range []string{"knowledge_bases", "documents", "chunks"} {
		if _, err := store.db.Exec(`UPDATE ` + table + ` SET workspace_id=''`); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	got, err := reopened.GetKB(wsroot.PersonalWorkspaceID, "legacy")
	if err != nil || got == nil {
		t.Fatalf("a pre-existing knowledge base was lost: %+v %v", got, err)
	}
	docs, err := reopened.ListDocuments(wsroot.PersonalWorkspaceID, got.ID)
	if err != nil || len(docs) != 1 {
		t.Fatalf("pre-existing documents were lost: %+v %v", docs, err)
	}
}
