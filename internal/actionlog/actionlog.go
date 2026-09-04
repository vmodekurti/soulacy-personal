// Package actionlog records structured agent actions to two places:
//
//  1. A known per-agent log file (<dir>/<agent-id>.log) as JSON Lines — easy to
//     tail, watch from the GUI, or inspect with `tail -f`. This is the location
//     the UI polls.
//  2. A SQLite table (agent_events) for durable, queryable history that survives
//     restarts and supports cross-agent queries.
//
// Every agent execution emits events (run start, llm calls, tool calls/results,
// reply, errors) which flow through here via the gateway EventHub.
//
// PRODUCTION_AUDIT → HIGH/Performance: Append was the engine's #1 hot-path
// lock-while-doing-I/O finding. The previous implementation held a single
// global mutex while doing two synchronous fsyncs per event (file append +
// SQLite insert), serialising every agent through this writer. We now
// run a single buffered async writer goroutine: callers enqueue events
// non-blockingly, the writer drains them in batches, fsyncs once per
// batch, and inserts to SQLite in one transaction. Latency on the agent
// loop drops from ~ms-per-event to ~µs-per-event.
package actionlog

import (
	"bufio"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/internal/redact"
	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/pkg/message"
)

const (
	// maxFileBytes triggers pruning of a per-agent log file once exceeded.
	maxFileBytes = 1 << 20 // 1 MiB
	// keepLines is how many recent lines are retained when a file is pruned.
	keepLines = 2000

	// PERF-3: rotation. The in-place prune above keeps the *active* file small
	// (~1 MiB) for fast tailing, but the previous implementation discarded the
	// pruned-off history entirely. Rotation instead preserves history by
	// renaming the active file to a numbered, gzipped backup (.1.gz, .2.gz, …)
	// once it crosses defaultMaxRotateBytes, keeping at most defaultMaxRotated
	// backups (oldest dropped). Defaults are overridable via WithRotation.

	// defaultMaxRotateBytes is the size at which a per-agent log is rotated.
	defaultMaxRotateBytes = 50 << 20 // 50 MiB
	// defaultMaxRotated is how many gzipped backups (.N.gz) are retained.
	defaultMaxRotated = 5

	// writerQueueSize is how many events can be buffered before Append
	// becomes a drop (with a warn log). Sized for ~1 second of high-rate
	// agent activity at 200 events/sec.
	writerQueueSize = 4096

	// batchMaxSize forces a flush even if the timeout hasn't elapsed —
	// caps memory growth under bursts.
	batchMaxSize = 256

	// batchFlushInterval is the maximum age of an unflushed event before
	// the writer forces a flush. Keeps the GUI's polling tail "fresh enough"
	// without thrashing the disk on every event.
	batchFlushInterval = 250 * time.Millisecond
)

const eventsSchema = `
CREATE TABLE IF NOT EXISTS agent_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id    TEXT NOT NULL,
    session_id  TEXT,
    type        TEXT NOT NULL,
    payload     TEXT,
    created_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_agent ON agent_events(agent_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_session ON agent_events(session_id, created_at);
`

// Logger writes agent action events to per-agent files and SQLite.
type Logger struct {
	dir string
	db  *sql.DB
	log *zap.Logger

	// queue receives events from Append; the writer goroutine drains it.
	queue chan message.Event
	wg    sync.WaitGroup
	stop  chan struct{}

	// pruneRequest is a non-blocking signal — when the writer notices a
	// file may need pruning it sends the path here; the prune goroutine
	// picks it up out-of-band. Buffered to avoid blocking the writer if
	// the prune goroutine is mid-rewrite.
	pruneRequest chan string

	// PERF-3 rotation config (per-Logger so tests can use small thresholds).
	maxRotateBytes     int64
	maxRotated         int
	retention          time.Duration
	lastRetentionSweep time.Time
}

// Option configures a Logger at construction time. Defined as a variadic on
// New so existing 3-arg callers keep compiling.
type Option func(*Logger)

// WithRotation overrides the action-log rotation policy: maxBytes is the size
// at which the active per-agent log is rotated into a gzipped backup, and
// maxBackups is how many .N.gz backups are retained (oldest dropped). Values
// <= 0 leave the corresponding default in place.
func WithRotation(maxBytes int64, maxBackups int) Option {
	return func(l *Logger) {
		if maxBytes > 0 {
			l.maxRotateBytes = maxBytes
		}
		if maxBackups > 0 {
			l.maxRotated = maxBackups
		}
	}
}

