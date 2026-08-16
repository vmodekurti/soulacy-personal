// follow_test.go — MU-027 criterion 4: reconnect with backoff, resume from the
// last cursor, and be honest when events were missed.
package runfollow

import (
	"errors"
	"testing"
	"time"
)

func fixed(state *State, value float64) *State {
	state.random = func() float64 { return value }
	return state
}

// Backoff grows, and it stops growing at the ceiling. A follower that keeps
// doubling is no longer following.
func TestBackoffGrowsAndStopsAtTheCeiling(t *testing.T) {
	state := fixed(New().WithSchedule(100*time.Millisecond, time.Second, 0), 0)
	want := []time.Duration{100, 200, 400, 800, 1000, 1000, 1000}
	for i, expected := range want {
		if got := state.Backoff(); got != expected*time.Millisecond {
			t.Fatalf("attempt %d: %v, want %v", i, got, expected*time.Millisecond)
		}
	}
}

// Jitter is not decoration. Without it every follower disconnected by one
// gateway restart retries at the same instant, and synchronised retries are
// indistinguishable from an attack.
func TestJitterSpreadsRetriesAndNeverExceedsTheCeiling(t *testing.T) {
	for _, random := range []float64{0, 0.5, 0.999} {
		state := fixed(New().WithSchedule(time.Second, time.Second, 0.3), random)
		got := state.Backoff()
		if got > time.Second {
			t.Fatalf("random=%v produced %v, above the ceiling", random, got)
		}
		if got < 700*time.Millisecond {
			t.Fatalf("random=%v produced %v, below the jitter floor", random, got)
		}
	}
	// Different random draws produce different delays — that is the point.
	low := fixed(New().WithSchedule(time.Second, time.Second, 0.3), 0).Backoff()
	high := fixed(New().WithSchedule(time.Second, time.Second, 0.3), 0.9).Backoff()
	if low == high {
		t.Fatal("jitter had no effect; every follower would retry in lockstep")
	}
}

// Reset on a SUCCESSFUL connection, not per attempt. Resetting per attempt
// makes the schedule constant, so a server accepting and immediately dropping
// connections is retried at the floor forever.
func TestBackoffResetsOnlyAfterASuccessfulConnection(t *testing.T) {
	state := fixed(New().WithSchedule(100*time.Millisecond, 10*time.Second, 0), 0)
	state.Backoff()
	state.Backoff()
	if state.Attempts() != 2 {
		t.Fatalf("attempts = %d", state.Attempts())
	}
	state.Connected()
	if state.Attempts() != 0 {
		t.Fatal("a successful connection did not reset the schedule")
	}
	if got := state.Backoff(); got != 100*time.Millisecond {
		t.Fatalf("after reconnecting, backoff = %v, want the floor", got)
	}
}

// The cursor only moves forward. A reconnect replays from the last position
// the server was told about, so stale frames arrive by design — and rewinding
// on one means asking for them all again next time.
func TestTheCursorOnlyMovesForward(t *testing.T) {
	state := New()
	state.Observed("ws_a:5")
	state.Observed("ws_a:3")
	if state.Cursor != "ws_a:5" {
		t.Fatalf("cursor = %q; a stale frame rewound the follower", state.Cursor)
	}
	state.Observed("ws_a:9")
	if state.Cursor != "ws_a:9" {
		t.Fatalf("cursor = %q", state.Cursor)
	}
	// Empty and malformed frames are ignored rather than adopted.
	for _, bad := range []string{"", "   ", "garbage", "ws_a:", ":7"} {
		state.Observed(bad)
		if state.Cursor != "ws_a:9" {
			t.Fatalf("cursor = %q after observing %q", state.Cursor, bad)
		}
	}
}

// Cursors order numerically on the sequence, not lexicographically. String
// comparison puts "ws:10" before "ws:9", so a follower using it would rewind
// nine events on every reconnect after the tenth.
func TestCursorOrderingIsNumericNotLexicographic(t *testing.T) {
	state := New()
	state.Observed("ws_a:9")
	state.Observed("ws_a:10")
	if state.Cursor != "ws_a:10" {
		t.Fatalf("cursor = %q; ordering is lexicographic", state.Cursor)
	}
	state.Observed("ws_a:9")
	if state.Cursor != "ws_a:10" {
		t.Fatalf("cursor = %q; a lower sequence won", state.Cursor)
	}
}

// A cursor from another workspace is not comparable, and adopting one would
// strand the follower: the server refuses a foreign cursor, so every
// subsequent reconnect starts from the live edge.
func TestAForeignCursorIsNotAdopted(t *testing.T) {
	state := New()
	state.Observed("ws_a:5")
	state.Observed("ws_other:99")
	if state.Cursor != "ws_a:5" {
		t.Fatalf("cursor = %q; a foreign cursor was adopted", state.Cursor)
	}
}

// A fresh follow starts at the live edge; after seeing anything it resumes.
func TestResumeReportsWhetherThereIsAPositionToResumeFrom(t *testing.T) {
	state := New()
	if cursor, resuming := state.ResumeFrom(); resuming || cursor != "" {
		t.Fatalf("a fresh follower claimed to resume from %q", cursor)
	}
	state.Observed("ws_a:1")
	if cursor, resuming := state.ResumeFrom(); !resuming || cursor != "ws_a:1" {
		t.Fatalf("resume = %q, %v", cursor, resuming)
	}
}

// A gap drops to the live edge and SAYS SO. Silently carrying on makes a
// stream that skipped indistinguishable from one that was quiet; starting over
// reprints a long run's history at the moment the user is watching for its
// last line; giving up abandons the events they are waiting for.
func TestAGapDropsToTheLiveEdgeAndIsReported(t *testing.T) {
	state := New()
	state.Observed("ws_a:42")

	err := state.HandleGap()
	if !errors.Is(err, ErrGap) {
		t.Fatalf("err = %v, want ErrGap", err)
	}
	if state.Cursor != "" {
		t.Fatalf("cursor = %q after a gap, want the live edge", state.Cursor)
	}
	if _, resuming := state.ResumeFrom(); resuming {
		t.Fatal("the follower still claims a resume position after a gap")
	}
	// And it keeps following: the next event is adopted normally.
	state.Observed("ws_a:100")
	if state.Cursor != "ws_a:100" {
		t.Fatal("the follower stopped tracking after a gap")
	}
}

// A long disconnection must not overflow into a negative or absurd delay.
func TestAVeryLongDisconnectionStaysBounded(t *testing.T) {
	state := fixed(New().WithSchedule(time.Second, 30*time.Second, 0), 0)
	for i := 0; i < 200; i++ {
		got := state.Backoff()
		if got < 0 || got > 30*time.Second {
			t.Fatalf("attempt %d produced %v", i, got)
		}
	}
}
