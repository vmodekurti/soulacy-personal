// events.go — real-time event streaming hub for the GUI.
// The EventHub maintains a set of connected WebSocket clients and broadcasts
// structured JSON events to all of them. The agent engine emits events via
// the EventSink interface, which the hub implements.
//
// Events are used by the GUI to:
//   - Stream live log entries (message.in, message.out)
//   - Show tool call/result traces
//   - Display scheduler firing events
//   - Update the session activity timeline
package gateway

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	fws "github.com/gofiber/websocket/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/storage"
	"github.com/soulacy/soulacy/pkg/message"
)

const wsPrincipalKey = "ws_event_principal"

// eventPrincipal is copied into the upgraded connection's locals. It contains
// no credential material and is immutable for the connection lifetime.
type eventPrincipal struct {
	Principal     string
	WorkspaceID   string
	Role          string
	Scopes        []string
	Admin         bool
	Authenticated bool
}

type eventAuthorizer func(eventPrincipal, message.Event) bool
type eventObserver func(message.Event)

// wsClient wraps a WebSocket connection with a buffered send queue so a slow or
// stale client can never block the broadcaster (and thus the agent engine).
type wsClient struct {
	conn      *fws.Conn
	send      chan []byte
	principal eventPrincipal
}

// EventHub broadcasts events to all connected WebSocket clients and persists
// them to the per-agent action log.
//
// CRITICAL: broadcasting is non-blocking. Each client has a buffered send queue
// drained by its own writer goroutine. If a client falls behind (e.g. a
// backgrounded browser tab that stops reading), its queue fills and we DROP
// events for that client rather than blocking — because Emit runs on the agent
// execution path, and a blocked WriteMessage would otherwise freeze the agent
// mid-run.
// eventPublisher forwards events to an external sink (the queue backend,
// story E1). Implemented by events.Publisher; must never block.
type eventPublisher interface {
	PublishEvent(message.Event)
}

type EventHub struct {
	mu          sync.RWMutex
	clients     map[*wsClient]struct{}
	log         *zap.Logger
	actions     storage.ActionLogBackend       // nil = persistence disabled
	publisher   atomic.Pointer[eventPublisher] // nil = queue publishing disabled
	authorize   eventAuthorizer                // startup-only; nil preserves broadcast compatibility
	observersMu sync.RWMutex
	observers   []eventObserver

	// replay retains recent events per workspace so a reconnecting client can
	// resume from a bounded cursor (MU-026 criterion 4). See eventcursor.go.
	replay *replayBuffer

	// activity is the E4c hung-session tracker. Every event that passes through
	// Emit() is noted so /activity/running can render "session hung" callouts
	// for runs that stopped emitting for longer than the tracker's threshold.
	activity *sessionActivityTracker
}

// SetEventAuthorizer installs per-connection event filtering. It must be wired
// before clients connect.
func (h *EventHub) SetEventAuthorizer(fn eventAuthorizer) { h.authorize = fn }

func (h *EventHub) AddObserver(fn eventObserver) {
	if fn == nil {
		return
	}
	h.observersMu.Lock()
	h.observers = append(h.observers, fn)
	h.observersMu.Unlock()
}

// maxWSFrameBytes caps an inbound WebSocket frame. The event stream is
// server→client only; a client frame is never parsed, so this only needs to be
// large enough for protocol control frames.
const maxWSFrameBytes = 64 << 10

// SetEventPublisher wires an external event publisher (story E1). Documented as
// startup-only, and that is how it is called — but Emit reads this field from
// arbitrary request goroutines, so "documented" and "safe" are not the same
// thing. An atomic pointer costs nothing and removes the footgun for whoever
// next decides to call this at runtime. When nil, events are only persisted +
// broadcast to WebSocket clients, exactly as before.
func (h *EventHub) SetEventPublisher(p eventPublisher) {
	h.publisher.Store(&p)
}

// eventPublisherOrNil returns the wired publisher, or nil.
func (h *EventHub) eventPublisherOrNil() eventPublisher {
	if p := h.publisher.Load(); p != nil {
		return *p
	}
	return nil
}

// NewEventHub creates an EventHub. actions may be nil to disable persistence.
func NewEventHub(log *zap.Logger, actions storage.ActionLogBackend) *EventHub {
	return &EventHub{
		clients:  make(map[*wsClient]struct{}),
		log:      log,
		actions:  actions,
		replay:   newReplayBuffer(defaultReplayPerWorkspace),
		activity: newSessionActivityTracker(),
	}
}

// RunningSessions returns the current hung-session-aware snapshot. Handlers
// call this to serve /activity/running; tests use it to observe the tracker.
func (h *EventHub) RunningSessions() []RunningSession {
	if h.activity == nil {
		return nil
	}
	return h.activity.Snapshot()
}

// Emit implements the runtime.EventSink interface. It persists the event to the
// per-agent action log, then broadcasts it to all connected WebSocket clients.
// It must never block — agent execution depends on it returning promptly.
func (h *EventHub) Emit(event message.Event) {
	if h.actions != nil {
		h.actions.Append(event)
	}
	if p := h.eventPublisherOrNil(); p != nil {
		p.PublishEvent(event) // non-blocking by contract
	}
	// E4c — session heartbeat: note before broadcast so /activity/running sees
	// the update at the same instant WebSocket clients do. Cheap map bump under
	// a per-tracker mutex; can't block Emit.
	if h.activity != nil {
		h.activity.Note(event)
	}
	h.observersMu.RLock()
	observers := append([]eventObserver(nil), h.observers...)
	h.observersMu.RUnlock()
	for _, observe := range observers {
		observe(event)
	}
	// Serialized through the public projection, so a field added to
	// message.Event for the authorizer's benefit cannot reach the wire by
	// default (MU-026 criterion 2). One serialization is still correct:
	// authorization is a boolean gate, so nothing about the payload varies by
	// WHO receives it — only whether they do.
	data, err := json.Marshal(project(event))
	if err != nil {
		h.log.Error("event marshal failed", zap.Error(err))
		return
	}
	// Retained BEFORE broadcast so a client that reconnects between the two
	// can still resume across the event: buffering afterwards leaves a window
	// where an event was delivered live and is not yet resumable.
	if h.replay != nil {
		h.replay.Append(event, data)
	}
	h.broadcastEvent(data, event)
}

