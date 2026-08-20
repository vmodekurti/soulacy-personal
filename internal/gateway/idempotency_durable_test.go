package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/apiversion"
)

func durableStore(t *testing.T) *idempotencyStore {
	t.Helper()
	db, err := OpenIdempotencyCache(filepath.Join(t.TempDir(), "idem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &idempotencyStore{records: map[string]*idempotencyRecord{}, now: time.Now, durable: db}
}

// The whole point: a retry that arrives after a restart, or on another
// replica, must replay rather than re-execute. Reopening the same file is
// both of those.
func TestAReplaySurvivesTheProcessThatStoredIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idem.db")

	first, err := OpenIdempotencyCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := first.begin("k1", "ws_a", "hash", "req-1"); err != nil || found {
		t.Fatalf("first begin: found=%v err=%v", found, err)
	}
	first.complete("k1", 201, "application/json", []byte(`{"id":"run_1"}`))
	_ = first.Close()

	second, err := OpenIdempotencyCache(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })

	replay, found, err := second.begin("k1", "ws_a", "hash", "req-2")
	if err != nil || !found {
		t.Fatalf("the retry was not replayed: found=%v err=%v — the mutation ran twice", found, err)
	}
	if replay.status != 201 || string(replay.body) != `{"id":"run_1"}` {
		t.Fatalf("replayed the wrong response: %d %q", replay.status, replay.body)
	}
	if replay.requestID != "req-1" {
		t.Errorf("original request id lost: %q", replay.requestID)
	}
}

// Every rule the in-memory store enforces has to hold in the durable one, or
// the two behave differently depending on deployment mode — which is a bug
// nobody would find until production.
func TestTheDurableStoreKeepsEveryRuleTheMemoryOneDoes(t *testing.T) {
	t.Run("a different body with the same key is refused", func(t *testing.T) {
		store := durableStore(t)
		if _, _, err := store.begin("k", "ws_a", "hash-a", "req-1"); err != nil {
			t.Fatal(err)
		}
		store.complete("k", 200, "application/json", []byte(`{}`))
		_, _, err := store.begin("k", "ws_a", "hash-b", "req-2")
		var typed *apiversion.IncompatibleError
		if !asIncompatible(err, &typed) || typed.Code != apiversion.CodeIdempotencyReus {
			t.Fatalf("err = %v, want a reuse conflict", err)
		}
	})

	t.Run("a concurrent duplicate is refused while in flight", func(t *testing.T) {
		store := durableStore(t)
		if _, _, err := store.begin("k", "ws_a", "hash", "req-1"); err != nil {
			t.Fatal(err)
		}
		_, _, err := store.begin("k", "ws_a", "hash", "req-2")
		var typed *apiversion.IncompatibleError
		if !asIncompatible(err, &typed) || typed.Code != apiversion.CodeIdempotencyBusy {
			t.Fatalf("err = %v, want a busy conflict", err)
		}
	})

	t.Run("a failed mutation is not replayed", func(t *testing.T) {
		store := durableStore(t)
		if _, _, err := store.begin("k", "ws_a", "hash", "req-1"); err != nil {
			t.Fatal(err)
		}
		store.complete("k", 500, "application/json", []byte(`{"error":"boom"}`))
		if _, found, err := store.begin("k", "ws_a", "hash", "req-2"); err != nil || found {
			t.Fatalf("a 500 was cached: found=%v err=%v", found, err)
		}
	})

	t.Run("an abandoned reservation is released", func(t *testing.T) {
		store := durableStore(t)
		if _, _, err := store.begin("k", "ws_a", "hash", "req-1"); err != nil {
			t.Fatal(err)
		}
		store.abandon("k")
		if _, found, err := store.begin("k", "ws_a", "hash", "req-2"); err != nil || found {
			t.Fatalf("the key stayed reserved: found=%v err=%v", found, err)
		}
	})

	t.Run("an expired record is re-executed", func(t *testing.T) {
		store := durableStore(t)
		if _, _, err := store.begin("k", "ws_a", "hash", "req-1"); err != nil {
			t.Fatal(err)
		}
		store.complete("k", 200, "application/json", []byte(`{}`))
		store.durable.now = func() time.Time { return time.Now().Add(2 * idempotencyTTL) }
		if _, found, err := store.begin("k", "ws_a", "hash", "req-2"); err != nil || found {
			t.Fatalf("an expired record was replayed: found=%v err=%v", found, err)
		}
	})
}

// A deleted workspace's replay records have to go with it, and only its own.
func TestPurgingAWorkspaceRemovesOnlyItsReplayRecords(t *testing.T) {
	store := durableStore(t)
	ctx := context.Background()
	if _, _, err := store.begin("k-a", "ws_a", "hash", "req"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.begin("k-b", "ws_b", "hash", "req"); err != nil {
		t.Fatal(err)
	}

	removed, err := store.durable.PurgeWorkspace(ctx, "ws_a")
	if err != nil || removed.Rows != 1 {
		t.Fatalf("purged %d err=%v", removed.Rows, err)
	}
	// ws_b's reservation is still held, so a duplicate is still refused.
	if _, _, err := store.begin("k-b", "ws_b", "hash", "req-2"); err == nil {
		t.Fatal("purging ws_a released ws_b's reservation")
	}
}

// The delegation, not just the backend.
//
// Every other test here would pass on a build where idempotencyStore ignores
// its durable field entirely, because the in-memory store enforces the same
// rules within one process — that is the whole point of them agreeing. Only
// crossing a process boundary THROUGH the store distinguishes the two.
func TestTheStoreActuallyUsesItsDurableBacking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idem.db")

	firstDB, err := OpenIdempotencyCache(path)
	if err != nil {
		t.Fatal(err)
	}
	first := &idempotencyStore{records: map[string]*idempotencyRecord{}, now: time.Now, durable: firstDB}
	if _, found, err := first.begin("k1", "ws_a", "hash", "req-1"); err != nil || found {
		t.Fatalf("first begin: found=%v err=%v", found, err)
	}
	first.complete("k1", 201, "application/json", []byte(`{"id":"run_1"}`))
	_ = firstDB.Close()

	secondDB, err := OpenIdempotencyCache(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondDB.Close() })
	// A FRESH store with an empty map: exactly what a restarted process, or a
	// second replica, has.
	second := &idempotencyStore{records: map[string]*idempotencyRecord{}, now: time.Now, durable: secondDB}

	replay, found, err := second.begin("k1", "ws_a", "hash", "req-2")
	if err != nil || !found {
		t.Fatalf("the store did not consult its durable backing: found=%v err=%v — the mutation "+
			"would run a second time", found, err)
	}
	if replay.status != 201 {
		t.Fatalf("replayed status = %d", replay.status)
	}
}

// A store with no durable backing must keep working exactly as before, or
// every deployment that cannot open the file loses idempotency entirely
// instead of falling back.
func TestWithoutDurableBackingTheMapStillWorks(t *testing.T) {
	store := newIdempotencyStore()
	if _, found, err := store.begin("k", "ws_a", "hash", "req-1"); err != nil || found {
		t.Fatalf("begin: found=%v err=%v", found, err)
	}
	store.complete("k", 200, "application/json", []byte(`{}`))
	if _, found, err := store.begin("k", "ws_a", "hash", "req-2"); err != nil || !found {
		t.Fatalf("in-memory replay broke: found=%v err=%v", found, err)
	}
}
