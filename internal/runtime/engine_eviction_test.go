// engine_eviction_test.go — tests for PERF-1 (session eviction) and PERF-2
// (history windowing). Pure-Go: no real LLM, no network. The sweep and trim
// logic are driven directly.
package runtime

import (
	"sync"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/storage"
)

// ---------------------------------------------------------------------------
// PERF-1 — session eviction
// ---------------------------------------------------------------------------

func sessionCount(e *Engine) int {
	n := 0
	e.sessions.Range(func(_, _ any) bool { n++; return true })
	return n
}

// TestSessionEviction_IdlePastTTLIsEvicted proves a session whose lastAccess is
// older than the TTL is removed by the sweep.
func TestSessionEviction_IdlePastTTLIsEvicted(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetSessionEviction(time.Hour, 1000)

	sess := e.getOrCreateSession("sess-idle", "agent-a")
	// Backdate lastAccess to well past the TTL.
	sess.mu.Lock()
	sess.lastAccess = time.Now().UTC().Add(-2 * time.Hour)
	sess.mu.Unlock()

	if got := sessionCount(e); got != 1 {
		t.Fatalf("precondition: expected 1 session, got %d", got)
	}

	evicted := e.sweepSessions(time.Now().UTC())
	if evicted != 1 {
		t.Errorf("expected 1 eviction, got %d", evicted)
	}
	if got := sessionCount(e); got != 0 {
		t.Errorf("idle session should have been evicted, %d remain", got)
	}
}

// TestSessionEviction_FreshSessionSurvives proves a recently-accessed session
// (within TTL) is NOT evicted.
func TestSessionEviction_FreshSessionSurvives(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetSessionEviction(time.Hour, 1000)

	_ = e.getOrCreateSession("sess-fresh", "agent-a") // lastAccess = now

	evicted := e.sweepSessions(time.Now().UTC())
	if evicted != 0 {
		t.Errorf("expected 0 evictions for fresh session, got %d", evicted)
	}
	if got := sessionCount(e); got != 1 {
		t.Errorf("fresh session should survive, got %d sessions", got)
	}
}

// TestSessionEviction_ActiveSessionNeverEvicted proves a session that is
// mid-conversation (inUse > 0) is never evicted, even when far past the TTL.
func TestSessionEviction_ActiveSessionNeverEvicted(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetSessionEviction(time.Hour, 1000)

	sess := e.getOrCreateSession("sess-active", "agent-a")
	sess.mu.Lock()
	sess.lastAccess = time.Now().UTC().Add(-10 * time.Hour) // way past TTL
	sess.inUse = 1                                          // mid-conversation
	sess.mu.Unlock()

	evicted := e.sweepSessions(time.Now().UTC())
	if evicted != 0 {
		t.Errorf("active session must not be evicted, but %d were", evicted)
	}
	if got := sessionCount(e); got != 1 {
		t.Errorf("active session should survive, got %d sessions", got)
	}
}

// TestSessionEviction_MaxCountCapEvictsOldestIdle proves the count cap drops
// the oldest-idle sessions when the live count exceeds maxSessions, while
// keeping the newest ones and never touching an in-use session.
func TestSessionEviction_MaxCountCapEvictsOldestIdle(t *testing.T) {
	e := newMinimalEngine(t)
	// TTL very large so only the count cap fires; cap = 2.
	e.SetSessionEviction(100*time.Hour, 2)

	base := time.Now().UTC()
	// Create 4 sessions with staggered (recent, within-TTL) access times.
	mk := func(id string, ageMinutes int) *Session {
		s := e.getOrCreateSession(id, "agent-a")
		s.mu.Lock()
		s.lastAccess = base.Add(time.Duration(-ageMinutes) * time.Minute)
		s.mu.Unlock()
		return s
	}
	mk("newest", 1)
	mk("recent", 5)
	mk("older", 30)
	mk("oldest", 60)

	if got := sessionCount(e); got != 4 {
		t.Fatalf("precondition: expected 4 sessions, got %d", got)
	}

	evicted := e.sweepSessions(base)
	if evicted != 2 {
		t.Errorf("expected 2 evictions to reach cap of 2, got %d", evicted)
	}
	if got := sessionCount(e); got != 2 {
		t.Errorf("expected 2 sessions after cap eviction, got %d", got)
	}
	// The two newest must survive; the two oldest must be gone.
	if _, ok := e.sessions.Load("agent-a|newest"); !ok {
		t.Error("newest session should have survived the cap")
	}
	if _, ok := e.sessions.Load("agent-a|recent"); !ok {
		t.Error("recent session should have survived the cap")
	}
	if _, ok := e.sessions.Load("agent-a|oldest"); ok {
		t.Error("oldest session should have been evicted by the cap")
	}
}

// fakeArchive is a minimal storage.MemoryBackend that records Archive calls so
// we can prove persist-then-evict.
type fakeArchive struct {
	mu      sync.Mutex
	entries []memory.Entry
}

func (a *fakeArchive) Archive(e memory.Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	return nil
}
func (a *fakeArchive) Search(string, string, int) ([]memory.Entry, error) { return nil, nil }
func (a *fakeArchive) ReadByScope(string, string, memory.Scope, int) ([]memory.Entry, error) {
	return nil, nil
}
func (a *fakeArchive) ReadGlobal(string, int) ([]memory.Entry, error)  { return nil, nil }
func (a *fakeArchive) Prune(string, time.Time) (int64, error)          { return 0, nil }
func (a *fakeArchive) Close() error                                    { return nil }

