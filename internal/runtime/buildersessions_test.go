package runtime

import (
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
)

// Builder sessions are keyed by a CLIENT-SUPPLIED id and were only ever removed
// by an explicit DELETE. LastActive was written on every turn and read nowhere,
// and the package doc promised a 2-hour expiry that did not exist. A caller
// holding builder:write could pin unbounded memory just by varying the id.
//
// These assert the SWEEP RUNS, not merely that a sweep function exists. The
// first version of this fix had a correct sweepBuilderSessions and no call to
// it; the function was dead code, and nothing failed.
func TestBuilderSessions_IdleSessionsAreEvicted(t *testing.T) {
	e := newMinimalEngine(t)

	stale := e.getOrCreateBuilderSession("stale")
	stale.mu.Lock()
	stale.LastActive = time.Now().Add(-builderSessionTTL - time.Hour)
	stale.mu.Unlock()

	// Any subsequent use is what drives the sweep.
	e.getOrCreateBuilderSession("fresh")

	if _, ok := e.builderSessions.Load("stale"); ok {
		t.Error("a session idle past the TTL is still held in memory")
	}
	if _, ok := e.builderSessions.Load("fresh"); !ok {
		t.Error("the live session was evicted")
	}
}

// The TTL alone bounds nothing: a caller creates sessions far faster than two
// hours retires them. The count cap is what actually holds memory down.
func TestBuilderSessions_CountIsCapped(t *testing.T) {
	e := newMinimalEngine(t)

	for i := 0; i < maxBuilderSessions+250; i++ {
		e.getOrCreateBuilderSession(sessionKey(i))
	}

	n := 0
	e.builderSessions.Range(func(_, _ any) bool { n++; return true })
	if n > maxBuilderSessions {
		t.Fatalf("%d builder sessions retained, over the cap of %d", n, maxBuilderSessions)
	}
	if n == 0 {
		t.Fatal("every session was evicted, including the one just created")
	}
}

// Touching a session must keep it alive — otherwise a long builder conversation
// gets swept out from under the user mid-flow.
func TestBuilderSessions_ActiveSessionSurvives(t *testing.T) {
	e := newMinimalEngine(t)
	e.getOrCreateBuilderSession("mine")
	for i := 0; i < 50; i++ {
		e.getOrCreateBuilderSession(sessionKey(i))
		e.getOrCreateBuilderSession("mine") // still in use
	}
	if _, ok := e.builderSessions.Load("mine"); !ok {
		t.Fatal("a session in active use was evicted")
	}
}

// History is appended BEFORE the LLM call, so even a failing provider costs
// memory per request. Unbounded growth in a single session is the other half of
// the same problem.
func TestBuilderSessions_HistoryIsBounded(t *testing.T) {
	e := newMinimalEngine(t)
	sess := e.getOrCreateBuilderSession("chatty")

	sess.mu.Lock()
	for i := 0; i < maxBuilderHistoryTurns*3; i++ {
		sess.History = append(sess.History, llm.ChatMessage{Role: "user", Content: "x"})
	}
	trimBuilderHistoryLocked(sess)
	got := len(sess.History)
	sess.mu.Unlock()

	if got > maxBuilderHistoryTurns {
		t.Fatalf("history kept %d turns, over the cap of %d", got, maxBuilderHistoryTurns)
	}
}

func sessionKey(i int) string {
	return "sess-" + time.Unix(int64(i), 0).UTC().Format("150405.000000000")
}
