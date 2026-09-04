// Package postgres implements storage.ActionLogBackend and storage.MemoryBackend
// on top of PostgreSQL using pgx/v5 connection pools.
//
// Schema bootstrap: tables and indexes are created with IF NOT EXISTS on the
// first connection. No separate migration tool is needed for the initial
// deployment. When schema evolution is required, add a migration tool (goose,
// atlas, etc.) on top of this package.
//
// Per-agent log files: the Postgres ActionLog still writes a per-agent <id>.log
// file alongside each batch flush, keeping the HTTP tail handler's range-request
// serving path working without any changes.
//
// Async writer: like the SQLite backend, Append never blocks the caller. Events
// are buffered in a channel and flushed in batches by a background goroutine.
package postgres

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/storage"
	"github.com/soulacy/soulacy/pkg/message"
)

// compile-time interface checks
var _ storage.ActionLogBackend = (*ActionLog)(nil)
var _ storage.MemoryBackend = (*MemoryStore)(nil)

// ddlStatements are executed once at Open() to ensure the schema exists.
var ddlStatements = []string{
	`CREATE TABLE IF NOT EXISTS agent_events (
		id         BIGSERIAL    PRIMARY KEY,
		agent_id   TEXT         NOT NULL,
		session_id TEXT         NOT NULL DEFAULT '',
		type       TEXT         NOT NULL,
		payload    JSONB,
		created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_events_agent_created
		ON agent_events (agent_id, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_events_session
		ON agent_events (agent_id, session_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_events_type
		ON agent_events (type, created_at DESC)`,
	`CREATE TABLE IF NOT EXISTS memories (
		id         TEXT         PRIMARY KEY,
		agent_id   TEXT         NOT NULL,
		session_id TEXT         NOT NULL DEFAULT '',
		scope      TEXT         NOT NULL,
		provenance TEXT         NOT NULL,
		key        TEXT,
		content    TEXT         NOT NULL,
		metadata   JSONB,
		created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
		expires_at TIMESTAMPTZ
	)`,
	`CREATE INDEX IF NOT EXISTS idx_memories_agent
		ON memories (agent_id, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_memories_session
		ON memories (agent_id, session_id)`,
	`CREATE INDEX IF NOT EXISTS idx_memories_scope
		ON memories (scope)`,
}

// ---------------------------------------------------------------------------
// ActionLog
// ---------------------------------------------------------------------------

const (
	writerQueueSize    = 4096
	batchMaxSize       = 256
	batchFlushInterval = 250 * time.Millisecond
)

// ActionLog implements storage.ActionLogBackend on Postgres.
type ActionLog struct {
	pool   *pgxpool.Pool
	logDir string // directory for per-agent .log mirror files
	log    *zap.Logger

	queue chan message.Event
	stop  chan struct{}
	wg    sync.WaitGroup
}

// OpenActionLog creates an ActionLog and starts its background writer goroutine.
func OpenActionLog(pool *pgxpool.Pool, logDir string, log *zap.Logger) (*ActionLog, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("postgres actionlog: mkdir %s: %w", logDir, err)
	}
	a := &ActionLog{
		pool:   pool,
		logDir: logDir,
		log:    log,
		queue:  make(chan message.Event, writerQueueSize),
		stop:   make(chan struct{}),
	}
	a.wg.Add(1)
	go a.run()
	return a, nil
}

