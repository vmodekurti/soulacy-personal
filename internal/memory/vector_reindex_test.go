package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fixedDimEmbedder struct {
	dims      int
	failAfter int
	calls     int
}

func TestVectorStoreDimensionMismatchNamesReindexCommand(t *testing.T) {
	store := newVectorStoreForTest(t)
	_, err := NewVectorStore(store.db, &fixedDimEmbedder{dims: 3}, 3)
	if err == nil || !strings.Contains(err.Error(), "sy memory reindex") {
		t.Fatalf("error=%v, want actionable reindex command", err)
	}
}

func (e *fixedDimEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	e.calls++
	if e.failAfter > 0 && e.calls > e.failAfter {
		return nil, errors.New("provider interrupted")
	}
	out := make([]float32, e.dims)
	for i := range out {
		out[i] = float32(len(text)+i) / 100
	}
	return out, nil
}

func TestVectorReindexChangesDimensionsAtomically(t *testing.T) {
	store := newVectorStoreForTest(t)
	writeVector(t, store, "ws_a", "assistant", "first memory")
	writeVector(t, store, "ws_b", "assistant", "second memory")

	report, err := ReindexVectorStore(context.Background(), store.db, &fixedDimEmbedder{dims: 3}, VectorReindexOptions{
		TargetDims: 3, ModelID: "test/embed-3", BatchSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.SourceDims != 4 || report.TargetDims != 3 || report.Rows != 2 || report.Noop {
		t.Fatalf("report=%+v", report)
	}
	dims, err := VectorDimensions(context.Background(), store.db)
	if err != nil || dims != 3 {
		t.Fatalf("dimensions=%d err=%v, want 3", dims, err)
	}
	var vectors, metadata int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM memory_vectors`).Scan(&vectors); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM memory_vector_meta`).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if vectors != metadata || vectors != 2 {
		t.Fatalf("vectors=%d metadata=%d", vectors, metadata)
	}
}

func TestVectorReindexResumesAfterProviderFailure(t *testing.T) {
	store := newVectorStoreForTest(t)
	writeVector(t, store, "ws_a", "assistant", "one")
	writeVector(t, store, "ws_a", "assistant", "two")

	_, err := ReindexVectorStore(context.Background(), store.db, &fixedDimEmbedder{dims: 3, failAfter: 1}, VectorReindexOptions{
		TargetDims: 3, ModelID: "test/embed-3", BatchSize: 1,
	})
	if err == nil {
		t.Fatal("interrupted provider did not stop reindex")
	}
	var staged int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM memory_vector_reindex_stage`).Scan(&staged); err != nil || staged != 1 {
		t.Fatalf("staged=%d err=%v, want one checkpoint", staged, err)
	}

	report, err := ReindexVectorStore(context.Background(), store.db, &fixedDimEmbedder{dims: 3}, VectorReindexOptions{
		TargetDims: 3, ModelID: "test/embed-3", BatchSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Resumed || report.Rows != 2 {
		t.Fatalf("report=%+v, want resumed two-row rebuild", report)
	}
}

func TestVectorReindexConflictPreservesOldIndex(t *testing.T) {
	store := newVectorStoreForTest(t)
	writeVector(t, store, "ws_a", "assistant", "one")
	writeVector(t, store, "ws_a", "assistant", "two")
	_, _ = ReindexVectorStore(context.Background(), store.db, &fixedDimEmbedder{dims: 3, failAfter: 1}, VectorReindexOptions{
		TargetDims: 3, ModelID: "test/embed-3", BatchSize: 1,
	})

	_, err := ReindexVectorStore(context.Background(), store.db, &fixedDimEmbedder{dims: 5}, VectorReindexOptions{
		TargetDims: 5, ModelID: "test/embed-5",
	})
	if !errors.Is(err, ErrVectorReindexConflict) {
		t.Fatalf("error=%v, want conflict", err)
	}
	dims, dimErr := VectorDimensions(context.Background(), store.db)
	if dimErr != nil || dims != 4 {
		t.Fatalf("old index dimensions=%d err=%v, want intact 4", dims, dimErr)
	}
}
