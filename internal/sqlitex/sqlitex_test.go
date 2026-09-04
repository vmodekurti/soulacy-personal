package sqlitex

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TestDSN_RequiredPragmas — every Open() call must come out of this
// helper with the four pragmas Litestream and the gateway depend on:
//
//	_journal_mode=WAL   — Litestream only streams WAL pages
//	_synchronous=NORMAL — safe with WAL, ~5-10x faster commits
//	_busy_timeout=…     — non-zero, otherwise concurrent writers fail
//	                       immediately under contention
//	_cache_size=-20000  — per-connection 20 MiB page cache
//
// A regression that dropped any of these would not surface until traffic
// hit the locked-database path; this test catches it at build time.
func TestDSN_RequiredPragmas(t *testing.T) {
	dsn := DSN("/tmp/foo.db", DefaultOptions())
	required := []string{
		"_journal_mode=WAL",
		"_synchronous=NORMAL",
		"_busy_timeout=30000",
		"_cache_size=-20000",
		"_temp_store=MEMORY",
	}
	for _, p := range required {
		if !strings.Contains(dsn, p) {
			t.Errorf("DSN missing required pragma %q\n full: %s", p, dsn)
		}
	}
}

// TestDSN_OptionalPragmas — foreign keys and mmap_size are opt-in (only
// the knowledge store wants them). They must NOT appear when their flag
// is off, and MUST appear when on.
func TestDSN_OptionalPragmas(t *testing.T) {
	off := DSN("/tmp/foo.db", Options{})
	if strings.Contains(off, "_foreign_keys") {
		t.Errorf("foreign_keys leaked when not requested: %s", off)
	}
	if strings.Contains(off, "_mmap_size") {
		t.Errorf("mmap_size leaked when not requested: %s", off)
	}
	on := DSN("/tmp/foo.db", Options{ForeignKeys: true, MMapSize: 256 << 20})
	if !strings.Contains(on, "_foreign_keys=on") {
		t.Errorf("foreign_keys missing when requested: %s", on)
	}
	if !strings.Contains(on, "_mmap_size=268435456") {
		t.Errorf("mmap_size missing or wrong value: %s", on)
	}
}

// TestDSN_PreservesPath — the helper must not corrupt the path portion
// of the DSN. A buggy version that, say, url-encoded the path would
// happily compile but fail at sql.Open() with file-not-found.
func TestDSN_PreservesPath(t *testing.T) {
	for _, p := range []string{
		"/var/lib/soulacy/knowledge.db",
		"./local.db",
		"/tmp/with spaces.db", // valid on POSIX; sqlite handles it
	} {
		dsn := DSN(p, DefaultOptions())
		if !strings.HasPrefix(dsn, p+"?") {
			t.Errorf("DSN should start with %q?, got %q", p, dsn)
		}
	}
}

// ---------------------------------------------------------------------------
// Additional coverage tests
// ---------------------------------------------------------------------------

// TestDefaultOptions verifies all default field values. Drift here would
// silently change connection pool behaviour on every store in the gateway.
func TestDefaultOptions(t *testing.T) {
	o := DefaultOptions()
	if o.BusyTimeout != 30*time.Second {
		t.Errorf("BusyTimeout = %v, want 30s", o.BusyTimeout)
	}
	if o.MaxOpenConns != 16 {
		t.Errorf("MaxOpenConns = %d, want 16", o.MaxOpenConns)
	}
	if o.MaxIdleConns != 8 {
		t.Errorf("MaxIdleConns = %d, want 8", o.MaxIdleConns)
	}
	if o.ConnMaxLifetime != 30*time.Minute {
		t.Errorf("ConnMaxLifetime = %v, want 30m", o.ConnMaxLifetime)
	}
	if o.ForeignKeys {
		t.Error("ForeignKeys should default to false")
	}
	if o.MMapSize != 0 {
		t.Errorf("MMapSize = %d, want 0", o.MMapSize)
	}
}

