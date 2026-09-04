// Package sqlitex centralises the SQLite connection-string and pool tuning
// used across Soulacy (actionlog, memory archive, knowledge store).
//
// PRODUCTION_AUDIT — F3 (2026-05-27): every prior call site re-spelled its
// own ?_journal=WAL&_timeout=5000&_busy_timeout=5000 suffix, with subtle
// drift between them (one had _foreign_keys=on, others didn't), and none
// of them set _synchronous, _cache_size, or _temp_store. Under heavy
// concurrent writers (async actionlog batched flushes happening at the
// same time as a RAG ingest or a memory archive write) the missing
// synchronous=NORMAL hint forced a full fdatasync on every commit and
// drove lock contention high enough that Litestream's WAL streamer would
// occasionally see partial pages.
//
// One helper, two knobs: a connection-string builder and a pool-config
// applier. Keeps Litestream's contract intact (WAL mode is preserved),
// removes the lock-storm tail, and gives every store the same defaults so
// future stores can't accidentally regress.

package sqlitex

import (
	"database/sql"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Options describes the SQLite tuning for one database file.
//
// Defaults are tuned for the gateway's mixed workload (many short-lived
// reads, occasional batched writes from the actionlog/RAG ingest path).
// Override only if you have a measured reason.
type Options struct {
	// ForeignKeys enables foreign-key constraint enforcement. The
	// knowledge store relies on it for cascade deletes; the other stores
	// don't need it. Default: false.
	ForeignKeys bool

	// MMapSize sets the memory-mapped I/O size in bytes. Useful for
	// read-heavy stores (knowledge.db's vec0 KNN queries scan rows
	// linearly). Default: 0 (disabled — mattn driver uses SQLite default).
	MMapSize int64

	// BusyTimeout is how long a connection waits for a held lock before
	// returning SQLITE_BUSY. Wal+30s lets a batched actionlog flush and
	// a hot RAG query coexist without either returning ECANCEL.
	// Default: 30 * time.Second.
	BusyTimeout time.Duration

	// MaxOpenConns caps the database/sql pool size. SQLite serialises
	// writers via WAL, so we don't gain from raising this above the
	// peak concurrent reader count. Default: 16.
	MaxOpenConns int

	// MaxIdleConns caps the idle pool. Half of MaxOpenConns is plenty.
	// Default: 8.
	MaxIdleConns int

	// ConnMaxLifetime forces stale connections to recycle. Mostly defensive
	// against driver memory growth over long uptimes. Default: 30 minutes.
	ConnMaxLifetime time.Duration
}

// DefaultOptions returns the conservative Soulacy defaults.
func DefaultOptions() Options {
	return Options{
		BusyTimeout:     30 * time.Second,
		MaxOpenConns:    16,
		MaxIdleConns:    8,
		ConnMaxLifetime: 30 * time.Minute,
	}
}

// DSN builds a mattn/go-sqlite3 connection string. The returned value is
// safe to pass straight to sql.Open("sqlite3", ...).
//
// Knobs baked in for every caller:
//
//	_journal_mode  = WAL       — required for concurrent readers + Litestream
//	_synchronous   = NORMAL    — safe under WAL, ~5-10x faster than FULL
//	_busy_timeout  = <opts>    — wait for held locks instead of failing fast
//	_cache_size    = -20000    — 20 MiB per-conn page cache (negative = KiB)
//	_temp_store    = MEMORY    — keep temp tables out of disk swap
//
// Optional knobs (set via Options):
//
//	_foreign_keys  = on        — when opts.ForeignKeys
//	_mmap_size     = <bytes>   — when opts.MMapSize > 0
func DSN(path string, opts Options) string {
	q := url.Values{}
	q.Set("_journal_mode", "WAL")
	q.Set("_synchronous", "NORMAL")
	q.Set("_cache_size", "-20000")
	q.Set("_temp_store", "MEMORY")

	bt := opts.BusyTimeout
	if bt <= 0 {
		bt = 30 * time.Second
	}
	// mattn expects milliseconds for both _timeout and _busy_timeout. The
	// driver alias keeps both; setting both is harmless and defensive
	// against version drift.
	ms := strconv.Itoa(int(bt / time.Millisecond))
	q.Set("_timeout", ms)
	q.Set("_busy_timeout", ms)

	if opts.ForeignKeys {
		q.Set("_foreign_keys", "on")
	}
	if opts.MMapSize > 0 {
		q.Set("_mmap_size", strconv.FormatInt(opts.MMapSize, 10))
	}

	// mattn parses `?key=value&...` with `_` prefixes; url.Values encodes
	// safely. The driver does its own unescaping so spaces in `path` would
	// be a footgun — paths under ~/.soulacy/ never contain them in
	// practice, but use a simple separator to be explicit.
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + q.Encode()
}

// Tune applies the pool settings from opts to an already-opened *sql.DB.
// Call right after sql.Open() to avoid the brief window where the default
// (unbounded) pool can spawn surplus connections under a startup burst.
func Tune(db *sql.DB, opts Options) {
	if opts.MaxOpenConns <= 0 {
		opts.MaxOpenConns = 16
	}
	if opts.MaxIdleConns <= 0 || opts.MaxIdleConns > opts.MaxOpenConns {
		opts.MaxIdleConns = opts.MaxOpenConns / 2
		if opts.MaxIdleConns == 0 {
			opts.MaxIdleConns = 1
		}
	}
	if opts.ConnMaxLifetime <= 0 {
		opts.ConnMaxLifetime = 30 * time.Minute
	}
	db.SetMaxOpenConns(opts.MaxOpenConns)
	db.SetMaxIdleConns(opts.MaxIdleConns)
	db.SetConnMaxLifetime(opts.ConnMaxLifetime)
}

// Open opens a *sql.DB with the tuned DSN + pool in one call. Convenience
// wrapper most callers should use.
func Open(path string, opts Options) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", DSN(path, opts))
	if err != nil {
		return nil, err
	}
	Tune(db, opts)
	return db, nil
}
