// rotation_test.go — tests for PERF-3 (size-based rotation of per-agent JSONL
// logs) and PERF-4 (streaming backwards Tail).
package actionlog

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/message"
)

// newLoggerWithRotation builds a Logger with a tiny rotation threshold so tests
// can trigger rotation cheaply.
func newLoggerWithRotation(t *testing.T, maxBytes int64, maxBackups int) *Logger {
	t.Helper()
	dir := t.TempDir()
	l, err := New(dir, filepath.Join(dir, "events.db"), zap.NewNop(),
		WithRotation(maxBytes, maxBackups))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// gunzipLines decompresses a .gz backup and returns its non-blank lines.
func gunzipLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open gz %s: %v", path, err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader %s: %v", path, err)
	}
	defer gr.Close()
	data, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read gz %s: %v", path, err)
	}
	var out []string
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// PERF-3: rotation
// ---------------------------------------------------------------------------

// TestRotateIfLargeTriggersAndGzips verifies that an oversized active log is
// rotated into a single gzipped backup (.1.gz) and the active file is emptied.
func TestRotateIfLargeTriggersAndGzips(t *testing.T) {
	l := newLoggerWithRotation(t, 4096, 3)
	path := l.Path("rot-agent")

	// Write a recognisable, well-over-threshold file.
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&sb, "line-%04d-%s\n", i, strings.Repeat("p", 40))
	}
	content := sb.String()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	l.rotateIfLarge(path)

	// Active file must now exist and be empty.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("active file missing after rotate: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("active file size = %d, want 0 after rotation", info.Size())
	}

	// .1.gz must exist and decompress to the original content's lines.
	gz1 := l.rotatedPath(path, 1)
	if !fileExists(gz1) {
		t.Fatalf("expected backup %s to exist", gz1)
	}
	got := gunzipLines(t, gz1)
	wantLines := strings.Count(content, "\n")
	if len(got) != wantLines {
		t.Errorf("backup line count = %d, want %d", len(got), wantLines)
	}
	if got[0] != "line-0000-"+strings.Repeat("p", 40) {
		t.Errorf("backup first line = %q", got[0])
	}
}

// TestRotateSkipsSmallFile verifies rotation is a no-op under threshold.
func TestRotateSkipsSmallFile(t *testing.T) {
	l := newLoggerWithRotation(t, 1<<20, 3)
	path := l.Path("rot-small")
	content := "a\nb\nc\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	l.rotateIfLarge(path)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != content {
		t.Errorf("small file changed by rotateIfLarge")
	}
	if fileExists(l.rotatedPath(path, 1)) {
		t.Errorf("unexpected backup created for small file")
	}
}

// TestRotateBoundsBackupCount verifies that repeated rotations keep at most
// maxBackups gzipped files, evicting the oldest, and that backups are gzipped.
func TestRotateBoundsBackupCount(t *testing.T) {
	const maxBackups = 3
	l := newLoggerWithRotation(t, 1024, maxBackups)
	path := l.Path("rot-many")

	// Rotate 6 times; each round writes a uniquely-marked oversized file so we
	// can prove ordering (newest in .1.gz, oldest evicted).
	for round := 0; round < 6; round++ {
		var sb strings.Builder
		fmt.Fprintf(&sb, "ROUND-%d\n", round)
		sb.WriteString(strings.Repeat("x", 2048))
		sb.WriteByte('\n')
		if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
			t.Fatalf("WriteFile round %d: %v", round, err)
		}
		l.rotateIfLarge(path)
	}

	// Count surviving backups: must be <= maxBackups, and none beyond.
	present := 0
	for i := 1; i <= maxBackups; i++ {
		if fileExists(l.rotatedPath(path, i)) {
			present++
		}
	}
	if present != maxBackups {
		t.Errorf("retained backups = %d, want %d", present, maxBackups)
	}
	if fileExists(l.rotatedPath(path, maxBackups+1)) {
		t.Errorf("backup beyond retention exists: %s", l.rotatedPath(path, maxBackups+1))
	}

	// .1.gz holds the most recent rotation (ROUND-5); .3.gz the oldest retained
	// (ROUND-3). ROUND-0..2 must have been evicted.
	newest := gunzipLines(t, l.rotatedPath(path, 1))
	if newest[0] != "ROUND-5" {
		t.Errorf(".1.gz first line = %q, want ROUND-5", newest[0])
	}
	oldest := gunzipLines(t, l.rotatedPath(path, maxBackups))
	if oldest[0] != "ROUND-3" {
		t.Errorf(".%d.gz first line = %q, want ROUND-3", maxBackups, oldest[0])
	}
}

// TestRotateDisabled verifies that zero/negative config disables rotation.
func TestRotateDisabled(t *testing.T) {
	l := newLoggerWithRotation(t, 1<<20, 5) // valid config...
	// ...but force-disable directly to exercise the guard.
	l.maxRotateBytes = 0
	path := l.Path("rot-off")
	content := strings.Repeat("q", 9000) + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	l.rotateIfLarge(path)
	if fileExists(l.rotatedPath(path, 1)) {
		t.Errorf("rotation happened despite being disabled")
	}
}

