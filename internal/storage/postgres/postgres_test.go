// postgres_test.go — parity smoke tests for the PostgreSQL storage backend
// (Story TEST-2). These mirror the SQLite memory-archive test cases
// (internal/memory/sqlite_test.go) against the Postgres MemoryStore and
// ActionLog so both backends are exercised through the same scenarios:
// CRUD on memories, scope/global retrieval, duplicate-ID idempotency,
// Prune, the ActionLog append→tail round-trip, and a schema-migration
// (DDL bootstrap) round-trip.
//
// There is no Postgres server in CI's default lane or in the local sandbox.
// Every test therefore SKIPS cleanly unless a DSN is provided via the
// SOULACY_TEST_POSTGRES_DSN environment variable. With the env var set
// (e.g. against a CI service container or a local docker postgres) the full
// suite runs. Without it, `go test` reports ok with skips, and the package
// still compiles and vets.
package postgres

import (
	"context"
	"fmt"
	"github.com/soulacy/soulacy/internal/wsroot"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap/zaptest"

	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/pkg/message"
)

// dsnEnvVar is the environment variable that supplies the Postgres connection
// string. When unset/empty, all tests in this file skip.
const dsnEnvVar = "SOULACY_TEST_POSTGRES_DSN"

var (
	containerOnce sync.Once
	containerDSN  string
	containerErr  error
	pgContainer   *tcpostgres.PostgresContainer
)

func TestMain(m *testing.M) {
	code := m.Run()
	if pgContainer != nil {
		_ = testcontainers.TerminateContainer(pgContainer)
	}
	os.Exit(code)
}

func integrationDSN(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv(dsnEnvVar); dsn != "" {
		return dsn
	}
	containerOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		pgContainer, containerErr = runPostgresContainer(ctx,
			tcpostgres.WithDatabase("soulacy_test"),
			tcpostgres.WithUsername("soulacy"),
			tcpostgres.WithPassword("soulacy-test-only"),
			testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)),
		)
		if containerErr == nil {
			containerDSN, containerErr = pgContainer.ConnectionString(ctx, "sslmode=disable")
		}
	})
	if containerErr != nil {
		t.Skipf("POSTGRES INTEGRATION SKIPPED LOUDLY: Docker/testcontainer unavailable: %v", containerErr)
	}
	return containerDSN
}

func runPostgresContainer(ctx context.Context, opts ...testcontainers.ContainerCustomizer) (container *tcpostgres.PostgresContainer, err error) {
	// testcontainers panics while discovering Docker on some desktop setups.
	// Integration unavailability is a loud skip, never a unit-test process crash.
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("Docker discovery failed: %v", recovered)
		}
	}()
	return tcpostgres.Run(ctx, "postgres:16-alpine", opts...)
}

// pgSeq generates unique memory-entry IDs across the test binary lifetime.
var pgSeq int64

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// testStores opens the Postgres backend for a single test, bootstrapping the
// schema (the migration/DDL round-trip) and truncating the core tables so each
// test starts from a clean slate. It skips the test when no DSN is configured.
func testStores(t *testing.T) (*ActionLog, *MemoryStore, *pgxpool.Pool) {
	t.Helper()

	dsn := integrationDSN(t)

	log := zaptest.NewLogger(t)

	// Open runs ddlStatements (CREATE TABLE/INDEX IF NOT EXISTS) — this is the
	// schema-migration round-trip: a fresh database is brought up to the
	// current schema, and a second Open against the same database is a no-op.
	al, ms, pool, err := Open(dsn, t.TempDir(), log)
	if err != nil {
		t.Fatalf("Open(%s): %v", dsnEnvVar, err)
	}
	t.Cleanup(func() {
		_ = al.Close() // closes the pool too
	})

	// Clean slate: remove any rows left by a prior run/test so assertions on
	// row counts are deterministic regardless of database reuse.
	truncate(t, pool)

	return al, ms, pool
}

// truncate empties the core tables. It tolerates a missing table (first run)
// by ignoring the error — Open will have created them anyway.
func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, tbl := range []string{"memories", "agent_events"} {
		if _, err := pool.Exec(ctx, "TRUNCATE TABLE "+tbl); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
}

