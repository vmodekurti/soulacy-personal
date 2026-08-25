// session_activity.go — per-session heartbeat tracking for the "session hung"
// surface (E4c — Cohort E Activity failure handling).
//
// Historically the gateway had no concept of "this session started at 09:00,
// last emitted anything at 09:02, and it's now 09:20 with the engine still
// holding the run" — the runtime's `Session.lastAccess` (used only for
// eviction) was never exposed, `Scheduler.RunningSnapshot` only knows about
// scheduled runs, and Activity.svelte had no filter for stalled runs. That
// meant an operator whose ReAct loop got wedged in a tool retry / dead MCP
// call / provider deadline had no way to notice short of scrolling the raw
// event log.
//
// This file adds a tiny in-memory tracker attached to the EventHub. Every
// event that flows through Emit() bumps that session's last-event timestamp;
// terminal events (`message.out` and `error`) evict the session; the exposed
// snapshot is what /activity/running returns and is what the GUI polls to
// render a "Running now" strip with a warning callout when a session has been
// silent for long enough to count as hung.
//
// The tracker is deliberately lightweight — a single mutex, a plain map, and
// a lazy sweep. It does NOT persist anything; a gateway restart forgets
// in-flight sessions (which is fine, because a restart also cancels them).

package gateway

import (
	"sort"
	"sync"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

// defaultHungSilentThreshold is how long a session may go without emitting
// ANY event before Snapshot marks it Hung. Chosen to be well above a
// reasonable slow-LLM turn (Anthropic can take 30-40s on long prompts) but
// below what any healthy chain reaches in normal operation. Overridable via
// (*sessionActivityTracker).SetHungThreshold for tests.
const defaultHungSilentThreshold = 5 * time.Minute

// defaultSessionEvictAfter is the safety cap on how long a session can sit in
// the map after its last event before Snapshot silently drops it. If a run
// dies without emitting a terminal `message.out` / `error` event the entry
// would otherwise leak. 1 hour is longer than the scheduler's `maxRunDuration`
// and well past when a hung session becomes uninteresting to the operator.
const defaultSessionEvictAfter = 1 * time.Hour

// sessionRecord is the private per-session bookkeeping row. It's converted to
// a public shape by Snapshot() before leaving the tracker.
type sessionRecord struct {
	AgentID       string
	SessionID     string
	StartedAt     time.Time
	LastEventAt   time.Time
	LastEventType string
}

// RunningSession is the JSON shape returned by /activity/running for each
// in-flight session. Field names mirror the message.Event convention (snake
// case) so the GUI can render them without a translation layer.
type RunningSession struct {
	AgentID        string    `json:"agent_id"`
	SessionID      string    `json:"session_id"`
	StartedAt      time.Time `json:"started_at"`
	LastEventAt    time.Time `json:"last_event_at"`
	LastEventType  string    `json:"last_event_type,omitempty"`
	ElapsedSeconds int64     `json:"elapsed_seconds"`
	SilentSeconds  int64     `json:"silent_seconds"`
	Hung           bool      `json:"hung"`
	HungReason     string    `json:"hung_reason,omitempty"`
	HungFix        string    `json:"hung_fix,omitempty"`
}

// sessionActivityTracker is the in-memory heartbeat table. Attached to a
// running EventHub via newSessionActivityTracker + hub wiring.
type sessionActivityTracker struct {
	mu            sync.RWMutex
	sessions      map[string]*sessionRecord // key: session_id
	hungThreshold time.Duration
	evictAfter    time.Duration
	nowFn         func() time.Time
}

func newSessionActivityTracker() *sessionActivityTracker {
	return &sessionActivityTracker{
		sessions:      make(map[string]*sessionRecord),
		hungThreshold: defaultHungSilentThreshold,
		evictAfter:    defaultSessionEvictAfter,
		nowFn:         time.Now,
	}
}

// SetHungThreshold overrides the default silent-window for tests. Must be
// >= 1s so we never flag a session hung during its own turn.
func (t *sessionActivityTracker) SetHungThreshold(d time.Duration) {
	if d < time.Second {
		d = time.Second
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.hungThreshold = d
}

// Note records that `ev` was observed. Called from EventHub.Emit BEFORE the
// event is broadcast so the tracker is up to date by the time any GUI polls
// /activity/running. Events without a session_id are ignored — they are
// system-level (connected, scheduler heartbeats) rather than per-run.
//
// message.in is treated as a session start (sets StartedAt); message.out and
// error mark the session as finished and evict the entry. Everything else
// (llm.*, tool.*, reasoning.*) just bumps LastEventAt.
func (t *sessionActivityTracker) Note(ev message.Event) {
	if ev.SessionID == "" {
		return
	}
	now := t.nowFn().UTC()
	if !ev.Timestamp.IsZero() {
		now = ev.Timestamp.UTC()
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	switch ev.Type {
	case "message.out", "error", "run.completed", "run.failed", "run.cancelled", "run.canceled", "run.finished":
		// Terminal for the run — drop it from the map. `error` is not always
		// terminal in the runtime (a ReAct loop can produce an intermediate
		// error and continue) but for the "hung session" surface this is the
		// right conservative choice: once we've seen an error we don't want to
		// keep highlighting it in the "still running" list.
		delete(t.sessions, ev.SessionID)
		return
	case "message.in":
		// Fresh run: always overwrite so a session-id reuse (uncommon but
		// possible for scheduled agents) resets the elapsed timer.
		t.sessions[ev.SessionID] = &sessionRecord{
			AgentID:       ev.AgentID,
			SessionID:     ev.SessionID,
			StartedAt:     now,
			LastEventAt:   now,
			LastEventType: ev.Type,
		}
		return
	case "connected":
		// System event, not per-run.
		return
	}

	r, ok := t.sessions[ev.SessionID]
	if !ok {
		// A non-message.in event landed for a session we've never seen — this
		// happens when the gateway restarted mid-run and the ledger flushed a
		// tail. Bootstrap a record from the event so /activity/running still
		// reports the run, but treat StartedAt as "unknown, use LastEventAt".
		r = &sessionRecord{
			AgentID:   ev.AgentID,
			SessionID: ev.SessionID,
			StartedAt: now,
		}
		t.sessions[ev.SessionID] = r
	}
	r.LastEventAt = now
	if ev.Type != "" {
		r.LastEventType = ev.Type
	}
	if r.AgentID == "" && ev.AgentID != "" {
		r.AgentID = ev.AgentID
	}
}

// Snapshot returns the currently-tracked sessions with derived elapsed/silent
// fields and the Hung flag applied. Sessions older than evictAfter (last
// event > 1h ago) are dropped in-place so the map never grows unbounded when
// a run dies without emitting a terminal event.
//
// Results are sorted by started_at descending so the newest in-flight session
// is first, which is what the operator usually wants to look at.
func (t *sessionActivityTracker) Snapshot() []RunningSession {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.nowFn().UTC()
	hungAfter := t.hungThreshold
	evictAfter := t.evictAfter

	out := make([]RunningSession, 0, len(t.sessions))
	for id, r := range t.sessions {
		if now.Sub(r.LastEventAt) > evictAfter {
			delete(t.sessions, id)
			continue
		}
		silent := now.Sub(r.LastEventAt)
		if silent < 0 {
			silent = 0
		}
		elapsed := now.Sub(r.StartedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		rs := RunningSession{
			AgentID:        r.AgentID,
			SessionID:      r.SessionID,
			StartedAt:      r.StartedAt,
			LastEventAt:    r.LastEventAt,
			LastEventType:  r.LastEventType,
			ElapsedSeconds: int64(elapsed / time.Second),
			SilentSeconds:  int64(silent / time.Second),
		}
		if silent >= hungAfter {
			rs.Hung = true
			rs.HungReason, rs.HungFix = hungExplain(r.LastEventType, silent)
		}
		out = append(out, rs)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out
}

// hungExplain returns per-last-event-type reason+fix strings for the hung
// surface. The idea is to hint at where in the runtime the wedge probably is,
// so the operator has a starting point rather than "session hung" with no
// context.
func hungExplain(lastEventType string, silent time.Duration) (string, string) {
	silentStr := silent.Round(time.Second).String()
	switch lastEventType {
	case "llm.call":
		return "The agent has been waiting on the LLM provider for " + silentStr + " with no reply.",
			"Check the Providers page for a rate-limit or overload against the run's model. If the provider is healthy, cancel this run and inspect the last prompt for size (very large contexts can time out silently)."
	case "tool.call":
		return "A tool call has been in flight for " + silentStr + " with no result.",
			"Tools that shell out to MCP / Python subprocesses can hang if the child process wedges. Check the run's last tool.call event for the tool name, then run `sy doctor` and inspect the tool's process."
	case "reasoning.step", "reasoning.start":
		return "The ReAct loop paused for " + silentStr + " between steps.",
			"Usually caused by a slow LLM turn or a large tool result being formatted. If the pause exceeds the agent's step_timeout, the loop will surface a timeout error; if it doesn't, consider tightening step_timeout via the Studio → Runtime intent preset."
	case "message.in", "":
		return "The run has been in setup for " + silentStr + " with no LLM / tool / reasoning activity yet.",
			"This is unusual — the runtime should reach an LLM call within seconds of message.in. Check the gateway logs for an early error, and confirm the agent's LLM provider is registered."
	default:
		return "No activity from this session for " + silentStr + ".",
			"Open Activity for this session to see the last event, or restart the gateway if it keeps happening for multiple agents."
	}
}