// ---------------------------------------------------------------------------
// PERF-4: streaming (backwards) Tail correctness
// ---------------------------------------------------------------------------

// writeJSONLines writes count events to an agent's log file directly (bypassing
// the async writer) and returns the path.
func writeJSONLines(t *testing.T, l *Logger, agentID string, count int) string {
	t.Helper()
	path := l.Path(agentID)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	for i := 0; i < count; i++ {
		ev := message.Event{AgentID: agentID, SessionID: "s", Type: fmt.Sprintf("ev-%d", i)}
		b, _ := json.Marshal(ev)
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return path
}

// TestTailBackwardsReturnsLastNInOrder writes far more lines than one read
// block (64 KiB) and verifies Tail returns the LAST `limit` events oldest-first.
func TestTailBackwardsReturnsLastNInOrder(t *testing.T) {
	l := newLogger(t)
	const total = 5000
	writeJSONLines(t, l, "tail-big", total)

	const limit = 1200
	events, err := l.Tail("tail-big", limit)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(events) != limit {
		t.Fatalf("got %d events, want %d", len(events), limit)
	}
	// Oldest-first: first returned event is index total-limit, last is total-1.
	wantFirst := fmt.Sprintf("ev-%d", total-limit)
	wantLast := fmt.Sprintf("ev-%d", total-1)
	if events[0].Type != wantFirst {
		t.Errorf("first event = %q, want %q", events[0].Type, wantFirst)
	}
	if events[len(events)-1].Type != wantLast {
		t.Errorf("last event = %q, want %q", events[len(events)-1].Type, wantLast)
	}
	// Verify strict ordering throughout.
	for i, ev := range events {
		want := fmt.Sprintf("ev-%d", total-limit+i)
		if ev.Type != want {
			t.Fatalf("event[%d] = %q, want %q", i, ev.Type, want)
			break
		}
	}
}

// TestTailBackwardsFewerThanLimit verifies that when the file has fewer lines
// than the limit, all of them come back in order.
func TestTailBackwardsFewerThanLimit(t *testing.T) {
	l := newLogger(t)
	writeJSONLines(t, l, "tail-few", 7)
	events, err := l.Tail("tail-few", 100)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(events) != 7 {
		t.Fatalf("got %d, want 7", len(events))
	}
	if events[0].Type != "ev-0" || events[6].Type != "ev-6" {
		t.Errorf("ordering wrong: %q .. %q", events[0].Type, events[6].Type)
	}
}

// TestTailBackwardsHandlesNoTrailingNewline verifies a file whose last line has
// no trailing '\n' is still tailed correctly (last line included).
func TestTailBackwardsHandlesNoTrailingNewline(t *testing.T) {
	l := newLogger(t)
	path := l.Path("tail-nonewline")
	var sb strings.Builder
	for i := 0; i < 3; i++ {
		ev := message.Event{AgentID: "tail-nonewline", Type: fmt.Sprintf("ev-%d", i)}
		b, _ := json.Marshal(ev)
		sb.Write(b)
		if i < 2 {
			sb.WriteByte('\n')
		}
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	events, err := l.Tail("tail-nonewline", 10)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d, want 3 (last line lacked newline)", len(events))
	}
	if events[2].Type != "ev-2" {
		t.Errorf("last event = %q, want ev-2", events[2].Type)
	}
}

// TestTailBackwardsSkipsBlankLines verifies blank lines interspersed in the log
// are ignored and don't count toward the limit.
func TestTailBackwardsSkipsBlankLines(t *testing.T) {
	l := newLogger(t)
	path := l.Path("tail-blanks")
	var sb strings.Builder
	for i := 0; i < 4; i++ {
		ev := message.Event{AgentID: "tail-blanks", Type: fmt.Sprintf("ev-%d", i)}
		b, _ := json.Marshal(ev)
		sb.Write(b)
		sb.WriteString("\n\n") // extra blank line after each
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	events, err := l.Tail("tail-blanks", 10)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4 (blanks skipped)", len(events))
	}
	if events[0].Type != "ev-0" || events[3].Type != "ev-3" {
		t.Errorf("ordering: %q .. %q", events[0].Type, events[3].Type)
	}
}

// TestTailBackwardsLimitOne is an edge case: a single-line tail of a multi-block
// file must return only the very last event.
func TestTailBackwardsLimitOne(t *testing.T) {
	l := newLogger(t)
	const total = 3000
	writeJSONLines(t, l, "tail-one", total)
	events, err := l.Tail("tail-one", 1)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d, want 1", len(events))
	}
	if events[0].Type != fmt.Sprintf("ev-%d", total-1) {
		t.Errorf("event = %q, want ev-%d", events[0].Type, total-1)
	}
}
