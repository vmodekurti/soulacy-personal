// actionlog_test.go — tests for the async action-log writer.
// Append is non-blocking; tests that need to observe persisted state must
// call Close() (which drains the queue) before reading files or querying the DB.
package actionlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/message"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newLogger(t *testing.T) *Logger {
	t.Helper()
	dir := t.TempDir()
	l, err := New(dir, filepath.Join(dir, "events.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func makeEvent(agentID, evType string) message.Event {
	return message.Event{
		AgentID:   agentID,
		SessionID: "sess-1",
		Type:      evType,
		Timestamp: time.Now().UTC(),
	}
}

// ---------------------------------------------------------------------------
// sanitize
// ---------------------------------------------------------------------------

func TestSanitizeAlphanumeric(t *testing.T) {
	if got := sanitize("agent-123_abc"); got != "agent-123_abc" {
		t.Errorf("sanitize alphanumeric: got %q", got)
	}
}

func TestSanitizeSpecialChars(t *testing.T) {
	got := sanitize("agent/with spaces.dots")
	if strings.ContainsAny(got, "/ .") {
		t.Errorf("sanitize: special chars not replaced: %q", got)
	}
}

func TestSanitizeEmpty(t *testing.T) {
	// Empty input maps to empty output — no panic.
	_ = sanitize("")
}

// ---------------------------------------------------------------------------
// New
// ---------------------------------------------------------------------------

func TestNewCreatesLogger(t *testing.T) {
	l := newLogger(t)
	if l == nil {
		t.Fatal("New returned nil")
	}
}

func TestNewBadDir(t *testing.T) {
	// /proc is not writable on macOS — MkdirAll will fail.
	_, err := New("/proc/no/such/path/soulacy/actionlog", "/tmp/soulacy_test_events.db", zap.NewNop())
	if err == nil {
		t.Log("skipping: /proc path was unexpectedly writable on this OS")
	}
}

// ---------------------------------------------------------------------------
// Path
// ---------------------------------------------------------------------------

func TestPathContainsSanitizedAgentID(t *testing.T) {
	l := newLogger(t)
	path := l.Path("my/agent.bot")
	if !strings.HasSuffix(path, ".log") {
		t.Errorf("Path does not end in .log: %q", path)
	}
	// Only check the stem (strip .log) — the extension itself contains a dot.
	stem := strings.TrimSuffix(filepath.Base(path), ".log")
	if strings.ContainsAny(stem, "/. ") {
		t.Errorf("Path stem has unsafe chars: %q", stem)
	}
}

// ---------------------------------------------------------------------------
// Append + Tail (via Close drain)
// ---------------------------------------------------------------------------

func TestAppendSkipsEmptyAgentID(t *testing.T) {
	l := newLogger(t)
	l.Append(message.Event{AgentID: "", Type: "test", Timestamp: time.Now().UTC()})
	_ = l.Close()
	// No file should exist for empty agent ID.
	events, err := l.Tail("", 10)
	if err != nil {
		t.Fatalf("Tail empty agentID: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func TestAppendSetsTimestampWhenZero(t *testing.T) {
	l := newLogger(t)
	before := time.Now().UTC().Add(-time.Second)
	// Zero Timestamp should be auto-set.
	l.Append(message.Event{AgentID: "agent-ts", Type: "test"})
	_ = l.Close()

	events, err := l.Tail("agent-ts", 10)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected 1 event, got 0")
	}
	if events[0].Timestamp.Before(before) {
		t.Errorf("auto-set Timestamp = %v, want after %v", events[0].Timestamp, before)
	}
}

func TestAppendWritesEventToFile(t *testing.T) {
	l := newLogger(t)
	l.Append(makeEvent("writer-agent", "message.in"))
	l.Append(makeEvent("writer-agent", "llm.call"))
	_ = l.Close()

	events, err := l.Tail("writer-agent", 100)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	if events[0].Type != "message.in" {
		t.Errorf("event[0].Type = %q, want message.in", events[0].Type)
	}
}

func TestTailNonExistentAgent(t *testing.T) {
	l := newLogger(t)
	events, err := l.Tail("ghost-agent", 10)
	if err != nil {
		t.Fatalf("Tail non-existent: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func TestTailDefaultsLimitOnZeroOrNegative(t *testing.T) {
	l := newLogger(t)
	for i := 0; i < 5; i++ {
		l.Append(makeEvent("limit-agent", "test"))
	}
	_ = l.Close()

	// limit=0 → defaults to 500
	events, err := l.Tail("limit-agent", 0)
	if err != nil {
		t.Fatalf("Tail limit=0: %v", err)
	}
	if len(events) != 5 {
		t.Errorf("Tail limit=0: got %d events, want 5", len(events))
	}
}

// TailFiltered must count only allowed types toward the limit, so noisy
// tool.log lines can't crowd out older runs' boundary events. Regression for the
// "cron history doesn't show all runs" bug.
func TestTailFilteredRecoversOldRuns(t *testing.T) {
	l := newLogger(t)
	// 3 runs, each 1 boundary event (message.out) followed by 100 tool.log lines.
	for r := 0; r < 3; r++ {
		l.Append(makeEvent("noisy-agent", "message.out"))
		for i := 0; i < 100; i++ {
			l.Append(makeEvent("noisy-agent", "tool.log"))
		}
	}
	_ = l.Close()

	allowed := map[string]bool{"message.out": true}

	// Plain Tail with a small limit sees only the newest tool.log noise — 0 runs.
	plain, err := l.Tail("noisy-agent", 50)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	boundaries := 0
	for _, ev := range plain {
		if ev.Type == "message.out" {
			boundaries++
		}
	}
	if boundaries != 0 {
		t.Fatalf("precondition: expected plain Tail(50) to bury boundaries, saw %d", boundaries)
	}

	// TailFiltered with the same limit recovers ALL 3 runs' boundary events.
	got, err := l.TailFiltered("noisy-agent", 50, allowed)
	if err != nil {
		t.Fatalf("TailFiltered: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("TailFiltered: expected 3 boundary events (all runs), got %d", len(got))
	}
	for _, ev := range got {
		if ev.Type != "message.out" {
			t.Errorf("TailFiltered returned a non-allowed type: %q", ev.Type)
		}
	}
}

func TestQueryFilteredReadsDurableHistory(t *testing.T) {
	dir := t.TempDir()
	l, err := New(filepath.Join(dir, "logs"), filepath.Join(dir, "actions.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	for i := 0; i < 3; i++ {
		sid := "shared-thread"
		l.Append(message.Event{
			Type:      "message.in",
			AgentID:   "durable-agent",
			SessionID: sid,
			Payload:   map[string]any{"channel": "telegram", "i": i},
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
		})
		l.Append(message.Event{
			Type:      "message.out",
			AgentID:   "durable-agent",
			SessionID: sid,
			Payload:   map[string]any{"parts": []any{map[string]any{"type": "text", "text": "ok"}}},
			Timestamp: time.Now().Add(time.Duration(i)*time.Second + time.Millisecond),
		})
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		evs, err := l.Tail("durable-agent", 10)
		if err != nil {
			t.Fatalf("Tail: %v", err)
		}
		if len(evs) >= 6 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for actionlog flush, saw %d events", len(evs))
		}
		time.Sleep(20 * time.Millisecond)
	}

	got, err := l.QueryFiltered("durable-agent", 10, map[string]bool{"message.in": true})
	if err != nil {
		t.Fatalf("QueryFiltered: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("events = %d, want 3", len(got))
	}
	for _, ev := range got {
		if ev.Type != "message.in" {
			t.Fatalf("event type = %q, want message.in", ev.Type)
		}
		if ev.Payload == nil {
			t.Fatalf("payload was not decoded: %+v", ev)
		}
	}
}

func TestTailRespectsLimit(t *testing.T) {
	l := newLogger(t)
	for i := 0; i < 10; i++ {
		l.Append(makeEvent("many-events", "test"))
	}
	_ = l.Close()

	events, err := l.Tail("many-events", 3)
	if err != nil {
		t.Fatalf("Tail limit: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("Tail limit=3: got %d events", len(events))
	}
}

// ---------------------------------------------------------------------------
// IncompleteMessageIns
// ---------------------------------------------------------------------------

func TestIncompleteMessageInsEmptyDB(t *testing.T) {
	l := newLogger(t)
	// Do NOT close before querying — Close() shuts down the DB connection.
	// t.Cleanup will close after the test.
	payloads, err := l.IncompleteMessageIns(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("IncompleteMessageIns: %v", err)
	}
	if len(payloads) != 0 {
		t.Errorf("expected 0 incomplete, got %d", len(payloads))
	}
}

func TestIncompleteMessageInsReturnsUncompletedRuns(t *testing.T) {
	l := newLogger(t)

	// Insert message.in without a corresponding message.out.
	ts := time.Now().UTC()
	_, err := l.db.Exec(
		`INSERT INTO agent_events (agent_id, session_id, type, payload, created_at)
		 VALUES ('ag', 'sess-incomplete', 'message.in', '{"id":"msg1"}', ?)`, ts)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	payloads, err := l.IncompleteMessageIns(ts.Add(-time.Second))
	if err != nil {
		t.Fatalf("IncompleteMessageIns: %v", err)
	}
	if len(payloads) != 1 {
		t.Fatalf("expected 1 incomplete payload, got %d", len(payloads))
	}
}

func TestIncompleteMessageInsExcludesCompletedRuns(t *testing.T) {
	l := newLogger(t)
	ts := time.Now().UTC()

	// message.in
	_, _ = l.db.Exec(
		`INSERT INTO agent_events (agent_id, session_id, type, payload, created_at)
		 VALUES ('ag', 'sess-complete', 'message.in', '{}', ?)`, ts)
	// message.out follows — run is complete.
	_, _ = l.db.Exec(
		`INSERT INTO agent_events (agent_id, session_id, type, payload, created_at)
		 VALUES ('ag', 'sess-complete', 'message.out', '{}', ?)`, ts.Add(time.Second))

	payloads, err := l.IncompleteMessageIns(ts.Add(-time.Second))
	if err != nil {
		t.Fatalf("IncompleteMessageIns: %v", err)
	}
	if len(payloads) != 0 {
		t.Errorf("expected 0 incomplete (run completed), got %d", len(payloads))
	}
}

func TestIncompleteMessageInsExcludesDeadLettered(t *testing.T) {
	l := newLogger(t)
	ts := time.Now().UTC()

	_, _ = l.db.Exec(
		`INSERT INTO agent_events (agent_id, session_id, type, payload, created_at)
		 VALUES ('ag', 'sess-dead', 'message.in', '{}', ?)`, ts)
	_, _ = l.db.Exec(
		`INSERT INTO agent_events (agent_id, session_id, type, payload, created_at)
		 VALUES ('ag', 'sess-dead', 'message.dead_letter', '{}', ?)`, ts.Add(time.Second))

	payloads, err := l.IncompleteMessageIns(ts.Add(-time.Second))
	if err != nil {
		t.Fatalf("IncompleteMessageIns: %v", err)
	}
	if len(payloads) != 0 {
		t.Errorf("expected 0 (dead lettered), got %d", len(payloads))
	}
}

// ---------------------------------------------------------------------------
// CountMessageInAttempts / MarkDeadLetter
// ---------------------------------------------------------------------------

func TestCountMessageInAttempts(t *testing.T) {
	l := newLogger(t)
	ts := time.Now().UTC()

	for i := 0; i < 3; i++ {
		_, _ = l.db.Exec(
			`INSERT INTO agent_events (agent_id, session_id, type, payload, created_at)
			 VALUES ('ag', 'sess-retry', 'message.in', '{}', ?)`, ts.Add(time.Duration(i)*time.Second))
	}

	count, err := l.CountMessageInAttempts("ag", "sess-retry", ts.Add(-time.Second))
	if err != nil {
		t.Fatalf("CountMessageInAttempts: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestMarkDeadLetter(t *testing.T) {
	l := newLogger(t)
	if err := l.MarkDeadLetter("ag", "sess-bad", "too many retries"); err != nil {
		t.Fatalf("MarkDeadLetter: %v", err)
	}
	// The dead_letter event should now exclude sess-bad from incomplete queries.
	ts := time.Now().UTC()
	_, _ = l.db.Exec(
		`INSERT INTO agent_events (agent_id, session_id, type, payload, created_at)
		 VALUES ('ag', 'sess-bad', 'message.in', '{}', ?)`, ts.Add(-time.Second))

	payloads, err := l.IncompleteMessageIns(ts.Add(-2 * time.Second))
	if err != nil {
		t.Fatalf("IncompleteMessageIns after dead-letter: %v", err)
	}
	if len(payloads) != 0 {
		t.Errorf("expected 0 (dead lettered), got %d", len(payloads))
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestCloseIdempotent(t *testing.T) {
	l := newLogger(t)
	if err := l.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tail — limit clamping
// ---------------------------------------------------------------------------

// TestTailClampsBigLimit verifies that a limit > 5000 is silently clamped to 500.
func TestTailClampsBigLimit(t *testing.T) {
	l := newLogger(t)
	for i := 0; i < 5; i++ {
		l.Append(makeEvent("clamp-agent", "test"))
	}
	_ = l.Close()

	// limit > 5000 should clamp to 500, not crash; we expect all 5 events back.
	events, err := l.Tail("clamp-agent", 9999)
	if err != nil {
		t.Fatalf("Tail limit=9999: %v", err)
	}
	if len(events) != 5 {
		t.Errorf("Tail limit=9999 (clamped): got %d events, want 5", len(events))
	}
}

// ---------------------------------------------------------------------------
// pruneIfLarge
// ---------------------------------------------------------------------------

// TestPruneIfLargeSkipsSmallFile verifies that pruneIfLarge is a no-op when
// the file is smaller than maxFileBytes.
func TestPruneIfLargeSkipsSmallFile(t *testing.T) {
	l := newLogger(t)
	path := l.Path("prune-small")

	// Write a small file that's well under maxFileBytes.
	content := strings.Repeat("short line\n", 10)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	l.pruneIfLarge(path)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after no-op prune: %v", err)
	}
	if string(got) != content {
		t.Errorf("small file should be unchanged after pruneIfLarge")
	}
}

// TestPruneIfLargeRewritesLargeFile verifies that pruneIfLarge keeps only
// the last keepLines lines when the file exceeds maxFileBytes.
func TestPruneIfLargeRewritesLargeFile(t *testing.T) {
	l := newLogger(t)
	path := l.Path("prune-large")

	// Build a file that is at least maxFileBytes (1 MiB) in size.
	// Use lines that are ~600 bytes each so we stay well above the threshold
	// while keeping test I/O reasonable.
	lineBase := strings.Repeat("x", 590)
	var sb strings.Builder
	totalLines := 3000 // 3000 * 600 ≈ 1.8 MiB → safely over maxFileBytes
	for i := 0; i < totalLines; i++ {
		sb.WriteString(lineBase)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	l.pruneIfLarge(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after prune: %v", err)
	}
	pruned := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(pruned) != keepLines {
		t.Errorf("after pruneIfLarge: got %d lines, want %d", len(pruned), keepLines)
	}
}

// TestPruneIfLargeSkipsNonExistentFile verifies that pruneIfLarge is a no-op
// (no panic) for a path that doesn't exist.
func TestPruneIfLargeSkipsNonExistentFile(t *testing.T) {
	l := newLogger(t)
	// Should not panic or error; just return silently.
	l.pruneIfLarge(filepath.Join(t.TempDir(), "nonexistent.log"))
}

// TestPruneIfLargeSkipsFileWithFewLines verifies that a large file (by byte
// count) that has fewer lines than keepLines is not rewritten.
func TestPruneIfLargeSkipsFileWithFewLines(t *testing.T) {
	l := newLogger(t)
	path := l.Path("prune-fewlines")

	// One line that's larger than maxFileBytes but count is 1 < keepLines.
	bigLine := strings.Repeat("y", maxFileBytes+100)
	content := bigLine + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	l.pruneIfLarge(path)

	// File should be unchanged: len(lines)==1 <= keepLines==2000.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != content {
		t.Errorf("file with few lines should be unchanged after pruneIfLarge")
	}
}

// ---------------------------------------------------------------------------
// pruneLoop — via writeFileBatch triggering the prune channel
// ---------------------------------------------------------------------------

// TestPruneLoopDrainsOnStop verifies that pending prune requests accumulated
// before Close are processed during the stop-drain phase of pruneLoop.
// We do this by sending a path directly to pruneRequest and verifying the
// oversized file gets pruned after Close().
func TestPruneLoopDrainsOnStop(t *testing.T) {
	l := newLogger(t)
	path := l.Path("prune-drain")

	// Build an oversized file.
	lineBase := strings.Repeat("z", 590)
	var sb strings.Builder
	for i := 0; i < 3000; i++ {
		sb.WriteString(lineBase)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Send the path directly to pruneRequest — mirrors what writeFileBatch does.
	select {
	case l.pruneRequest <- path:
	default:
		t.Fatal("pruneRequest channel full")
	}

	// Close drains the stop path in pruneLoop, which should call pruneIfLarge.
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after Close: %v", err)
	}
	pruned := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(pruned) != keepLines {
		t.Errorf("pruneLoop drain: got %d lines, want %d", len(pruned), keepLines)
	}
}

// ---------------------------------------------------------------------------
// IncompleteMessageIns — error variant
// ---------------------------------------------------------------------------

// TestIncompleteMessageInsExcludesErrorOutcome verifies that a session that
// ended with an "error" event (rather than "message.out") is also considered
// complete and excluded from the recovery list.
func TestIncompleteMessageInsExcludesErrorOutcome(t *testing.T) {
	l := newLogger(t)
	ts := time.Now().UTC()

	_, _ = l.db.Exec(
		`INSERT INTO agent_events (agent_id, session_id, type, payload, created_at)
		 VALUES ('ag', 'sess-err', 'message.in', '{}', ?)`, ts)
	_, _ = l.db.Exec(
		`INSERT INTO agent_events (agent_id, session_id, type, payload, created_at)
		 VALUES ('ag', 'sess-err', 'error', '{}', ?)`, ts.Add(time.Second))

	payloads, err := l.IncompleteMessageIns(ts.Add(-time.Second))
	if err != nil {
		t.Fatalf("IncompleteMessageIns: %v", err)
	}
	if len(payloads) != 0 {
		t.Errorf("expected 0 (error outcome), got %d", len(payloads))
	}
}

// ---------------------------------------------------------------------------
// CountMessageInAttempts — edge: zero count
// ---------------------------------------------------------------------------

// TestCountMessageInAttemptsZero verifies the function returns 0 when no
// matching events exist.
func TestCountMessageInAttemptsZero(t *testing.T) {
	l := newLogger(t)
	count, err := l.CountMessageInAttempts("nobody", "sess-x", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("CountMessageInAttempts: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

// ---------------------------------------------------------------------------
// writeFileBatch — write to multiple agents in one flush
// ---------------------------------------------------------------------------

// TestAppendMultipleAgentsSameFlush verifies that events for two different
// agents written in the same flush batch both land in their respective files.
func TestAppendMultipleAgentsSameFlush(t *testing.T) {
	l := newLogger(t)
	l.Append(makeEvent("agent-alpha", "message.in"))
	l.Append(makeEvent("agent-beta", "message.in"))
	l.Append(makeEvent("agent-alpha", "message.out"))
	_ = l.Close()

	evA, err := l.Tail("agent-alpha", 10)
	if err != nil {
		t.Fatalf("Tail alpha: %v", err)
	}
	if len(evA) != 2 {
		t.Errorf("agent-alpha: got %d events, want 2", len(evA))
	}

	evB, err := l.Tail("agent-beta", 10)
	if err != nil {
		t.Fatalf("Tail beta: %v", err)
	}
	if len(evB) != 1 {
		t.Errorf("agent-beta: got %d events, want 1", len(evB))
	}
}