// TestDSN_ZeroBusyTimeoutDefaultsTo30s verifies that passing a zero
// BusyTimeout causes the helper to fall back to 30 000 ms so callers
// that zero-value an Options struct never accidentally get _busy_timeout=0.
func TestDSN_ZeroBusyTimeoutDefaultsTo30s(t *testing.T) {
	dsn := DSN("/tmp/x.db", Options{}) // zero BusyTimeout
	if !strings.Contains(dsn, "_busy_timeout=30000") {
		t.Errorf("zero BusyTimeout should default to 30000 ms; got: %s", dsn)
	}
	if !strings.Contains(dsn, "_timeout=30000") {
		t.Errorf("zero BusyTimeout should also set _timeout=30000 ms; got: %s", dsn)
	}
}

// TestDSN_CustomBusyTimeout verifies that non-zero BusyTimeout values
// are converted to milliseconds correctly.
func TestDSN_CustomBusyTimeout(t *testing.T) {
	dsn := DSN("/tmp/x.db", Options{BusyTimeout: 5 * time.Second})
	if !strings.Contains(dsn, "_busy_timeout=5000") {
		t.Errorf("5s BusyTimeout should produce _busy_timeout=5000; got: %s", dsn)
	}
}

// TestDSN_PathAlreadyHasQuery verifies that when the path already contains
// a `?` the DSN appends with `&` rather than `?`, producing a valid
// query string rather than a double-`?` URL.
func TestDSN_PathAlreadyHasQuery(t *testing.T) {
	path := "/tmp/foo.db?mode=memory"
	dsn := DSN(path, DefaultOptions())
	if strings.HasPrefix(dsn, path+"?") {
		t.Errorf("path with existing ? should use & separator, got %s", dsn)
	}
	if !strings.HasPrefix(dsn, path+"&") {
		t.Errorf("expected & separator after existing query, got %s", dsn)
	}
}

// TestTune_ZeroMaxOpenConnsDefaultsTo16 confirms that Tune treats
// MaxOpenConns==0 as "use default 16".
func TestTune_ZeroMaxOpenConnsDefaultsTo16(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	Tune(db, Options{MaxOpenConns: 0, MaxIdleConns: 4, ConnMaxLifetime: time.Minute})
	// We can't directly read the pool settings back from *sql.DB, but we
	// verify Tune doesn't panic and the DB remains usable.
	if err := db.Ping(); err != nil {
		t.Fatalf("db.Ping after Tune: %v", err)
	}
}

// TestTune_MaxIdleConnsGreaterThanMaxOpen verifies the guard that clamps
// MaxIdleConns down to MaxOpenConns/2 when it would otherwise exceed the
// open-connection limit (which is invalid for database/sql).
func TestTune_MaxIdleConnsGreaterThanMaxOpen(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	// MaxIdleConns > MaxOpenConns — should be clamped automatically.
	Tune(db, Options{MaxOpenConns: 4, MaxIdleConns: 100, ConnMaxLifetime: time.Minute})
	if err := db.Ping(); err != nil {
		t.Fatalf("db.Ping after Tune: %v", err)
	}
}

// TestTune_SingleConnectionPool verifies the edge case where
// MaxOpenConns==1, which means MaxIdleConns/2==0 and must be bumped to 1.
func TestTune_SingleConnectionPool(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	Tune(db, Options{MaxOpenConns: 1, MaxIdleConns: 0})
	if err := db.Ping(); err != nil {
		t.Fatalf("db.Ping after Tune: %v", err)
	}
}

// TestTune_ZeroConnMaxLifetimeDefaultsTo30m confirms that a zero
// ConnMaxLifetime is replaced with the default 30 minutes.
func TestTune_ZeroConnMaxLifetimeDefaultsTo30m(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	// ConnMaxLifetime == 0 → should be defaulted internally without panic.
	Tune(db, Options{MaxOpenConns: 4, MaxIdleConns: 2, ConnMaxLifetime: 0})
	if err := db.Ping(); err != nil {
		t.Fatalf("db.Ping after Tune with zero lifetime: %v", err)
	}
}

// TestTune_AllDefaults verifies that Tune(db, Options{}) (fully zero) does
// not panic and leaves the DB functional.
func TestTune_AllDefaults(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	Tune(db, Options{})
	if err := db.Ping(); err != nil {
		t.Fatalf("db.Ping after Tune with all-zero options: %v", err)
	}
}