// archive inserts a memory entry with a unique ID and returns it.
func archive(t *testing.T, m *MemoryStore, agentID, sessionID string, scope memory.Scope, content string) memory.Entry {
	t.Helper()
	seq := atomic.AddInt64(&pgSeq, 1)
	e := memory.Entry{WorkspaceID: wsroot.PersonalWorkspaceID,
		ID:        fmt.Sprintf("pg-test-%d", seq),
		AgentID:   agentID,
		SessionID: sessionID,
		Scope:     scope,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	if err := m.Archive(e); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	return e
}

// ---------------------------------------------------------------------------
// MemoryStore — Archive / Search (mirrors SQLite Write/Search)
// ---------------------------------------------------------------------------

func TestPostgresArchiveWriteAndSearch(t *testing.T) {
	_, m, _ := testStores(t)
	archive(t, m, "ag", "s1", memory.ScopeSession, "the cat sat on the mat")
	archive(t, m, "ag", "s2", memory.ScopeSession, "the dog barked")

	results, err := m.Search("ag", "cat", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("search 'cat' count = %d, want 1", len(results))
	}
	if results[0].Content != "the cat sat on the mat" {
		t.Errorf("content = %q", results[0].Content)
	}
}

func TestPostgresSearchMultipleHits(t *testing.T) {
	_, m, _ := testStores(t)
	archive(t, m, "ag", "s1", memory.ScopeSession, "python tutorial")
	archive(t, m, "ag", "s2", memory.ScopeSession, "python cookbook")
	archive(t, m, "ag", "s3", memory.ScopeSession, "go programming")

	results, err := m.Search("ag", "python", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("search 'python' count = %d, want 2", len(results))
	}
}

func TestPostgresSearchRespectsLimit(t *testing.T) {
	_, m, _ := testStores(t)
	for i := 0; i < 5; i++ {
		archive(t, m, "ag", "s1", memory.ScopeSession, "match keyword here")
	}

	results, err := m.Search("ag", "match", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("search with limit 3 returned %d results", len(results))
	}
}

func TestPostgresSearchWrongAgent(t *testing.T) {
	_, m, _ := testStores(t)
	archive(t, m, "agent-a", "s1", memory.ScopeSession, "secret data")

	results, err := m.Search("agent-b", "secret", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for wrong agent, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// MemoryStore — ReadByScope
// ---------------------------------------------------------------------------

func TestPostgresReadByScope(t *testing.T) {
	_, m, _ := testStores(t)
	archive(t, m, "ag", "s1", memory.ScopeSession, "session content")
	archive(t, m, "ag", "s1", memory.ScopeGlobal, "global content")
	archive(t, m, "ag", "s2", memory.ScopeSession, "other session")

	results, err := m.ReadByScope("ag", "s1", memory.ScopeSession, 10)
	if err != nil {
		t.Fatalf("ReadByScope: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("ReadByScope count = %d, want 1", len(results))
	}
	if results[0].Content != "session content" {
		t.Errorf("content = %q", results[0].Content)
	}
}

// ---------------------------------------------------------------------------
// MemoryStore — ReadGlobal
// ---------------------------------------------------------------------------

func TestPostgresReadGlobal(t *testing.T) {
	_, m, _ := testStores(t)
	archive(t, m, "ag", "s1", memory.ScopeSession, "session one")
	archive(t, m, "ag", "s2", memory.ScopeSession, "session two")
	archive(t, m, "ag", "s3", memory.ScopeGlobal, "global one")
	archive(t, m, "other", "s1", memory.ScopeSession, "different agent")

	results, err := m.ReadGlobal("ag", 10)
	if err != nil {
		t.Fatalf("ReadGlobal: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("ReadGlobal count = %d, want 3", len(results))
	}
	for _, r := range results {
		if r.AgentID != "ag" {
			t.Errorf("unexpected agentID %q in ReadGlobal results", r.AgentID)
		}
	}
}

// ---------------------------------------------------------------------------
// MemoryStore — duplicate ID idempotency (ON CONFLICT DO NOTHING)
// ---------------------------------------------------------------------------

func TestPostgresDuplicateIDIsIgnored(t *testing.T) {
	_, m, _ := testStores(t)
	e := archive(t, m, "ag", "s1", memory.ScopeSession, "original content")

	// Archive again with the same ID but different content.
	e.Content = "modified content"
	if err := m.Archive(e); err != nil {
		t.Fatalf("second Archive: %v", err)
	}

	results, err := m.Search("ag", "content", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 row after duplicate insert, got %d", len(results))
	}
	// ON CONFLICT DO NOTHING preserves the original row.
	if results[0].Content != "original content" {
		t.Errorf("content = %q, want 'original content'", results[0].Content)
	}
}

// ---------------------------------------------------------------------------
// MemoryStore — metadata round-trip (Postgres JSONB column)
// ---------------------------------------------------------------------------

func TestPostgresMetadataRoundTrip(t *testing.T) {
	_, m, _ := testStores(t)
	seq := atomic.AddInt64(&pgSeq, 1)
	want := memory.Entry{WorkspaceID: wsroot.PersonalWorkspaceID,
		ID:        fmt.Sprintf("pg-meta-%d", seq),
		AgentID:   "ag",
		SessionID: "s1",
		Scope:     memory.ScopeSession,
		Key:       "fav-color",
		Content:   "metadata roundtrip body",
		Metadata:  map[string]string{"source": "unit-test", "k": "v"},
		CreatedAt: time.Now().UTC(),
	}
	if err := m.Archive(want); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	results, err := m.Search("ag", "roundtrip", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("count = %d, want 1", len(results))
	}
	got := results[0]
	if got.Key != want.Key {
		t.Errorf("key = %q, want %q", got.Key, want.Key)
	}
	if got.Metadata["source"] != "unit-test" || got.Metadata["k"] != "v" {
		t.Errorf("metadata = %#v, want %#v", got.Metadata, want.Metadata)
	}
}

// ---------------------------------------------------------------------------
// MemoryStore — Prune (delete)
// ---------------------------------------------------------------------------

func TestPostgresPrune(t *testing.T) {
	_, m, _ := testStores(t)

	seq := atomic.AddInt64(&pgSeq, 1)
	old := memory.Entry{WorkspaceID: wsroot.PersonalWorkspaceID,
		ID:        fmt.Sprintf("pg-old-%d", seq),
		AgentID:   "ag",
		SessionID: "s1",
		Scope:     memory.ScopeSession,
		Content:   "old entry",
		CreatedAt: time.Now().Add(-2 * time.Hour).UTC(),
	}
	if err := m.Archive(old); err != nil {
		t.Fatalf("Archive old: %v", err)
	}
	archive(t, m, "ag", "s1", memory.ScopeSession, "recent entry")

	cutoff := time.Now().Add(-time.Hour).UTC()
	n, err := m.Prune("ag", cutoff)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Errorf("Prune deleted %d rows, want 1", n)
	}

	remaining, err := m.ReadGlobal("ag", 10)
	if err != nil {
		t.Fatalf("ReadGlobal after prune: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("after prune count = %d, want 1", len(remaining))
	}
	if remaining[0].Content != "recent entry" {
		t.Errorf("surviving content = %q, want 'recent entry'", remaining[0].Content)
	}
}

func TestPostgresPruneWrongAgent(t *testing.T) {
	_, m, _ := testStores(t)
	seq := atomic.AddInt64(&pgSeq, 1)
	old := memory.Entry{WorkspaceID: wsroot.PersonalWorkspaceID,
		ID:        fmt.Sprintf("pg-other-old-%d", seq),
		AgentID:   "other-ag",
		SessionID: "s1",
		Scope:     memory.ScopeSession,
		Content:   "other agent old data",
		CreatedAt: time.Now().Add(-2 * time.Hour).UTC(),
	}
	if err := m.Archive(old); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	n, err := m.Prune("ag", time.Now().UTC()) // prune "ag", not "other-ag"
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 0 {
		t.Errorf("Prune deleted %d rows from wrong agent, want 0", n)
	}

	results, err := m.ReadGlobal("other-ag", 10)
	if err != nil {
		t.Fatalf("ReadGlobal: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("other-ag entry was unexpectedly removed")
	}
}

// ---------------------------------------------------------------------------
// ActionLog — append → tail round-trip
// ---------------------------------------------------------------------------

// TestPostgresActionLogAppendTail exercises the async writer: events are
// enqueued via Append, flushed by the background goroutine on Close, and read
// back from the per-agent mirror file via Tail.
func TestPostgresActionLogAppendTail(t *testing.T) {
	al, _, _ := testStores(t)

	for i := 0; i < 3; i++ {
		al.Append(message.Event{
			AgentID:   "ag",
			SessionID: "s1",
			Type:      "message.in",
			Payload:   map[string]any{"n": i},
			Timestamp: time.Now().UTC(),
		})
	}

	// Flushing is async; poll Tail briefly until the events appear (or give up).
	var events []message.Event
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		events, err = al.Tail("ag", 100)
		if err != nil {
			t.Fatalf("Tail: %v", err)
		}
		if len(events) >= 3 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(events) != 3 {
		t.Fatalf("Tail count = %d, want 3", len(events))
	}
	for _, ev := range events {
		if ev.AgentID != "ag" || ev.Type != "message.in" {
			t.Errorf("unexpected event %+v", ev)
		}
	}
}

// TestPostgresActionLogTailEmpty verifies Tail on an unknown agent returns an
// empty slice (not an error) — matches the SQLite backend's behaviour.
func TestPostgresActionLogTailEmpty(t *testing.T) {
	al, _, _ := testStores(t)
	events, err := al.Tail("never-seen", 100)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("Tail on unknown agent returned %d events, want 0", len(events))
	}
}

// ---------------------------------------------------------------------------
// Schema migration round-trip
// ---------------------------------------------------------------------------

// TestPostgresMigrationRoundTrip verifies the DDL bootstrap is idempotent:
// running every ddlStatement a second time against an already-initialised
// database succeeds (CREATE ... IF NOT EXISTS), which is what makes Open safe
// to call on every process start. This is the migration round-trip for the
// schemaless-bootstrap design documented in the package header.
func TestPostgresMigrationRoundTrip(t *testing.T) {
	_, _, pool := testStores(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Re-apply all DDL statements; each must be a no-op on the existing schema.
	for _, ddl := range ddlStatements {
		if _, err := pool.Exec(ctx, ddl); err != nil {
			t.Fatalf("re-applying DDL failed (not idempotent): %v\nstmt: %s", err, ddl)
		}
	}

	// Sanity: the expected tables exist and are queryable after re-migration.
	for _, tbl := range []string{"agent_events", "memories"} {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				 WHERE table_name = $1
			)`, tbl).Scan(&exists)
		if err != nil {
			t.Fatalf("table-exists check for %s: %v", tbl, err)
		}
		if !exists {
			t.Errorf("table %s missing after migration round-trip", tbl)
		}
	}
}

func TestPostgresConcurrentIngestClaimsAreUnique(t *testing.T) {
	_, _, pool := testStores(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS test_ingest_jobs`)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS test_ingest_jobs`) })
	_, err := pool.Exec(ctx, `CREATE TABLE test_ingest_jobs (
		id BIGSERIAL PRIMARY KEY, status TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`)
	if err != nil {
		t.Fatal(err)
	}
	const jobs = 24
	if _, err := pool.Exec(ctx, `INSERT INTO test_ingest_jobs(status) SELECT 'queued' FROM generate_series(1,$1)`, jobs); err != nil {
		t.Fatal(err)
	}

	claimed := make(chan int64, jobs)
	errs := make(chan error, jobs)
	var wg sync.WaitGroup
	for i := 0; i < jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var id int64
			err := pool.QueryRow(ctx, `
				WITH next AS (
					SELECT id FROM test_ingest_jobs WHERE status='queued'
					ORDER BY created_at, id FOR UPDATE SKIP LOCKED LIMIT 1
				)
				UPDATE test_ingest_jobs j SET status='running' FROM next
				WHERE j.id=next.id RETURNING j.id`).Scan(&id)
			if err != nil {
				errs <- err
				return
			}
			claimed <- id
		}()
	}
	wg.Wait()
	close(errs)
	close(claimed)
	for err := range errs {
		t.Errorf("concurrent claim: %v", err)
	}
	seen := map[int64]bool{}
	for id := range claimed {
		if seen[id] {
			t.Errorf("job %d claimed more than once", id)
		}
		seen[id] = true
	}
	if len(seen) != jobs {
		t.Fatalf("unique claims = %d, want %d", len(seen), jobs)
	}
}

func TestPostgresRollbackLeavesNoPartialState(t *testing.T) {
	_, _, pool := testStores(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO memories(id,agent_id,scope,provenance,content) VALUES('rollback-probe','agent','session','','first')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO memories(id,agent_id,scope,provenance,content) VALUES('rollback-probe','agent','session','','duplicate')`); err == nil {
		t.Fatal("expected duplicate-key failure")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM memories WHERE id='rollback-probe'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed transaction left %d partial rows", count)
	}
}