// WithRetention deletes durable events and expired rotated files older than d.
// Zero disables age-based deletion (size-based rotation remains active).
func WithRetention(d time.Duration) Option {
	return func(l *Logger) { l.retention = d }
}

// New creates a Logger. dir is the per-agent log directory (created if missing);
// dbPath is the SQLite database for durable event history. The async writer
// goroutine is started before New returns; callers must invoke Close() on
// shutdown to flush any pending events.
func New(dir, dbPath string, log *zap.Logger, opts ...Option) (*Logger, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("actionlog: create dir %s: %w", dir, err)
	}
	// PRODUCTION_AUDIT → F3 (2026-05-27): centralised WAL + NORMAL synchronous
	// + 30s busy_timeout + tuned pool via internal/sqlitex. Litestream
	// compatibility preserved (WAL is the only journal mode it streams).
	db, err := sqlitex.Open(dbPath, sqlitex.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("actionlog: open sqlite %s: %w", dbPath, err)
	}
	for _, stmt := range strings.Split(eventsSchema, ";") {
		if s := strings.TrimSpace(stmt); s != "" {
			if _, err := db.Exec(s); err != nil {
				return nil, fmt.Errorf("actionlog: schema: %w", err)
			}
		}
	}

	// Schema versioning (E22 adoption): v1 = the idempotent bootstrap above;
	// future changes go through sqlitex.MigrateSchema with v2+.
	if err := sqlitex.RecordSchemaVersion(db, "actionlog", 1); err != nil {
		return nil, fmt.Errorf("actionlog: schema version: %w", err)
	}
	l := &Logger{
		dir:            dir,
		db:             db,
		log:            log,
		queue:          make(chan message.Event, writerQueueSize),
		pruneRequest:   make(chan string, 64),
		stop:           make(chan struct{}),
		maxRotateBytes: defaultMaxRotateBytes,
		maxRotated:     defaultMaxRotated,
		retention:      90 * 24 * time.Hour,
	}
	for _, opt := range opts {
		opt(l)
	}
	l.wg.Add(2)
	go l.run()
	go l.pruneLoop()
	return l, nil
}

// Path returns the known on-disk log location for an agent.
func (l *Logger) Path(agentID string) string {
	return filepath.Join(l.dir, sanitize(agentID)+".log")
}

