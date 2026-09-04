package knowledge

import (
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func enqueue(t *testing.T, st *Store, title string) IngestJob {
	t.Helper()
	j, err := st.EnqueueIngest(IngestJob{
		KBName: "docs", Title: title, MIMEType: "text/plain",
		SpoolPath: "/tmp/spool-" + title, ByteSize: 42,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return j
}

func TestEnqueueStartsQueued(t *testing.T) {
	st := testStore(t)
	j := enqueue(t, st, "a")
	if j.Status != JobQueued || j.Attempt != 0 || j.ID == "" {
		t.Fatalf("unexpected new job: %+v", j)
	}
}

// Claiming must be FIFO, must flip the row to running, and must burn an attempt.
func TestClaimNextIsFIFOAndCountsAttempts(t *testing.T) {
	st := testStore(t)
	first := enqueue(t, st, "first")
	enqueue(t, st, "second")

	got, ok, err := st.ClaimNextIngest()
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if got.ID != first.ID {
		t.Errorf("expected FIFO (oldest first), got %q", got.Title)
	}
	if got.Status != JobRunning {
		t.Errorf("claimed job should be running, got %q", got.Status)
	}
	if got.Attempt != 1 {
		t.Errorf("claim should increment attempt, got %d", got.Attempt)
	}
	if got.StartedAt == nil {
		t.Error("claimed job should have started_at")
	}
}

// A second claimer must never get the same row (the guard against double-ingest).
func TestClaimIsExclusive(t *testing.T) {
	st := testStore(t)
	enqueue(t, st, "only")

	a, okA, _ := st.ClaimNextIngest()
	b, okB, _ := st.ClaimNextIngest()

	if !okA {
		t.Fatal("first claim should succeed")
	}
	if okB {
		t.Fatalf("second claim must find nothing, but got %q", b.ID)
	}
	if a.Status != JobRunning {
		t.Errorf("claimed = %q", a.Status)
	}
}

func TestClaimEmptyQueue(t *testing.T) {
	st := testStore(t)
	if _, ok, err := st.ClaimNextIngest(); ok || err != nil {
		t.Fatalf("empty queue: ok=%v err=%v", ok, err)
	}
}

func TestFinishDoneRecordsDocAndFullProgress(t *testing.T) {
	st := testStore(t)
	enqueue(t, st, "a")
	j, _, _ := st.ClaimNextIngest()

	done, err := st.FinishIngest(j.ID, JobDone, "doc-123", "")
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if done.Status != JobDone || done.DocID != "doc-123" {
		t.Errorf("unexpected: %+v", done)
	}
	if done.Progress != 100 {
		t.Errorf("done should be 100%%, got %d", done.Progress)
	}
	if done.EndedAt == nil || !done.Terminal() {
		t.Error("done job should be terminal with ended_at")
	}
}

// A failed job must keep the reason AND the progress it reached, so the operator
// can see how far it got.
func TestFinishFailedKeepsReasonAndStalledProgress(t *testing.T) {
	st := testStore(t)
	enqueue(t, st, "a")
	j, _, _ := st.ClaimNextIngest()
	if err := st.SetIngestProgress(j.ID, 40); err != nil {
		t.Fatal(err)
	}

	failed, err := st.FinishIngest(j.ID, JobFailed, "", "embedder unreachable")
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if failed.Status != JobFailed || failed.Error != "embedder unreachable" {
		t.Errorf("unexpected: %+v", failed)
	}
	if failed.Progress != 40 {
		t.Errorf("failed job should keep stalled progress 40, got %d", failed.Progress)
	}
}

func TestFinishRejectsNonTerminalStatus(t *testing.T) {
	st := testStore(t)
	enqueue(t, st, "a")
	j, _, _ := st.ClaimNextIngest()
	if _, err := st.FinishIngest(j.ID, JobRunning, "", ""); err == nil {
		t.Error("finishing with a non-terminal status must be rejected")
	}
}

// Retry preserves the attempt budget; the operator's retry resets it.
func TestRequeuePreservesOrResetsAttempts(t *testing.T) {
	st := testStore(t)
	enqueue(t, st, "a")
	j, _, _ := st.ClaimNextIngest() // attempt=1

	back, err := st.RequeueIngest(j.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if back.Status != JobQueued || back.Attempt != 1 {
		t.Errorf("retry should keep the attempt count: %+v", back)
	}

	reset, err := st.RequeueIngest(j.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if reset.Attempt != 0 {
		t.Errorf("operator retry should reset attempts, got %d", reset.Attempt)
	}
}

// THE restart-safety guarantee: a job left `running` by a crash is requeued,
// not lost. Without this the document silently never lands in the KB.
func TestRecoverStaleIngestsRequeuesInterruptedJobs(t *testing.T) {
	st := testStore(t)
	enqueue(t, st, "a")
	enqueue(t, st, "b")
	j1, _, _ := st.ClaimNextIngest() // simulate a crash mid-run

	n, err := st.RecoverStaleIngests()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 recovered job, got %d", n)
	}
	got, err := st.GetIngest(j1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != JobQueued {
		t.Errorf("interrupted job should be requeued, got %q", got.Status)
	}
	// Terminal jobs must NOT be resurrected.
	j2, _, _ := st.ClaimNextIngest()
	if _, err := st.FinishIngest(j2.ID, JobDone, "d", ""); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.RecoverStaleIngests(); n != 0 {
		t.Errorf("done jobs must not be requeued, recovered %d", n)
	}
}

func TestListIngestsNewestFirstAndFilteredByKB(t *testing.T) {
	st := testStore(t)
	enqueue(t, st, "a")
	enqueue(t, st, "b")
	if _, err := st.EnqueueIngest(IngestJob{KBName: "other", Title: "c", SpoolPath: "/tmp/c"}); err != nil {
		t.Fatal(err)
	}

	all, err := st.ListIngests("", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(all))
	}

	docs, err := st.ListIngests("docs", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 jobs for kb=docs, got %d", len(docs))
	}
	for _, j := range docs {
		if j.KBName != "docs" {
			t.Errorf("filter leaked kb %q", j.KBName)
		}
	}
}

func TestSetIngestProgressClamps(t *testing.T) {
	st := testStore(t)
	j := enqueue(t, st, "a")
	if err := st.SetIngestProgress(j.ID, 250); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetIngest(j.ID)
	if got.Progress != 100 {
		t.Errorf("progress should clamp to 100, got %d", got.Progress)
	}
	if err := st.SetIngestProgress(j.ID, -5); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetIngest(j.ID)
	if got.Progress != 0 {
		t.Errorf("progress should clamp to 0, got %d", got.Progress)
	}
}
