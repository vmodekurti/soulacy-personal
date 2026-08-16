// Package runfollow is the reconnection policy behind `sy run follow`
// (MU-027 criterion 4: "reconnects with backoff and resumes from its last
// cursor").
//
// The command is thin; the correctness is here, and it is almost entirely
// about what happens when things go wrong. A follower that reconnects
// enthusiastically turns one gateway restart into a thundering herd from every
// CLI anyone left running; one that resumes from the wrong place either
// silently loses the events the user is waiting for or repeats them; and one
// that treats a lost cursor as "start over" prints a run's whole history again
// at the exact moment the user is watching for its last line.
package runfollow

import (
	"errors"
	"math"
	"math/rand"
	"strings"
	"time"
)

// Defaults for the backoff schedule.
//
// The first retry is fast because the overwhelmingly common cause is a proxy
// idling out a healthy connection, where a near-immediate reconnect is
// invisible to the user. The ceiling is 30s because beyond that a follower is
// no longer following — a person watching a run would have opened a new
// terminal.
const (
	DefaultInitialBackoff = 250 * time.Millisecond
	DefaultMaxBackoff     = 30 * time.Second
	DefaultJitter         = 0.3
)

// ErrGap reports that the cursor fell outside the server's retained window, so
// events were missed.
//
// Surfaced rather than swallowed. The follower CAN carry on from the live edge
// — and that is usually what the user wants — but they have to be told, because
// a stream that silently skips is indistinguishable from one that was quiet.
var ErrGap = errors.New("runfollow: events were missed while disconnected")

// State is a follower's position and reconnection schedule.
//
// Deliberately a plain value with no I/O: the reconnection policy is the part
// worth testing, and a policy entangled with a WebSocket can only be tested by
// running one.
type State struct {
	// Cursor is the last position the server acknowledged. Empty means "start
	// from the live edge", which is what a fresh follow does.
	Cursor string

	initial  time.Duration
	max      time.Duration
	jitter   float64
	attempts int
	random   func() float64
}

// New returns a follower state.
func New() *State {
	return &State{
		initial: DefaultInitialBackoff,
		max:     DefaultMaxBackoff,
		jitter:  DefaultJitter,
		random:  rand.Float64,
	}
}

// WithSchedule overrides the backoff parameters. Tests use it; so would an
// operator with an unusual network.
func (s *State) WithSchedule(initial, max time.Duration, jitter float64) *State {
	if initial > 0 {
		s.initial = initial
	}
	if max > 0 {
		s.max = max
	}
	if jitter >= 0 {
		s.jitter = jitter
	}
	return s
}

// Observed records a delivered event's cursor.
//
// Only ever moves forward. An out-of-order or stale frame — which a reconnect
// can produce, because the server replays from the last position it was told
// about — must not rewind the follower, or the next reconnect asks for
// everything again.
func (s *State) Observed(cursor string) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return
	}
	if !after(cursor, s.Cursor) {
		return
	}
	s.Cursor = cursor
}

// Connected resets the backoff.
//
// Reset on a SUCCESSFUL connection, not on an attempt. Resetting per attempt
// makes the schedule constant: a server that accepts a connection and drops it
// immediately would be retried every 250ms forever, which is the thundering
// herd the backoff exists to prevent.
func (s *State) Connected() {
	s.attempts = 0
}

// Backoff returns how long to wait before the next reconnect attempt.
//
// Exponential with full jitter. Jitter is not decoration: without it, every
// follower disconnected by the same gateway restart retries at the same
// instant, and the synchronised retries are indistinguishable from an attack.
func (s *State) Backoff() time.Duration {
	delay := float64(s.initial) * math.Pow(2, float64(s.attempts))
	s.attempts++
	if delay > float64(s.max) || math.IsInf(delay, 1) {
		delay = float64(s.max)
	}
	if s.jitter > 0 {
		random := s.random
		if random == nil {
			random = rand.Float64
		}
		// Jitter DOWNWARD only. Adding above the ceiling would let the delay
		// exceed a bound the operator set, and a ceiling that is sometimes
		// exceeded is not a ceiling.
		delay -= delay * s.jitter * random()
	}
	if delay < 0 {
		delay = 0
	}
	return time.Duration(delay)
}

// Attempts is how many consecutive reconnects have been tried, for a status
// line the user can read.
func (s *State) Attempts() int { return s.attempts }

// ResumeFrom returns the cursor to present on reconnect, and whether this is a
// resume or a fresh start.
func (s *State) ResumeFrom() (string, bool) {
	return s.Cursor, s.Cursor != ""
}

// HandleGap decides what to do when the server reports the cursor fell outside
// its retained window.
//
// Drops to the live edge and returns ErrGap for the caller to report. NOT
// "start from the beginning": a follower that re-reads a long run's whole
// history at the moment the user is watching for its final line has turned a
// recoverable hiccup into a screenful of noise. And not "give up" either — the
// events the user is waiting for are the ones still coming.
func (s *State) HandleGap() error {
	s.Cursor = ""
	return ErrGap
}

// after reports whether cursor is strictly newer than current.
//
// Cursors are "<workspace>:<sequence>", so ordering is numeric on the suffix,
// not lexicographic — string comparison puts "ws:10" before "ws:9" and a
// follower using it would rewind nine events on every reconnect after the
// tenth.
func after(cursor, current string) bool {
	if current == "" {
		return true
	}
	currentWorkspace, currentSeq, ok := split(current)
	if !ok {
		return true
	}
	cursorWorkspace, cursorSeq, ok := split(cursor)
	if !ok {
		return false
	}
	if cursorWorkspace != currentWorkspace {
		// A cursor from another workspace is not comparable and must not
		// become this follower's position — the server would refuse it on the
		// next reconnect, stranding the follower at the live edge forever.
		return false
	}
	return cursorSeq > currentSeq
}

func split(cursor string) (string, uint64, bool) {
	at := strings.LastIndex(cursor, ":")
	if at <= 0 || at == len(cursor)-1 {
		return "", 0, false
	}
	var seq uint64
	for _, r := range cursor[at+1:] {
		if r < '0' || r > '9' {
			return "", 0, false
		}
		seq = seq*10 + uint64(r-'0')
	}
	return cursor[:at], seq, true
}
