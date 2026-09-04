// dlq_test.go — tests for the dead-letter queue store.
package dlq

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// NewID
// ---------------------------------------------------------------------------

func TestNewIDIsHex16Chars(t *testing.T) {
	id := NewID()
	if len(id) != 16 {
		t.Errorf("NewID len = %d, want 16", len(id))
	}
}

func TestNewIDUnique(t *testing.T) {
	a, b := NewID(), NewID()
	if a == b {
		t.Error("two NewID() calls returned the same ID")
	}
}

// ---------------------------------------------------------------------------
// NoopStore
// ---------------------------------------------------------------------------

func TestNoopStorePushReturnsNil(t *testing.T) {
	var s NoopStore
	if err := s.Push(context.Background(), DeadLetter{ID: "x", Queue: "q"}); err != nil {
		t.Errorf("NoopStore Push: %v", err)
	}
}

func TestNoopStoreListReturnsEmpty(t *testing.T) {
	var s NoopStore
	entries, err := s.List(context.Background(), "")
	if err != nil || len(entries) != 0 {
		t.Errorf("NoopStore List: entries=%v err=%v", entries, err)
	}
}

func TestNoopStoreGetReturnsErrNotFound(t *testing.T) {
	var s NoopStore
	_, err := s.Get(context.Background(), "any-id")
	if err != ErrNotFound {
		t.Errorf("NoopStore Get: err = %v, want ErrNotFound", err)
	}
}

func TestNoopStoreDeleteReturnsNil(t *testing.T) {
	var s NoopStore
	if err := s.Delete(context.Background(), "any-id"); err != nil {
		t.Errorf("NoopStore Delete: %v", err)
	}
}

func TestNoopStoreCloseReturnsNil(t *testing.T) {
	var s NoopStore
	if err := s.Close(); err != nil {
		t.Errorf("NoopStore Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SQLiteStore helpers
// ---------------------------------------------------------------------------

func newDLQStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "dlq.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ---------------------------------------------------------------------------
// NewSQLiteStore
// ---------------------------------------------------------------------------

func TestNewSQLiteStoreCreates(t *testing.T) {
	s := newDLQStore(t)
	if s == nil {
		t.Fatal("NewSQLiteStore returned nil")
	}
}

func TestNewSQLiteStoreBadPath(t *testing.T) {
	_, err := NewSQLiteStore("/no/such/dir/dlq.db")
	if err == nil {
		t.Fatal("expected error for bad path, got nil")
	}
}

// ---------------------------------------------------------------------------
// Push / Get / Delete / List
// ---------------------------------------------------------------------------

func TestPushAndGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newDLQStore(t)

	entry := DeadLetter{
		ID:       "entry-1",
		Queue:    "agent-run",
		Payload:  []byte(`{"id":"msg-1"}`),
		ErrorMsg: "timeout",
		Attempts: 3,
	}
	if err := s.Push(ctx, entry); err != nil {
		t.Fatalf("Push: %v", err)
	}

	got, err := s.Get(ctx, "entry-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "entry-1" || got.Queue != "agent-run" || got.Attempts != 3 {
		t.Errorf("Get: %+v", got)
	}
	if string(got.Payload) != `{"id":"msg-1"}` {
		t.Errorf("Payload: %s", got.Payload)
	}
	if got.ErrorMsg != "timeout" {
		t.Errorf("ErrorMsg: %q", got.ErrorMsg)
	}
}

func TestPushAutoFillsTimestamps(t *testing.T) {
	ctx := context.Background()
	s := newDLQStore(t)
	before := time.Now().UTC().Add(-time.Second)

	entry := DeadLetter{ID: "ts-check", Queue: "q", Payload: []byte("x"), ErrorMsg: "e"}
	// Zero CreatedAt/LastAttemptAt — should be auto-set.
	_ = s.Push(ctx, entry)

	got, _ := s.Get(ctx, "ts-check")
	if got.CreatedAt.Before(before) {
		t.Errorf("CreatedAt not auto-set: %v", got.CreatedAt)
	}
	if got.LastAttemptAt.Before(before) {
		t.Errorf("LastAttemptAt not auto-set: %v", got.LastAttemptAt)
	}
}

func TestPushNormalisesAttemptsLessThanOne(t *testing.T) {
	ctx := context.Background()
	s := newDLQStore(t)

	entry := DeadLetter{ID: "zero-attempts", Queue: "q", Payload: []byte("x"), ErrorMsg: "e", Attempts: 0}
	_ = s.Push(ctx, entry)

	got, _ := s.Get(ctx, "zero-attempts")
	if got.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", got.Attempts)
	}
}

func TestPushUpsertOverwrites(t *testing.T) {
	ctx := context.Background()
	s := newDLQStore(t)

	_ = s.Push(ctx, DeadLetter{ID: "dup", Queue: "q", Payload: []byte("v1"), ErrorMsg: "first", Attempts: 1})
	_ = s.Push(ctx, DeadLetter{ID: "dup", Queue: "q", Payload: []byte("v2"), ErrorMsg: "second", Attempts: 2})

	got, _ := s.Get(ctx, "dup")
	if got.ErrorMsg != "second" || got.Attempts != 2 {
		t.Errorf("upsert: %+v", got)
	}
}

func TestGetUnknownIDReturnsErrNotFound(t *testing.T) {
	s := newDLQStore(t)
	_, err := s.Get(context.Background(), "ghost")
	if err != ErrNotFound {
		t.Fatalf("Get unknown: err = %v, want ErrNotFound", err)
	}
}

func TestDeleteThenGetReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	s := newDLQStore(t)

	_ = s.Push(ctx, DeadLetter{ID: "del-me", Queue: "q", Payload: []byte("x"), ErrorMsg: "e"})
	if err := s.Delete(ctx, "del-me"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := s.Get(ctx, "del-me")
	if err != ErrNotFound {
		t.Fatalf("after delete Get: err = %v, want ErrNotFound", err)
	}
}

func TestDeleteNonExistentReturnsErrNotFound(t *testing.T) {
	s := newDLQStore(t)
	if err := s.Delete(context.Background(), "ghost"); err != ErrNotFound {
		t.Errorf("Delete non-existent: err = %v, want ErrNotFound", err)
	}
}

func TestListAllEntries(t *testing.T) {
	ctx := context.Background()
	s := newDLQStore(t)

	for i, q := range []string{"q1", "q2", "q1"} {
		_ = s.Push(ctx, DeadLetter{
			ID: string(rune('a' + i)), Queue: q,
			Payload: []byte("x"), ErrorMsg: "e",
		})
	}

	entries, err := s.List(ctx, "")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("List all count = %d, want 3", len(entries))
	}
}

func TestListFilteredByQueue(t *testing.T) {
	ctx := context.Background()
	s := newDLQStore(t)

	_ = s.Push(ctx, DeadLetter{ID: "x1", Queue: "queue-a", Payload: []byte("x"), ErrorMsg: "e"})
	_ = s.Push(ctx, DeadLetter{ID: "x2", Queue: "queue-b", Payload: []byte("x"), ErrorMsg: "e"})

	entries, err := s.List(ctx, "queue-a")
	if err != nil {
		t.Fatalf("List filtered: %v", err)
	}
	if len(entries) != 1 || entries[0].Queue != "queue-a" {
		t.Errorf("List filtered: %+v", entries)
	}
}

func TestListEmptyReturnsNil(t *testing.T) {
	s := newDLQStore(t)
	entries, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("List empty: %v", entries)
	}
}
