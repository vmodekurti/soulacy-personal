// page_test.go — MU-031 criterion 4's pagination, tested against the failure
// it exists to prevent rather than against its happy path.
//
// The happy path of any paginator works. What matters here is the boundary:
// an audit trail that repeats a record is one an investigator cannot count,
// and one that skips a record is worse — and both failures appear only when
// rows share a timestamp or when a write lands mid-walk, which is the normal
// state of an append-only table under load.
package actionlog

import (
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/message"
)

func pagingLogger(t *testing.T) *Logger {
	t.Helper()
	dir := t.TempDir()
	l, err := New(dir, filepath.Join(dir, "events.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// seed writes n events into one workspace, all sharing `at` when `collide` is
// true. Written through the durable path and flushed, so the test walks the
// same rows a reader would.
func seed(t *testing.T, l *Logger, workspaceID string, n int, at time.Time, collide bool) {
	t.Helper()
	for i := 0; i < n; i++ {
		stamp := at
		if !collide {
			stamp = at.Add(time.Duration(i) * time.Millisecond)
		}
		l.Append(message.Event{
			WorkspaceID: workspaceID, AgentID: "_system", Type: "admin.audit",
			SessionID: "req", Timestamp: stamp,
			Payload: map[string]any{"n": i},
		})
	}
	// The writer batches, so a read straight after an Append can race the
	// flush. Waiting for the rows to appear — rather than sleeping — keeps
	// the test deterministic on a loaded machine, and a test that sleeps just
	// long enough is one that starts failing on somebody else's laptop.
	waitForRows(t, l, workspaceID, n)
}

func waitForRows(t *testing.T, l *Logger, workspaceID string, want int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		// Counted ACROSS pages. A single page is capped at maxPageSize, so a
		// one-page count can never observe more than that — and a seed larger
		// than the cap would then time out reporting a durability failure that
		// is really a measurement bug.
		got := 0
		cursor := ""
		for {
			events, next, err := l.QueryEventsPageInWorkspace(workspaceID, "_system", "", maxPageSize, map[string]bool{"admin.audit": true}, cursor)
			if err != nil {
				t.Fatal(err)
			}
			got += len(events)
			if next == "" {
				break
			}
			cursor = next
		}
		if got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d events reached the durable store", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func walk(t *testing.T, l *Logger, workspaceID string, pageSize int) []int {
	t.Helper()
	var seen []int
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 200 {
			t.Fatal("pagination did not terminate")
		}
		events, next, err := l.QueryEventsPageInWorkspace(workspaceID, "_system", "", pageSize, map[string]bool{"admin.audit": true}, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range events {
			payload, _ := ev.Payload.(map[string]any)
			n, _ := payload["n"].(float64)
			seen = append(seen, int(n))
		}
		if next == "" {
			return seen
		}
		cursor = next
	}
}

// THE CASE THAT BREAKS A TIMESTAMP-ONLY CURSOR. Several audit records written
// in the same millisecond is ordinary — one request can produce more than one
// — and a cursor without the row-id tiebreaker either drops the second or
// returns it twice, depending on which side of the comparison it falls.
func TestEveryRecordIsSeenExactlyOnceWhenTimestampsCollide(t *testing.T) {
	l := pagingLogger(t)
	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	seed(t, l, "ws_a", 25, at, true)

	seen := walk(t, l, "ws_a", 4)
	if len(seen) != 25 {
		t.Fatalf("walked %d records over identical timestamps, want 25: %v", len(seen), seen)
	}
	counts := map[int]int{}
	for _, n := range seen {
		counts[n]++
	}
	for n := 0; n < 25; n++ {
		if counts[n] != 1 {
			t.Errorf("record %d was seen %d times", n, counts[n])
		}
	}
}

func TestPagesAreNewestFirstAndComplete(t *testing.T) {
	l := pagingLogger(t)
	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	seed(t, l, "ws_a", 12, at, false)

	seen := walk(t, l, "ws_a", 5)
	if len(seen) != 12 {
		t.Fatalf("walked %d of 12: %v", len(seen), seen)
	}
	// Newest first, and the PAGES in that order too. A paginated read whose
	// pages each ran oldest-first would have the pages themselves descending,
	// which reads as corrupted data.
	for i := 1; i < len(seen); i++ {
		if seen[i] >= seen[i-1] {
			t.Fatalf("record %d came after %d — the walk is not monotonically backwards: %v", seen[i], seen[i-1], seen)
		}
	}
	if seen[0] != 11 || seen[len(seen)-1] != 0 {
		t.Fatalf("walk did not span the trail: %v", seen)
	}
}

// An empty next cursor must mean "the end of the trail", not "the end of this
// page". A client that trusts a non-empty cursor loops forever on a page that
// happens to end exactly on the last row.
func TestTheFinalPageReturnsNoCursor(t *testing.T) {
	l := pagingLogger(t)
	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	seed(t, l, "ws_a", 10, at, false)

	// Page size divides the total exactly — the case where a naive
	// "full page means there is more" heuristic hands out a cursor that
	// returns nothing.
	_, next, err := l.QueryEventsPageInWorkspace("ws_a", "_system", "", 10, map[string]bool{"admin.audit": true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if next != "" {
		events, second, err := l.QueryEventsPageInWorkspace("ws_a", "_system", "", 10, map[string]bool{"admin.audit": true}, next)
		t.Fatalf("a page covering the whole trail handed out a cursor; following it returned %d events (next=%q, err=%v)",
			len(events), second, err)
	}
}

// The workspace predicate is inside the paginated query too. A pagination
// parameter that widened the scope would be the leak MU-019 spent a milestone
// removing, reintroduced through a new read path.
func TestAPageNeverCrossesWorkspaces(t *testing.T) {
	l := pagingLogger(t)
	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	seed(t, l, "ws_a", 5, at, false)
	seed(t, l, "ws_b", 5, at, false)

	if got := len(walk(t, l, "ws_a", 2)); got != 5 {
		t.Fatalf("ws_a walked %d records, want its own 5", got)
	}
	// And a cursor minted in one workspace does not carry into another: it is
	// a position, and positions in a scoped query are scoped.
	_, next, err := l.QueryEventsPageInWorkspace("ws_a", "_system", "", 2, map[string]bool{"admin.audit": true}, "")
	if err != nil || next == "" {
		t.Fatalf("no cursor to reuse: %v %q", err, next)
	}
	events, _, err := l.QueryEventsPageInWorkspace("ws_b", "_system", "", 100, map[string]bool{"admin.audit": true}, next)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.WorkspaceID != "ws_b" {
			t.Fatalf("ws_a's cursor pulled a ws_%s record into ws_b's page", ev.WorkspaceID)
		}
	}
}

// A malformed cursor is a client bug or tampering. Answering it with an empty
// page makes both look like the end of the data, and an investigator would
// conclude the trail stopped there.
func TestAnInvalidCursorIsAnErrorNotAnEmptyPage(t *testing.T) {
	l := pagingLogger(t)
	seed(t, l, "ws_a", 3, time.Now().UTC(), false)

	for _, bad := range []string{"not-base64!!", "YWJj", "djJ8MXwy"} {
		if _, _, err := l.QueryEventsPageInWorkspace("ws_a", "_system", "", 10, nil, bad); err == nil {
			t.Errorf("cursor %q was accepted", bad)
		}
	}
	// A cursor this package produced round-trips.
	_, next, err := l.QueryEventsPageInWorkspace("ws_a", "_system", "", 1, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.QueryEventsPageInWorkspace("ws_a", "_system", "", 1, nil, next); err != nil {
		t.Fatalf("a cursor this package emitted was rejected: %v", err)
	}
}

// The page size is bounded. "Paginated" with an unbounded page is the
// unpaginated read wearing a parameter.
func TestThePageSizeIsBounded(t *testing.T) {
	l := pagingLogger(t)
	// More than the bound, or the assertion cannot fail — seeding a handful
	// and asking for a million proves only that the store had a handful.
	// Mutation testing found exactly that: removing the clamp changed nothing.
	seed(t, l, "ws_a", maxPageSize+5, time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC), false)

	events, next, err := l.QueryEventsPageInWorkspace("ws_a", "_system", "", 1_000_000, map[string]bool{"admin.audit": true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) > maxPageSize {
		t.Fatalf("a page returned %d records past the %d bound", len(events), maxPageSize)
	}
	if next == "" {
		t.Fatal("a clamped page reported no further records, so the rest of the trail is unreachable")
	}
}