// Append enqueues ev for async write. Never blocks.
func (a *ActionLog) Append(ev message.Event) {
	if ev.AgentID == "" {
		return
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	select {
	case a.queue <- ev:
	default:
		a.log.Warn("postgres actionlog: queue full, dropping event",
			zap.String("agent", ev.AgentID), zap.String("type", ev.Type))
	}
}

// run is the background flush goroutine.
func (a *ActionLog) run() {
	defer a.wg.Done()
	batch := make([]message.Event, 0, batchMaxSize)
	timer := time.NewTimer(batchFlushInterval)
	defer timer.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		a.flush(batch)
		batch = batch[:0]
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(batchFlushInterval)
	}

	for {
		select {
		case ev, ok := <-a.queue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, ev)
			if len(batch) >= batchMaxSize {
				flush()
			}
		case <-timer.C:
			flush()
		case <-a.stop:
			for {
				select {
				case ev := <-a.queue:
					batch = append(batch, ev)
					if len(batch) >= batchMaxSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// flush writes a batch to Postgres and mirrors it to per-agent log files.
func (a *ActionLog) flush(batch []message.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := a.pool.Acquire(ctx)
	if err != nil {
		a.log.Warn("postgres actionlog: acquire conn", zap.Error(err))
		return
	}
	defer conn.Release()

	for _, ev := range batch {
		payload, _ := json.Marshal(ev.Payload)
		_, err := conn.Exec(ctx,
			`INSERT INTO agent_events (agent_id, session_id, type, payload, created_at)
			 VALUES ($1, $2, $3, $4, $5)`,
			ev.AgentID, ev.SessionID, ev.Type, string(payload), ev.Timestamp,
		)
		if err != nil {
			a.log.Warn("postgres actionlog: insert event",
				zap.String("agent", ev.AgentID), zap.Error(err))
		}
	}

	// Mirror to per-agent log files.
	byAgent := make(map[string][]message.Event, len(batch))
	for _, ev := range batch {
		byAgent[ev.AgentID] = append(byAgent[ev.AgentID], ev)
	}
	for agentID, events := range byAgent {
		a.writeFile(agentID, events)
	}
}

func (a *ActionLog) writeFile(agentID string, events []message.Event) {
	path := a.EventFilePath(agentID)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, ev := range events {
		line, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		_, _ = w.Write(append(line, '\n'))
	}
	_ = w.Flush()
}

// EventFilePath returns the on-disk mirror path for agentID.
func (a *ActionLog) EventFilePath(agentID string) string {
	return filepath.Join(a.logDir, sanitize(agentID)+".log")
}

// Tail reads up to limit recent events from the per-agent log file.
func (a *ActionLog) Tail(agentID string, limit int) ([]message.Event, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	path := a.EventFilePath(agentID)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []message.Event{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if t := sc.Text(); strings.TrimSpace(t) != "" {
			lines = append(lines, t)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	events := make([]message.Event, 0, len(lines))
	for _, ln := range lines {
		var ev message.Event
		if err := json.Unmarshal([]byte(ln), &ev); err == nil {
			events = append(events, ev)
		}
	}
	return events, nil
}

// IncompleteMessageIns returns payloads of unresolved message.in events since `since`.
func (a *ActionLog) IncompleteMessageIns(since time.Time) ([][]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows, err := a.pool.Query(ctx, `
		SELECT agent_id, session_id, payload::text, created_at
		  FROM agent_events
		 WHERE type = 'message.in'
		   AND created_at >= $1
		 ORDER BY created_at ASC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("postgres actionlog: query message.in: %w", err)
	}
	defer rows.Close()

	type candidate struct {
		agentID, sessionID, payload string
		createdAt                   time.Time
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.agentID, &c.sessionID, &c.payload, &c.createdAt); err != nil {
			return nil, fmt.Errorf("postgres actionlog: scan: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	out := make([][]byte, 0, len(candidates))
	for _, c := range candidates {
		// Check dead-letter.
		var deadLetter int
		_ = a.pool.QueryRow(ctx, `
			SELECT 1 FROM agent_events
			 WHERE agent_id = $1 AND session_id = $2 AND type = 'message.dead_letter'
			 LIMIT 1
		`, c.agentID, c.sessionID).Scan(&deadLetter)
		if deadLetter == 1 {
			continue
		}
		// Check completion.
		var hasOutcome int
		err := a.pool.QueryRow(ctx, `
			SELECT 1 FROM agent_events
			 WHERE agent_id   = $1
			   AND session_id = $2
			   AND created_at > $3
			   AND type IN ('message.out', 'error')
			 LIMIT 1
		`, c.agentID, c.sessionID, c.createdAt).Scan(&hasOutcome)
		if err != nil {
			// Any scan error on LIMIT 1 means no row → incomplete.
			out = append(out, []byte(c.payload))
		}
	}
	return out, nil
}

// CountMessageInAttempts counts message.in events for (agentID, sessionID) since `since`.
func (a *ActionLog) CountMessageInAttempts(agentID, sessionID string, since time.Time) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var count int
	err := a.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM agent_events
		 WHERE agent_id   = $1
		   AND session_id = $2
		   AND type       = 'message.in'
		   AND created_at >= $3
	`, agentID, sessionID, since).Scan(&count)
	return count, err
}

// MarkDeadLetter synchronously inserts a message.dead_letter event.
func (a *ActionLog) MarkDeadLetter(agentID, sessionID, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload, _ := json.Marshal(map[string]string{
		"reason":     reason,
		"quarantine": "poison-pill guard (too many crash-recovery attempts)",
	})
	_, err := a.pool.Exec(ctx,
		`INSERT INTO agent_events (agent_id, session_id, type, payload, created_at)
		 VALUES ($1, $2, 'message.dead_letter', $3, $4)`,
		agentID, sessionID, string(payload), time.Now().UTC(),
	)
	return err
}

// Close flushes pending events and closes the pool.
func (a *ActionLog) Close() error {
	select {
	case <-a.stop:
	default:
		close(a.stop)
	}
	a.wg.Wait()
	a.pool.Close()
	return nil
}

// sanitize makes an agent ID safe for use as a filename.
func sanitize(id string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, id)
}

// ---------------------------------------------------------------------------
// MemoryStore
// ---------------------------------------------------------------------------

// MemoryStore implements storage.MemoryBackend on Postgres.
type MemoryStore struct {
	pool *pgxpool.Pool
	log  *zap.Logger
}

// OpenMemoryStore returns a MemoryStore backed by pool.
func OpenMemoryStore(pool *pgxpool.Pool, log *zap.Logger) *MemoryStore {
	return &MemoryStore{pool: pool, log: log}
}

// Archive inserts a memory entry. Duplicate IDs are silently ignored.
func (m *MemoryStore) Archive(entry memory.Entry) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	meta, _ := json.Marshal(entry.Metadata)
	_, err := m.pool.Exec(ctx, `
		INSERT INTO memories
			(id, agent_id, session_id, scope, provenance, key, content, metadata, created_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO NOTHING`,
		entry.ID, entry.AgentID, entry.SessionID, string(entry.Scope),
		"", entry.Key, entry.Content, // provenance col retained for schema compat, always empty
		string(meta), entry.CreatedAt, entry.ExpiresAt,
	)
	return err
}

// Search performs case-insensitive substring search for agentID.
func (m *MemoryStore) Search(agentID, query string, limit int) ([]memory.Entry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := m.pool.Query(ctx, `
		SELECT id, agent_id, session_id, scope, provenance, key,
		       content, metadata::text, created_at, expires_at
		  FROM memories
		 WHERE agent_id = $1 AND content ILIKE $2
		 ORDER BY created_at DESC
		 LIMIT $3`,
		agentID, "%"+query+"%", limit,
	)
	if err != nil {
		return nil, err
	}
	return scanPgEntries(rows)
}

// ReadByScope returns entries for (agentID, sessionID, scope), newest-first.
func (m *MemoryStore) ReadByScope(agentID, sessionID string, scope memory.Scope, limit int) ([]memory.Entry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := m.pool.Query(ctx, `
		SELECT id, agent_id, session_id, scope, provenance, key,
		       content, metadata::text, created_at, expires_at
		  FROM memories
		 WHERE agent_id = $1 AND session_id = $2 AND scope = $3
		 ORDER BY created_at DESC
		 LIMIT $4`,
		agentID, sessionID, string(scope), limit,
	)
	if err != nil {
		return nil, err
	}
	return scanPgEntries(rows)
}

// ReadGlobal returns the most recent entries for agentID across all sessions.
func (m *MemoryStore) ReadGlobal(agentID string, limit int) ([]memory.Entry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := m.pool.Query(ctx, `
		SELECT id, agent_id, session_id, scope, provenance, key,
		       content, metadata::text, created_at, expires_at
		  FROM memories
		 WHERE agent_id = $1
		 ORDER BY created_at DESC
		 LIMIT $2`,
		agentID, limit,
	)
	if err != nil {
		return nil, err
	}
	return scanPgEntries(rows)
}

// Prune deletes entries older than before for agentID.
func (m *MemoryStore) Prune(agentID string, before time.Time) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := m.pool.Exec(ctx,
		`DELETE FROM memories WHERE agent_id = $1 AND created_at < $2`, agentID, before,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected(), nil
}

// Close is a no-op; the pool is owned by the factory (Open) and closed via ActionLog.Close().
func (m *MemoryStore) Close() error { return nil }

// ---------------------------------------------------------------------------
// pgRows scanner
// ---------------------------------------------------------------------------

// pgRows is a small interface so scanPgEntries works with pgx v5 Rows.
// pgx.Rows already satisfies this; the interface exists to avoid importing
// pgx types directly in the scanner helper.
type pgRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

func scanPgEntries(rows pgRows) ([]memory.Entry, error) {
	defer rows.Close()
	var entries []memory.Entry
	for rows.Next() {
		var e memory.Entry
		var scope, ignoredProvenance, meta string // provenance col retained in schema, discarded on read
		var expiresAt sql.NullTime
		if err := rows.Scan(
			&e.ID, &e.AgentID, &e.SessionID, &scope, &ignoredProvenance,
			&e.Key, &e.Content, &meta, &e.CreatedAt, &expiresAt,
		); err != nil {
			return nil, err
		}
		e.Scope = memory.Scope(scope)
		if expiresAt.Valid {
			t := expiresAt.Time
			e.ExpiresAt = &t
		}
		if meta != "" && meta != "null" {
			_ = json.Unmarshal([]byte(meta), &e.Metadata)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

// Open creates a *pgxpool.Pool, bootstraps the schema (CREATE TABLE IF NOT EXISTS),
// and returns both ActionLog and MemoryStore ready for use.
//
// dsn is a standard libpq connection string:
//
//	"postgres://user:pass@host:5432/dbname?sslmode=disable"
//
// logDir is where per-agent .log mirror files are written.
func Open(dsn, logDir string, log *zap.Logger) (*ActionLog, *MemoryStore, *pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("postgres: parse DSN: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MinConns = 2

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("postgres: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, nil, nil, fmt.Errorf("postgres: ping: %w", err)
	}

	// Bootstrap schema — safe to run on every start (IF NOT EXISTS).
	conn, err := pool.Acquire(ctx)
	if err != nil {
		pool.Close()
		return nil, nil, nil, fmt.Errorf("postgres: acquire for schema: %w", err)
	}
	for _, ddl := range ddlStatements {
		if _, err := conn.Exec(ctx, ddl); err != nil {
			conn.Release()
			pool.Close()
			preview := ddl
			if len(preview) > 60 {
				preview = preview[:60]
			}
			return nil, nil, nil, fmt.Errorf("postgres: schema DDL %q: %w", preview, err)
		}
	}
	conn.Release()

	al, err := OpenActionLog(pool, logDir, log)
	if err != nil {
		pool.Close()
		return nil, nil, nil, err
	}

	ms := OpenMemoryStore(pool, log)
	return al, ms, pool, nil
}