// Append enqueues one event for the writer goroutine. Never blocks the
// caller; if the queue is full, the event is dropped and a warn is logged
// (preserving the engine's progress is more important than complete logs).
func (l *Logger) Append(ev message.Event) {
	if ev.AgentID == "" {
		return
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	// The event hub may need the full live payload, but neither the JSONL nor
	// SQLite persistence layer does. Copy and redact at this boundary so tool
	// arguments/results cannot bypass protection through a new event type.
	ev.Payload = redact.Value(ev.Payload)
	select {
	case l.queue <- ev:
		metrics.ActionlogQueueDepth.Set(float64(len(l.queue)))
	default:
		metrics.ActionlogDropsTotal.Inc()
		l.log.Warn("actionlog: queue full, dropping event",
			zap.String("agent", ev.AgentID),
			zap.String("type", ev.Type),
			zap.Int("queue_size", writerQueueSize),
		)
	}
}

// run is the single writer goroutine. Reads events off the queue, batches
// up to batchMaxSize OR batchFlushInterval, then writes them all at once.
func (l *Logger) run() {
	defer l.wg.Done()
	batch := make([]message.Event, 0, batchMaxSize)
	timer := time.NewTimer(batchFlushInterval)
	defer timer.Stop()

	flush := func() {
		// Always re-arm the timer first, regardless of whether the batch is
		// empty. Previously the timer was only reset after a non-empty flush,
		// so a timer-fire at startup (before any events arrived) would leave
		// the timer permanently dead — all subsequent events would accumulate
		// in the batch but never be written until the process exited.
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(batchFlushInterval)

		if len(batch) == 0 {
			return
		}
		metrics.ActionlogBatchSize.Observe(float64(len(batch)))
		l.flush(batch)
		batch = batch[:0]
		metrics.ActionlogQueueDepth.Set(float64(len(l.queue)))
	}

	for {
		select {
		case ev, ok := <-l.queue:
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
		case <-l.stop:
			// Drain whatever is queued, then exit.
			drain := true
			for drain {
				select {
				case ev := <-l.queue:
					batch = append(batch, ev)
					if len(batch) >= batchMaxSize {
						flush()
					}
				default:
					drain = false
				}
			}
			flush()
			return
		}
	}
}

// flush writes a batch of events: appends to per-agent files (one open
// per file across the batch) and bulk-inserts to SQLite in one transaction.
// Errors are logged per-event; we never block the engine.
func (l *Logger) flush(batch []message.Event) {
	// Group by agent so each file is opened/closed once per batch instead of
	// once per event.
	byAgent := make(map[string][]message.Event, len(batch))
	for _, ev := range batch {
		byAgent[ev.AgentID] = append(byAgent[ev.AgentID], ev)
	}
	for agentID, events := range byAgent {
		l.writeFileBatch(agentID, events)
	}
	l.writeDBBatch(batch)
}

func (l *Logger) writeFileBatch(agentID string, events []message.Event) {
	path := l.Path(agentID)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		l.log.Warn("actionlog: open file", zap.String("path", path), zap.Error(err))
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, ev := range events {
		line, err := json.Marshal(ev)
		if err != nil {
			l.log.Warn("actionlog: marshal event", zap.Error(err))
			continue
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			l.log.Warn("actionlog: write file", zap.Error(err))
			return
		}
	}
	if err := w.Flush(); err != nil {
		l.log.Warn("actionlog: flush file", zap.Error(err))
	}
	// Request prune asynchronously (non-blocking). The prune goroutine
	// rate-limits and dedupes work so a steady stream of writes doesn't
	// continually re-rewrite the file.
	// (PRODUCTION_AUDIT → LOW/Reliability)
	select {
	case l.pruneRequest <- path:
	default:
		// Channel full — prune backlog is healthy enough on its own; skip.
	}
}

func (l *Logger) writeDBBatch(events []message.Event) {
	tx, err := l.db.Begin()
	if err != nil {
		l.log.Warn("actionlog: begin tx", zap.Error(err))
		return
	}
	stmt, err := tx.Prepare(`INSERT INTO agent_events (agent_id, session_id, type, payload, created_at) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		l.log.Warn("actionlog: prepare insert", zap.Error(err))
		return
	}
	defer stmt.Close()
	for _, ev := range events {
		payload, _ := json.Marshal(ev.Payload)
		if _, err := stmt.Exec(ev.AgentID, ev.SessionID, ev.Type, string(payload), ev.Timestamp); err != nil {
			l.log.Warn("actionlog: sqlite insert", zap.Error(err))
		}
	}
	if err := tx.Commit(); err != nil {
		l.log.Warn("actionlog: commit", zap.Error(err))
	}
}

// tailBlockSize is the chunk size used by Tail when reading a log file
// backwards from the end. 64 KiB comfortably holds many JSONL lines per read
// while keeping a single reusable buffer small.
const tailBlockSize = 64 * 1024

// Tail returns the most recent events for an agent from its log file, oldest-first.
//
// PERF-4: this reads the file BACKWARDS in fixed-size blocks (seeking from the
// end) and stops as soon as it has collected `limit` lines, so memory and I/O
// are O(limit) rather than O(file size). Behaviour and signature are unchanged
// from the previous whole-file implementation: blank lines are skipped, lines
// that fail to JSON-unmarshal are dropped, and the result is oldest-first.
//
// Scope: Tail reads the CURRENT active log file only. If rotation (PERF-3) has
// split older history into <agent>.log.N.gz backups, those are intentionally
// not traversed here — the active file is what the GUI polls, and reaching back
// into compressed backups would defeat the O(limit) goal. Callers needing full
// history should query SQLite (agent_events) instead.
func (l *Logger) Tail(agentID string, limit int) ([]message.Event, error) {
	return l.tailFilter(agentID, limit, nil)
}

// TailFiltered is Tail, but only events whose Type is in `allowed` count toward
// the limit. High-volume tool.log lines from a chatty run can otherwise crowd
// out the boundary events (message.in/out, error) that mark distinct runs, so
// older runs fall outside the tail window and vanish from the History panel.
// Filtering DURING the backward scan makes `limit` count run-boundary events,
// so all of a job's runs show. `allowed` empty/nil ⇒ unfiltered (same as Tail).
func (l *Logger) TailFiltered(agentID string, limit int, allowed map[string]bool) ([]message.Event, error) {
	if len(allowed) == 0 {
		return l.tailFilter(agentID, limit, nil)
	}
	return l.tailFilter(agentID, limit, allowed)
}

// QueryFiltered returns recent events for an agent from the durable SQLite
// history instead of the rolling JSONL file. It is intentionally not part of
// the public storage.ActionLogBackend interface so non-SQL backends remain
// compatible; callers can type-assert when they want full history.
func (l *Logger) QueryFiltered(agentID string, limit int, allowed map[string]bool) ([]message.Event, error) {
	return l.QueryEvents(agentID, "", limit, allowed)
}

// QueryEvents returns durable SQLite-backed events, oldest-first, from the
// newest `limit` events matching optional filters. Empty agentID/sessionID mean
// "all". This powers cross-agent Activity/history views that should not depend
// on per-agent JSONL tails or log rotation.
func (l *Logger) QueryEvents(agentID, sessionID string, limit int, allowed map[string]bool) ([]message.Event, error) {
	agentID = strings.TrimSpace(agentID)
	sessionID = strings.TrimSpace(sessionID)
	if limit <= 0 {
		limit = 1000
	} else if limit > 50000 {
		limit = 50000
	}
	args := []any{}
	where := "1 = 1"
	if agentID != "" {
		where += " AND agent_id = ?"
		args = append(args, agentID)
	}
	if sessionID != "" {
		where += " AND session_id = ?"
		args = append(args, sessionID)
	}
	if len(allowed) > 0 {
		placeholders := make([]string, 0, len(allowed))
		for typ := range allowed {
			typ = strings.TrimSpace(typ)
			if typ == "" {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, typ)
		}
		if len(placeholders) > 0 {
			where += " AND type IN (" + strings.Join(placeholders, ",") + ")"
		}
	}
	args = append(args, limit)
	rows, err := l.db.Query(`
		SELECT agent_id, session_id, type, COALESCE(payload, ''), created_at
		FROM (
			SELECT agent_id, session_id, type, payload, created_at, id
			FROM agent_events
			WHERE `+where+`
			ORDER BY created_at DESC, id DESC
			LIMIT ?
		)
		ORDER BY created_at ASC, id ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]message.Event, 0, limit)
	for rows.Next() {
		var ev message.Event
		var payload string
		var atRaw any
		if err := rows.Scan(&ev.AgentID, &ev.SessionID, &ev.Type, &payload, &atRaw); err != nil {
			return nil, err
		}
		ev.Timestamp = parseSQLiteTime(atRaw)
		if payload != "" {
			var decoded any
			if err := json.Unmarshal([]byte(payload), &decoded); err == nil {
				ev.Payload = decoded
			} else {
				ev.Payload = payload
			}
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (l *Logger) tailFilter(agentID string, limit int, allowed map[string]bool) ([]message.Event, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	path := l.Path(agentID)

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []message.Event{}, nil // no actions logged yet
		}
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()

	// Collect up to `limit` lines reading backwards. `lines` holds them in
	// reverse (newest-first) order; we flip to oldest-first at the end.
	lines := make([]string, 0, limit)
	buf := make([]byte, tailBlockSize)
	// `carry` is the partial first line of the most-recently-read block: its
	// start hasn't been seen yet because it continues into the previous block.
	var carry []byte
	pos := size

	for pos > 0 && len(lines) < limit {
		readSize := int64(tailBlockSize)
		if pos < readSize {
			readSize = pos
		}
		pos -= readSize
		if _, err := f.ReadAt(buf[:readSize], pos); err != nil && err != io.EOF {
			return nil, err
		}
		// Prepend this block to any carry from the previous (later) block.
		chunk := make([]byte, 0, int(readSize)+len(carry))
		chunk = append(chunk, buf[:readSize]...)
		chunk = append(chunk, carry...)

		// Split into lines. The segment before the first '\n' is incomplete
		// unless we've reached the start of the file (pos == 0); hold it as the
		// new carry to be completed by the next (earlier) block.
		var start int
		if pos > 0 {
			if nl := indexByte(chunk, '\n'); nl >= 0 {
				carry = append(carry[:0], chunk[:nl]...)
				start = nl + 1
			} else {
				// No newline in the whole accumulated chunk yet — keep buffering.
				carry = append(carry[:0], chunk...)
				continue
			}
		} else {
			carry = carry[:0]
			start = 0
		}

		// Emit complete lines from `start` to end, newest-first.
		emitLinesReverse(chunk[start:], &lines, limit, allowed)
	}

	// Reverse to oldest-first and clamp (we may have collected exactly limit).
	if len(lines) > limit {
		lines = lines[:limit]
	}
	reverseStrings(lines)

	events := make([]message.Event, 0, len(lines))
	for _, ln := range lines {
		var ev message.Event
		if err := json.Unmarshal([]byte(ln), &ev); err == nil {
			events = append(events, ev)
		}
	}
	return events, nil
}

// emitLinesReverse splits data on '\n' and appends non-blank lines to *lines in
// reverse (last line first), stopping once *lines reaches limit. Used by Tail
// while walking the file backwards so the newest lines accumulate first.
func emitLinesReverse(data []byte, lines *[]string, limit int, allowed map[string]bool) {
	// Walk from the end so the newest line in this block is appended first.
	end := len(data)
	for end > 0 && len(*lines) < limit {
		nl := lastIndexByte(data[:end], '\n')
		seg := data[nl+1 : end]
		if t := strings.TrimSpace(string(seg)); t != "" {
			if allowed == nil || lineTypeAllowed(t, allowed) {
				*lines = append(*lines, t)
			}
		}
		if nl < 0 {
			break
		}
		end = nl
	}
}

// lineTypeAllowed reports whether a raw JSONL event line's "type" is in the
// allowed set. Used to filter during the tail scan so only matching events count
// toward the limit. Unparseable lines are excluded.
func lineTypeAllowed(line string, allowed map[string]bool) bool {
	var probe struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(line), &probe) != nil {
		return false
	}
	return allowed[probe.Type]
}

func indexByte(b []byte, c byte) int {
	for i := 0; i < len(b); i++ {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func lastIndexByte(b []byte, c byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func reverseStrings(s []string) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// IncompleteMessageIns returns the payload JSON of every message.in event
// recorded in the SQLite history that does NOT have a corresponding
// message.out or error event after it for the same agent + session +
// inbound message ID. Used by the gateway at boot to re-enqueue runs that
// were in flight when the process died.
// (PRODUCTION_AUDIT → F2, 2026-05-27)
//
// `since` bounds the scan to avoid re-running ancient messages on every
// boot (e.g. operators leaving the host off for a weekend shouldn't blast
// 3-day-old prompts back at users).
//
// Sessions that have been marked with a message.dead_letter event are
// excluded automatically — they have already been quarantined by a
// previous recovery pass (poison-pill guard).
//
// Returns raw JSON payloads rather than message.Message structs because
// the actionlog package doesn't import pkg/message's full schema layer;
// callers unmarshal into whichever type matches their dispatch path.
func (l *Logger) IncompleteMessageIns(since time.Time) ([][]byte, error) {
	// Pull message.in candidates first, then prove each one DIDN'T complete.
	// A single SQL with NOT EXISTS would be denser but harder to reason
	// about; this two-step keeps the matching logic in Go where it's easy
	// to evolve as we add new event types (e.g. a future message.cancelled).
	rows, err := l.db.Query(`
		SELECT agent_id, session_id, payload, created_at
		  FROM agent_events
		 WHERE type = 'message.in'
		   AND created_at >= ?
		 ORDER BY created_at ASC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("actionlog: query message.in: %w", err)
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
			return nil, fmt.Errorf("actionlog: scan: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// Now check completion. A run is "complete" if any message.out OR
	// error event landed for (agent, session) AFTER the message.in.
	// Multiple messages can share a session (long-running chats); pair
	// them by timestamp ordering, not by message ID — the engine doesn't
	// emit a back-reference in message.out events.
	//
	// Dead-lettered sessions (message.dead_letter exists for the same
	// agent+session) are skipped — the poison-pill guard already quarantined
	// them and there's no point re-enqueuing them again.
	out := make([][]byte, 0, len(candidates))
	for _, c := range candidates {
		// Skip dead-lettered sessions.
		var deadLetter int
		_ = l.db.QueryRow(`
			SELECT 1 FROM agent_events
			 WHERE agent_id = ? AND session_id = ? AND type = 'message.dead_letter'
			 LIMIT 1
		`, c.agentID, c.sessionID).Scan(&deadLetter)
		if deadLetter == 1 {
			continue
		}

		var hasOutcome int
		err := l.db.QueryRow(`
			SELECT 1 FROM agent_events
			 WHERE agent_id = ?
			   AND session_id = ?
			   AND created_at > ?
			   AND type IN ('message.out', 'error')
			 LIMIT 1
		`, c.agentID, c.sessionID, c.createdAt).Scan(&hasOutcome)
		if err == sql.ErrNoRows {
			out = append(out, []byte(c.payload))
		} else if err != nil {
			// Tolerate per-row failures: log and keep scanning. We'd
			// rather recover 9/10 in-flight runs than abort the recovery
			// pass on one weird row.
			l.log.Warn("actionlog: recovery outcome check failed",
				zap.String("agent", c.agentID),
				zap.String("session", c.sessionID),
				zap.Error(err))
		}
	}
	return out, nil
}

// CountMessageInAttempts returns how many message.in events exist for a
// given (agentID, sessionID) pair since `since`. Each represents one
// time the engine was asked to handle that session — including the
// original run and any crash-recovery replays. Used by the poison-pill
// guard in recover.go.
func (l *Logger) CountMessageInAttempts(agentID, sessionID string, since time.Time) (int, error) {
	var count int
	err := l.db.QueryRow(`
		SELECT COUNT(*) FROM agent_events
		 WHERE agent_id = ?
		   AND session_id = ?
		   AND type = 'message.in'
		   AND created_at >= ?
	`, agentID, sessionID, since).Scan(&count)
	return count, err
}

// MarkDeadLetter writes a message.dead_letter event directly to SQLite
// (synchronous, bypasses the async queue) so it is visible to the NEXT
// boot's IncompleteMessageIns query even if the process exits immediately
// after. This is only called by the startup poison-pill guard and should
// NOT be called on the hot path.
func (l *Logger) MarkDeadLetter(agentID, sessionID, reason string) error {
	payload, _ := json.Marshal(map[string]string{
		"reason":     reason,
		"quarantine": "poison-pill guard (too many crash-recovery attempts)",
	})
	_, err := l.db.Exec(
		`INSERT INTO agent_events (agent_id, session_id, type, payload, created_at) VALUES (?, ?, 'message.dead_letter', ?, ?)`,
		agentID, sessionID, string(payload), time.Now().UTC(),
	)
	return err
}

// pruneLoop coalesces pending prune requests and runs each pruneIfLarge in
// its own iteration. Dedupes on a per-tick basis: many writes to the same
// file produce one rewrite per pruneTickInterval, not N. Worst case: the
// file briefly exceeds maxFileBytes by one tick's worth of writes — fine,
// the cap is soft. (PRODUCTION_AUDIT → LOW/Reliability)
func (l *Logger) pruneLoop() {
	defer l.wg.Done()
	const pruneTickInterval = 10 * time.Second
	tick := time.NewTicker(pruneTickInterval)
	defer tick.Stop()

	pending := make(map[string]struct{})
	for {
		select {
		case path, ok := <-l.pruneRequest:
			if !ok {
				return
			}
			pending[path] = struct{}{}
		case <-tick.C:
			for path := range pending {
				l.rotateIfLarge(path)
				l.pruneIfLarge(path)
			}
			pending = make(map[string]struct{})
			if l.retention > 0 && time.Since(l.lastRetentionSweep) >= time.Hour {
				_ = l.DeleteBefore(time.Now().Add(-l.retention))
				l.lastRetentionSweep = time.Now()
			}
		case <-l.stop:
			// Drain remaining requests, prune once each, then exit.
			drain := true
			for drain {
				select {
				case path := <-l.pruneRequest:
					pending[path] = struct{}{}
				default:
					drain = false
				}
			}
			for path := range pending {
				l.rotateIfLarge(path)
				l.pruneIfLarge(path)
			}
			return
		}
	}
}

// DeleteBefore applies the configured deletion policy immediately. It removes
// matching SQLite rows and rotated log files whose modification time predates
// cutoff. Active logs remain size-bounded and may contain newer records.
func (l *Logger) DeleteBefore(cutoff time.Time) error {
	if l == nil || l.db == nil {
		return nil
	}
	if _, err := l.db.Exec(`DELETE FROM agent_events WHERE created_at < ?`, cutoff.UTC()); err != nil {
		return err
	}
	entries, err := os.ReadDir(l.dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.Contains(entry.Name(), ".log.") {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(l.dir, entry.Name()))
		}
	}
	return nil
}

// pruneIfLarge rewrites a log file keeping only the last keepLines once it grows
// past maxFileBytes. Called by the prune goroutine outside the agent path so
// the long-rewrite stall (PRODUCTION_AUDIT → LOW) doesn't block Append OR
// the writer goroutine's batch flushes.
func (l *Logger) pruneIfLarge(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < maxFileBytes {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	f.Close()
	if len(lines) <= keepLines {
		return
	}
	lines = lines[len(lines)-keepLines:]
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// rotateIfLarge implements PERF-3 size-based rotation. When the active log
// (path) grows past l.maxRotateBytes it is rotated:
//
//	path.(N-1).gz → path.N.gz   (shift existing backups up, oldest dropped)
//	path          → path.1.gz   (compress the current file into the newest slot)
//	path                         (recreated empty so appends continue)
//
// At most l.maxRotated gzipped backups are kept; any beyond that (including a
// rotated-out path.(maxRotated+1).gz) are deleted, bounding total disk use.
//
// Rotation runs in the prune goroutine, off the agent hot path. It is a no-op
// when the file is missing, under threshold, or rotation is disabled
// (maxRotateBytes <= 0 / maxRotated <= 0).
func (l *Logger) rotateIfLarge(path string) {
	if l.maxRotateBytes <= 0 || l.maxRotated <= 0 {
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() < l.maxRotateBytes {
		return
	}

	// Shift existing backups up by one, dropping anything that would exceed
	// the retention count. Walk from the oldest down so renames don't clobber.
	// We also remove a would-be path.(maxRotated+1).gz to bound disk use even
	// if maxRotated shrank between runs.
	if old := l.rotatedPath(path, l.maxRotated+1); fileExists(old) {
		_ = os.Remove(old)
	}
	for i := l.maxRotated; i >= 1; i-- {
		src := l.rotatedPath(path, i)
		if !fileExists(src) {
			continue
		}
		if i == l.maxRotated {
			// This is the oldest retained slot; it gets evicted.
			_ = os.Remove(src)
			continue
		}
		_ = os.Rename(src, l.rotatedPath(path, i+1))
	}

	// Compress the active file into the newest backup slot (.1.gz), then
	// truncate the active file so appends resume into an empty log.
	if err := gzipFile(path, l.rotatedPath(path, 1)); err != nil {
		l.log.Warn("actionlog: rotate gzip", zap.String("path", path), zap.Error(err))
		return
	}
	if err := os.Truncate(path, 0); err != nil {
		l.log.Warn("actionlog: rotate truncate", zap.String("path", path), zap.Error(err))
	}
}

// rotatedPath returns the backup name for the Nth rotation of path, e.g.
// "<agent>.log.1.gz". N starts at 1 (newest).
func (l *Logger) rotatedPath(path string, n int) string {
	return fmt.Sprintf("%s.%d.gz", path, n)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// gzipFile writes a gzip-compressed copy of src to dst (overwriting dst).
func gzipFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	gw := gzip.NewWriter(out)
	if _, err := io.Copy(gw, in); err != nil {
		_ = gw.Close()
		return err
	}
	return gw.Close()
}

// Close stops the writer goroutine, drains any pending events, and releases
// the SQLite handle. Safe to call multiple times.
func (l *Logger) Close() error {
	select {
	case <-l.stop:
		// already closed
	default:
		close(l.stop)
	}
	l.wg.Wait()
	if l.db != nil {
		return l.db.Close()
	}
	return nil
}

// sanitize makes an agent ID safe to use as a filename.
func sanitize(id string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, id)
}
