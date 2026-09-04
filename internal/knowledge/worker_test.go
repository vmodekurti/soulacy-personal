package knowledge

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestWorkerBackoffIsBoundedAndExponential(t *testing.T) {
	w := NewWorker(nil, WorkerOptions{
		BaseBackoff: time.Second,
		MaxBackoff:  8 * time.Second,
		MaxAttempts: 5,
	}, nil)

	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 8 * time.Second},  // capped
		{99, 8 * time.Second}, // never unbounded
	}
	for _, tc := range cases {
		if got := w.backoff(tc.attempt); got != tc.want {
			t.Errorf("backoff(attempt=%d) = %s, want %s", tc.attempt, got, tc.want)
		}
	}
}

func TestWorkerOptionDefaults(t *testing.T) {
	o := WorkerOptions{}.withDefaults()
	if o.MaxAttempts != DefaultMaxAttempts || o.PollInterval <= 0 || o.BaseBackoff <= 0 || o.MaxBackoff <= 0 || o.MaxDocumentBytes != DefaultMaxDocumentBytes {
		t.Fatalf("zero-value options must get sane defaults: %+v", o)
	}
}

func TestWorkerRejectsOversizedJobBeforeReadingSpool(t *testing.T) {
	w := NewWorker(nil, WorkerOptions{MaxDocumentBytes: 4}, nil)
	_, err := w.run(context.Background(), IngestJob{
		ID:        "j-big",
		SpoolPath: "/does/not/matter",
		ByteSize:  5,
	})
	if err == nil {
		t.Fatal("expected oversized job to fail")
	}
	if !strings.Contains(err.Error(), "knowledge.max_document_bytes") {
		t.Fatalf("error should explain the configured limit, got %v", err)
	}
}

// Nudge must never block, even when nobody is listening — it's called on the
// hot path of an upload request.
func TestWorkerNudgeNeverBlocks(t *testing.T) {
	w := NewWorker(nil, WorkerOptions{}, nil)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			w.Nudge()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Nudge blocked — it must be fire-and-forget")
	}
}

// A worker with no service must not panic or spin.
func TestWorkerStartWithNoServiceIsSafe(t *testing.T) {
	w := NewWorker(nil, WorkerOptions{}, nil)
	w.Start(context.TODO()) // must simply return
}

type fakeSink struct{ jobs []IngestJob }

func (f *fakeSink) IngestProgress(j IngestJob) { f.jobs = append(f.jobs, j) }

func TestWorkerEmitsProgressToSink(t *testing.T) {
	w := NewWorker(nil, WorkerOptions{}, nil)
	sink := &fakeSink{}
	w.SetProgressSink(sink)
	w.emit(IngestJob{ID: "j1", Status: JobRunning, Progress: 50})
	if len(sink.jobs) != 1 || sink.jobs[0].Progress != 50 {
		t.Fatalf("progress sink did not receive the update: %+v", sink.jobs)
	}
}
