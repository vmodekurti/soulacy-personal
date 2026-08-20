// export_test.go — MU-032 criterion 2's two halves: the archive is complete,
// and where it is not, it says so.
//
// The interesting assertions here are all about ABSENCE. An export that
// quietly omits a resource class is indistinguishable from one whose workspace
// had none of it, and the customer holding the archive has no way to tell —
// which is the failure this package's shape exists to make impossible.
package workspaceexport

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/ownership"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

func staticSource(resource, filename, body string) Source {
	return Source{
		Resource: resource, Filename: filename,
		Emit: func(ctx context.Context, w io.Writer) (bool, error) {
			if body == "" {
				return false, nil
			}
			_, err := io.WriteString(w, body)
			return true, err
		},
	}
}

func runExport(t *testing.T, store *Store, workspaceID string, sources []Source) *Job {
	t.Helper()
	job, err := NewJob("export-abcdef123456", workspaceID, "Acme", "usr_alice", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(store, Options{Job: job, Sources: sources}); err != nil {
		t.Fatalf("run: %v", err)
	}
	return job
}

// THE PROPERTY. A resource class this build cannot export appears in the
// manifest by name, saying it was not exported — rather than being absent,
// which reads identically to "the workspace had none".
func TestTheManifestNamesEveryResourceClassIncludingTheOnesItCannotExport(t *testing.T) {
	store := newTestStore(t)
	job := runExport(t, store, "ws-a", []Source{staticSource("agents", "agents.yaml", "id: bot\n")})

	plan := ownership.WorkspaceExportPlan()
	if len(job.Manifest.Entries) != len(plan) {
		t.Fatalf("manifest carries %d entries for a %d-class catalog", len(job.Manifest.Entries), len(plan))
	}
	byResource := map[string]Entry{}
	for _, e := range job.Manifest.Entries {
		byResource[e.Resource] = e
	}
	for _, p := range plan {
		e, ok := byResource[p.Resource]
		if !ok {
			t.Fatalf("resource %q is in the catalog and absent from the manifest", p.Resource)
		}
		if e.Promise != p.Export {
			t.Errorf("resource %q: manifest promise %q differs from the catalog's %q", p.Resource, e.Promise, p.Export)
		}
	}
	if byResource["agents"].Status != StatusExported {
		t.Fatalf("agents status = %q, want exported", byResource["agents"].Status)
	}
	// Every other class had no Source, and says so with a reason.
	for _, e := range job.Manifest.Entries {
		if e.Resource == "agents" {
			continue
		}
		if e.Status != StatusNotExported {
			t.Errorf("resource %q status = %q, want not-exported", e.Resource, e.Status)
		}
		if strings.TrimSpace(e.Detail) == "" {
			t.Errorf("resource %q is not exported and gives no reason", e.Resource)
		}
	}
	if len(job.Manifest.Incomplete()) != len(plan)-1 {
		t.Fatalf("Incomplete() = %d, want %d", len(job.Manifest.Incomplete()), len(plan)-1)
	}
}

// "We looked and there is none" is a different fact from "we cannot export
// this", and collapsing them costs the customer the ability to tell whether
// anything is missing. It is also different from an empty file, which looks
// like a broken exporter.
func TestAnEmptyResourceIsRecordedAsEmptyRatherThanAsAZeroByteFile(t *testing.T) {
	store := newTestStore(t)
	job := runExport(t, store, "ws-a", []Source{staticSource("runs", "runs.json", "")})

	for _, e := range job.Manifest.Entries {
		if e.Resource != "runs" {
			continue
		}
		if e.Status != StatusEmpty {
			t.Fatalf("runs status = %q, want empty", e.Status)
		}
		if e.Path != "" || e.Bytes != 0 || e.SHA256 != "" {
			t.Fatalf("an empty resource carries archive metadata: %+v", e)
		}
		if !e.Status.Complete() {
			t.Fatal("empty must count as complete; the workspace had nothing to carry")
		}
		return
	}
	t.Fatal("runs is not in the manifest")
}

// One resource failing must not abandon the export. An archive with all but
// one class, and a manifest naming the exception, serves a customer better
// than no archive — provided the gap is explicit.
func TestOneFailingResourceDoesNotAbandonTheExport(t *testing.T) {
	store := newTestStore(t)
	sources := []Source{
		staticSource("agents", "agents.yaml", "id: bot\n"),
		{Resource: "runs", Filename: "runs.json", Emit: func(ctx context.Context, w io.Writer) (bool, error) {
			return false, errors.New("the run store is unreachable")
		}},
	}
	job := runExport(t, store, "ws-a", sources)

	if job.Status != JobReady {
		t.Fatalf("job status = %q, want ready despite one failed resource", job.Status)
	}
	found := false
	for _, e := range job.Manifest.Entries {
		if e.Resource != "runs" {
			continue
		}
		found = true
		if e.Status != StatusFailed {
			t.Fatalf("runs status = %q, want failed", e.Status)
		}
		if !strings.Contains(e.Detail, "unreachable") {
			t.Fatalf("the failure gives no usable reason: %q", e.Detail)
		}
	}
	if !found {
		t.Fatal("runs is not in the manifest")
	}
	if names := job.Manifest.Incomplete(); !contains(names, "runs") {
		t.Fatal("a failed resource is not reported as incomplete")
	}
}

// The archive has to be readable without unpacking it first, which means
// manifest.json comes FIRST rather than last. Written last — the shape a
// streaming exporter produces, because checksums are not known until the end —
// a consumer cannot read it without buffering the whole file.
func TestTheArchiveLeadsWithItsManifest(t *testing.T) {
	store := newTestStore(t)
	job := runExport(t, store, "ws-a", []Source{staticSource("agents", "agents.yaml", "id: bot\n")})

	file, _, err := store.Open("ws-a", job.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(gz)

	first, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != manifestFile {
		t.Fatalf("first archive member is %q, want %q", first.Name, manifestFile)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var embedded Manifest
	if err := json.Unmarshal(body, &embedded); err != nil {
		t.Fatalf("the embedded manifest is not readable JSON: %v", err)
	}
	if embedded.Checksum != job.Manifest.Checksum {
		t.Fatal("the manifest inside the archive disagrees with the one the job reports")
	}
	if embedded.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version = %q", embedded.SchemaVersion)
	}

	next, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if next.Name != "resources/agents.yaml" {
		t.Fatalf("second member is %q", next.Name)
	}
}

// The checksum answers "are these two exports the same DATA", which is what
// somebody reconciling a re-run against an archive they already hold is
// asking. Folding the timestamp in would make every export of unchanged data
// a different checksum — answering a question nobody asked, and making the
// useful one unanswerable.
func TestTheChecksumIgnoresWhenAndWhoAndTracksWhat(t *testing.T) {
	store := newTestStore(t)
	source := []Source{staticSource("agents", "agents.yaml", "id: bot\n")}

	first, _ := NewJob("export-aaaaaaaaaaaa", "ws-a", "Acme", "usr_alice", time.Now())
	if err := Run(store, Options{Job: first, Sources: source}); err != nil {
		t.Fatal(err)
	}
	second, _ := NewJob("export-bbbbbbbbbbbb", "ws-a", "Acme", "usr_bob", time.Now().Add(time.Hour))
	if err := Run(store, Options{Job: second, Sources: source}); err != nil {
		t.Fatal(err)
	}
	if first.Manifest.Checksum != second.Manifest.Checksum {
		t.Fatal("two exports of identical data produced different checksums")
	}

	changed, _ := NewJob("export-cccccccccccc", "ws-a", "Acme", "usr_alice", time.Now())
	if err := Run(store, Options{Job: changed,
		Sources: []Source{staticSource("agents", "agents.yaml", "id: other\n")}}); err != nil {
		t.Fatal(err)
	}
	if changed.Manifest.Checksum == first.Manifest.Checksum {
		t.Fatal("changing the exported data did not change the checksum")
	}
}

// Export IDs become directory names under the workspace root, so this is the
// boundary between an opaque identifier and a filesystem traversal.
func TestAnExportIDCannotEscapeTheWorkspaceDirectory(t *testing.T) {
	store := newTestStore(t)
	for _, id := range []string{"../../etc", "..", ".", "a/b", "with space", "sh0rt", strings.Repeat("x", 65)} {
		if _, err := store.dir("ws-a", id); !errors.Is(err, ErrNotFound) {
			t.Errorf("export id %q was accepted as a directory name (err=%v)", id, err)
		}
	}
	// And a legal one resolves INSIDE the workspace, which is the assertion
	// that survives a rewrite of the pattern: checking only that bad ids are
	// refused would still pass if the good path escaped too.
	dir, err := store.dir("ws-a", "export-abcdef123456")
	if err != nil {
		t.Fatal(err)
	}
	root := store.workspaceDir("ws-a")
	if rel, err := filepath.Rel(root, dir); err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("a legal export id resolved to %q, outside %q", dir, root)
	}
}

// One workspace's exports are a different DIRECTORY, not a filtered view, so a
// handler that lost its workspace reads an empty root rather than a neighbour's
// archives.
func TestAWorkspaceCannotSeeAnotherWorkspacesExports(t *testing.T) {
	store := newTestStore(t)
	source := []Source{staticSource("agents", "agents.yaml", "id: bot\n")}
	job := runExport(t, store, "ws-a", source)

	if _, err := store.Get("ws-b", job.ID, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another workspace resolved this export: %v", err)
	}
	if _, _, err := store.Open("ws-b", job.ID, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another workspace opened this archive: %v", err)
	}
	others, err := store.List("ws-b", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(others) != 0 {
		t.Fatalf("ws-b lists %d of ws-a's exports", len(others))
	}
	if mine, err := store.List("ws-a", time.Now()); err != nil || len(mine) != 1 {
		t.Fatalf("ws-a cannot list its own export: %v %d", err, len(mine))
	}
}

// An expired archive is purged but its RECORD survives, so an owner asking
// about it is told it expired rather than that it never existed. "Request
// another" is actionable; "no such export" sends them hunting for a typo in an
// id that was correct.
func TestAnExpiredExportIsReportedAsExpiredNotAsMissing(t *testing.T) {
	store := newTestStore(t)
	job := runExport(t, store, "ws-a", []Source{staticSource("agents", "agents.yaml", "id: bot\n")})

	later := job.ExpiresAt.Add(time.Minute)
	got, err := store.Get("ws-a", job.ID, later)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != JobExpired {
		t.Fatalf("status past the TTL = %q, want expired", got.Status)
	}
	if _, _, err := store.Open("ws-a", job.ID, later); !errors.Is(err, ErrExpired) {
		t.Fatalf("Open past the TTL = %v, want ErrExpired", err)
	}

	if n, err := store.PurgeExpired("ws-a", later); err != nil || n != 1 {
		t.Fatalf("PurgeExpired = %d, %v", n, err)
	}
	dir, _ := store.dir("ws-a", job.ID)
	if _, err := os.Stat(filepath.Join(dir, archiveFile)); !os.IsNotExist(err) {
		t.Fatal("the archive survived expiry")
	}
	after, err := store.Get("ws-a", job.ID, later)
	if err != nil {
		t.Fatalf("the record did not survive expiry: %v", err)
	}
	if after.Status != JobExpired {
		t.Fatalf("status after purge = %q", after.Status)
	}
}

// A Source naming a class the catalog does not carry would be perfectly quiet:
// it runs, its bytes go nowhere, and the class it was meant to fill stays
// "not-exported". An export missing a resource whose exporter somebody wrote,
// reviewed and shipped.
func TestASourceNamingAnUnknownResourceIsRefused(t *testing.T) {
	if err := ValidateSources([]Source{staticSource("agents", "a.yaml", "x")}); err != nil {
		t.Fatalf("a catalogued resource was refused: %v", err)
	}
	err := ValidateSources([]Source{staticSource("agnets", "a.yaml", "x")})
	if err == nil {
		t.Fatal("a Source naming a resource the catalog does not carry was accepted")
	}
	if !strings.Contains(err.Error(), "agnets") {
		t.Fatalf("the error does not name the offending resource: %v", err)
	}
}

// A job left "running" forever is the worst outcome to show a customer: it is
// indistinguishable from slow, so nobody retries and nobody reports it.
func TestAFailedExportRecordsThatItFailed(t *testing.T) {
	store := newTestStore(t)
	job, _ := NewJob("export-ffffffffffff", "ws-a", "Acme", "usr_alice", time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := Run(store, Options{Job: job, Ctx: ctx,
		Sources: []Source{staticSource("agents", "agents.yaml", "id: bot\n")}}); err == nil {
		t.Fatal("a cancelled run reported success")
	}
	stored, err := store.Get("ws-a", job.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != JobFailed {
		t.Fatalf("status = %q, want failed", stored.Status)
	}
	if strings.TrimSpace(stored.Error) == "" {
		t.Fatal("a failed export records no reason")
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