// ResumeSince replays the events a subscriber missed, re-authorizing each one.
//
// Re-authorized rather than replayed as stored: a reconnecting client is a NEW
// connection with a new principal, and what it was allowed to see before is not
// the question. A buffer that replayed its contents would be a second delivery
// path with no permission check — which is how a replay feature becomes the
// leak the live path was careful to prevent.
func (h *EventHub) ResumeSince(principal eventPrincipal, cursor string) ([][]byte, string, error) {
	if h == nil || h.replay == nil {
		return nil, "", nil
	}
	buffered, err := h.replay.Since(principal.WorkspaceID, cursor)
	if err != nil {
		return nil, "", err
	}
	out := make([][]byte, 0, len(buffered))
	latest := cursor
	for _, entry := range buffered {
		latest = formatCursor(principal.WorkspaceID, entry.seq)
		if h.authorize != nil && !h.authorize(principal, entry.event) {
			continue
		}
		out = append(out, entry.data)
	}
	return out, latest, nil
}

func (h *EventHub) broadcastEvent(data []byte, event message.Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if h.authorize != nil && !h.authorize(c.principal, event) {
			continue
		}
		select {
		case c.send <- data:
		default:
		}
	}
}

// Handler is the Fiber WebSocket handler. Each connecting client gets a buffered
// send queue and a dedicated writer goroutine; the read loop detects disconnect.
func (h *EventHub) Handler(conn *fws.Conn) {
	principal, _ := conn.Locals(wsPrincipalKey).(eventPrincipal)
	c := &wsClient{conn: conn, send: make(chan []byte, 256), principal: principal}

	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	h.log.Debug("ws client connected", zap.String("remote", conn.RemoteAddr().String()))

	// Writer goroutine — the ONLY place we call WriteMessage. A write deadline
	// ensures a wedged connection errors out instead of hanging forever.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for data := range c.send {
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(1, data); err != nil { // 1 = TextMessage
				_ = conn.Close() // unblock the read loop so cleanup runs
				return
			}
		}
	}()

	// MU-026 criterion 4: replay what this subscriber missed, if it presented a
	// cursor. Before the welcome frame, so a client processing frames in order
	// sees its gap filled and then "you are live" — the other order would have
	// it treat replayed events as new ones arriving after it caught up.
	resumeCursor := strings.TrimSpace(conn.Query("cursor"))
	latestCursor := resumeCursor
	var resumeGap error
	if resumeCursor != "" {
		replayed, latest, err := h.ResumeSince(principal, resumeCursor)
		if err != nil {
			resumeGap = err
		} else {
			latestCursor = latest
			for _, data := range replayed {
				select {
				case c.send <- data:
				default:
				}
			}
		}
	}

	// Welcome event (non-blocking).
	//
	// Carries the resume state: the cursor the client should present next, and
	// whether its previous cursor fell outside the retained window. A gap is
	// reported rather than silently partially replayed — "here are some events"
	// is indistinguishable from "here are all of them" once delivered, and a
	// client that believes it is caught up when it is not is the worst outcome
	// available.
	welcomePayload := map[string]any{
		"message": "Soulacy event stream active",
		"cursor":  latestCursor,
	}
	if resumeGap != nil {
		welcomePayload["resume_gap"] = resumeGap.Error()
	}
	if welcome, err := json.Marshal(message.Event{
		Type: "connected", Payload: welcomePayload, Timestamp: time.Now().UTC(),
	}); err == nil {
		select {
		case c.send <- welcome:
		default:
		}
	}

	// Read loop — discard client frames; returns on disconnect.
	//
	// The read limit is not optional. The websocket library defaults to NO limit,
	// ReadMessage is an io.ReadAll under the hood, and fiber's BodyLimit does not
	// apply to a hijacked connection — nor does the IP rate limiter, which skips
	// /ws by design. One authenticated client sending a single huge text frame
	// therefore buffered the whole thing in RAM. Nothing here reads client frames
	// at all, so a small limit costs nothing.
	conn.SetReadLimit(maxWSFrameBytes)
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}

	// Remove from the broadcast set BEFORE closing send so broadcast never
	// sends on a closed channel.
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	close(c.send)
	<-writerDone

	h.log.Debug("ws client disconnected", zap.String("remote", conn.RemoteAddr().String()))
}

// PublishProgress forwards a ProgressEvent as an SSE frame to all connected
// GUI clients. The SSE event name is "progress".
func (h *EventHub) PublishProgress(ev message.ProgressEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		h.log.Error("progress event marshal failed", zap.Error(err))
		return
	}
	// Format as a proper SSE frame: "event: progress\ndata: <json>\n\n"
	frame := "event: progress\ndata: " + string(data) + "\n\n"
	// A progress event is scoped by run ID. Streaming chat binds the run ID to
	// the same principal as its session before registering the run.
	h.broadcastEvent([]byte(frame), message.Event{Type: "progress", SessionID: ev.RunID, Timestamp: ev.Timestamp})
}

// ClientCount returns the number of connected WebSocket clients.
func (h *EventHub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