var _ storage.MemoryBackend = (*fakeArchive)(nil)

// TestSessionEviction_PersistsBeforeEvict proves that when an archive is wired,
// evicting a session first persists its non-system history.
func TestSessionEviction_PersistsBeforeEvict(t *testing.T) {
	e := newMinimalEngine(t)
	arch := &fakeArchive{}
	e.archive = arch
	e.SetSessionEviction(time.Hour, 1000)

	sess := e.getOrCreateSession("sess-persist", "agent-p")
	sess.mu.Lock()
	sess.History = []llm.ChatMessage{
		{Role: "system", Content: "system prompt"}, // should NOT be persisted
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}
	sess.lastAccess = time.Now().UTC().Add(-2 * time.Hour)
	sess.mu.Unlock()

	if got := e.sweepSessions(time.Now().UTC()); got != 1 {
		t.Fatalf("expected 1 eviction, got %d", got)
	}
	if got := sessionCount(e); got != 0 {
		t.Fatalf("session should be evicted, %d remain", got)
	}

	arch.mu.Lock()
	defer arch.mu.Unlock()
	if len(arch.entries) != 2 {
		t.Fatalf("expected 2 persisted (non-system) entries, got %d", len(arch.entries))
	}
	for _, en := range arch.entries {
		if en.AgentID != "agent-p" || en.SessionID != "sess-persist" {
			t.Errorf("persisted entry has wrong ids: %+v", en)
		}
		if en.Scope != memory.ScopeSession {
			t.Errorf("persisted entry scope = %q, want session", en.Scope)
		}
	}
}

// TestStartStopSessionEviction exercises the goroutine lifecycle: it must start
// without panicking, run at least one sweep, and stop cleanly (and be safe to
// stop twice).
func TestStartStopSessionEviction(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetSessionEviction(time.Hour, 1000)

	// Backdate a session so the timed sweep evicts it.
	sess := e.getOrCreateSession("sess-timed", "agent-t")
	sess.mu.Lock()
	sess.lastAccess = time.Now().UTC().Add(-2 * time.Hour)
	sess.mu.Unlock()

	e.StartSessionEviction(10 * time.Millisecond)
	// Give the ticker a few cycles.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sessionCount(e) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := sessionCount(e); got != 0 {
		t.Errorf("timed sweep should have evicted the idle session, %d remain", got)
	}
	e.StopSessionEviction()
	e.StopSessionEviction() // must be safe to call twice
}

// ---------------------------------------------------------------------------
// PERF-2 — history windowing
// ---------------------------------------------------------------------------

func TestTrimHistory_NoSystemPrompt(t *testing.T) {
	var h []llm.ChatMessage
	for i := 0; i < 500; i++ {
		h = append(h, llm.ChatMessage{Role: "user", Content: "msg"})
	}
	out := trimHistory(h, 100)
	if len(out) != 100 {
		t.Fatalf("expected 100 messages after trim, got %d", len(out))
	}
}

func TestTrimHistory_PreservesLeadingSystemPrompt(t *testing.T) {
	h := []llm.ChatMessage{{Role: "system", Content: "SYSTEM PROMPT"}}
	for i := 0; i < 500; i++ {
		h = append(h, llm.ChatMessage{Role: "user", Content: "msg"})
	}
	out := trimHistory(h, 100)
	// system prompt + 100 body = 101.
	if len(out) != 101 {
		t.Fatalf("expected 101 (system + 100 body), got %d", len(out))
	}
	if out[0].Role != "system" || out[0].Content != "SYSTEM PROMPT" {
		t.Errorf("system prompt must survive trimming, got %+v", out[0])
	}
	// The newest body message must be retained (oldest-first trim).
	if out[len(out)-1].Role != "user" {
		t.Errorf("last message should be a user turn, got %+v", out[len(out)-1])
	}
}

func TestTrimHistory_BelowCapUnchanged(t *testing.T) {
	h := []llm.ChatMessage{
		{Role: "system", Content: "s"},
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
	}
	out := trimHistory(h, 100)
	if len(out) != 3 {
		t.Errorf("history below cap should be unchanged, got %d", len(out))
	}
}

func TestTrimHistory_ZeroCapDisablesTrimming(t *testing.T) {
	h := make([]llm.ChatMessage, 200)
	out := trimHistory(h, 0)
	if len(out) != 200 {
		t.Errorf("cap<=0 should disable trimming, got %d", len(out))
	}
}

// TestAppendHistoryLocked_500TurnsWindowed drives a session to 500 turns through
// the centralized append helper and asserts the window holds and the system
// prompt survives.
func TestAppendHistoryLocked_500TurnsWindowed(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetMaxHistoryTurns(100)

	sess := &Session{ID: "s", AgentID: "a"}
	sess.mu.Lock()
	// Seed with a system prompt that must never be trimmed.
	sess.History = []llm.ChatMessage{{Role: "system", Content: "KEEP ME"}}
	sess.mu.Unlock()

	for i := 0; i < 500; i++ {
		sess.mu.Lock()
		e.appendHistoryLocked(sess, llm.ChatMessage{Role: "user", Content: "turn"})
		sess.mu.Unlock()
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	// system + at most 100 body messages.
	if len(sess.History) > 101 {
		t.Errorf("history exceeded window: got %d, want <= 101", len(sess.History))
	}
	if len(sess.History) == 0 || sess.History[0].Role != "system" || sess.History[0].Content != "KEEP ME" {
		t.Errorf("system prompt must survive 500 turns of trimming; head = %+v", sess.History[0])
	}
}
