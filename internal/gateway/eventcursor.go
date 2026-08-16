package gateway

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/message"
)

// eventcursor.go — MU-026 criterion 4: "clients can resume from a bounded
// cursor after reconnect."
//
// A dropped WebSocket is the ordinary case, not the exceptional one: a laptop
// sleeps, a phone changes network, a proxy times out an idle connection. On
// reconnect the client has a gap it cannot see, and the events it missed are
// exactly the ones it was watching for — a tool result, a run finishing.
//
// THREE THINGS THIS HAS TO GET RIGHT, and two of them are easy to get wrong in
// a way that looks like success.
//
//  1. REPLAY IS NOT A BYPASS. Buffered events are re-authorized on the way out,
//     through the same authorizer the live broadcast uses. A buffer that
//     replayed what it stored would be a second delivery path with no
//     permission check — and the natural implementation, "remember what we
//     sent this client", does not exist here because a reconnecting client is
//     a NEW connection with a new principal. Whatever it was allowed to see
//     before is not the question; what it may see now is.
//
//  2. A CURSOR IS WORKSPACE-BOUND. It names a position in one tenant's stream.
//     Presenting workspace A's cursor on a workspace B subscription must not
//     resume B's stream at that offset, or a guessed integer becomes a read
//     into somebody else's history. The workspace is encoded in the cursor and
//     compared to the subscription's — a mismatch is refused, not coerced.
//
//  3. FALLING OFF THE BUFFER IS AN ANSWER, NOT A DETAIL. A bounded buffer
//     necessarily forgets. Resuming a cursor older than what is retained by
//     silently starting from the oldest available event looks exactly like a
//     successful resume and quietly loses everything in between — the worst
//     failure available, because the client believes it is caught up. A gap is
//     reported explicitly so the client can refetch from the durable action
//     log instead.
//
// BOUNDED PER WORKSPACE, not globally. A global ring would let one busy tenant
// evict every other tenant's replay window, turning "resume works" into "resume
// works unless somebody else is busy" — availability coupled across tenants,
// which is the thing this milestone keeps taking apart.

// defaultReplayPerWorkspace is how many recent events each workspace retains.
//
// A window, not a log: the durable action log is where history lives, and this
// only has to cover a reconnect. 256 events is a few seconds of a busy run and
// a long time on an idle one, which is the shape of the gap a dropped socket
// actually produces.
const defaultReplayPerWorkspace = 256

// bufferedEvent is one retained event and its position.
type bufferedEvent struct {
	seq   uint64
	event message.Event
	data  []byte
}

// replayBuffer retains recent events per workspace so a reconnecting client can
// resume.
type replayBuffer struct {
	mu       sync.RWMutex
	capacity int
	// nextSeq is per workspace: two tenants' sequences are independent, so one
	// tenant's traffic cannot advance another's cursor and make its client
	// think it missed events.
	nextSeq map[string]uint64
	events  map[string][]bufferedEvent
}

func newReplayBuffer(capacity int) *replayBuffer {
	if capacity <= 0 {
		capacity = defaultReplayPerWorkspace
	}
	return &replayBuffer{
		capacity: capacity,
		nextSeq:  make(map[string]uint64),
		events:   make(map[string][]bufferedEvent),
	}
}

// Append retains an event and returns its cursor.
func (b *replayBuffer) Append(event message.Event, data []byte) string {
	workspaceID := wsroot.Normalize(event.WorkspaceID)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextSeq[workspaceID]++
	seq := b.nextSeq[workspaceID]
	retained := append(b.events[workspaceID], bufferedEvent{seq: seq, event: event, data: data})
	if len(retained) > b.capacity {
		// Drop from the front. Copying into a fresh slice rather than
		// re-slicing keeps the backing array from growing without bound — a
		// re-slice retains every evicted element alive behind the window.
		trimmed := make([]bufferedEvent, b.capacity)
		copy(trimmed, retained[len(retained)-b.capacity:])
		retained = trimmed
	}
	b.events[workspaceID] = retained
	return formatCursor(workspaceID, seq)
}

// ErrCursorGap reports that a cursor is older than what the buffer retains, so
// events between it and the oldest retained one are gone.
//
// A distinct error rather than a silent partial replay: the client needs to
// know it must refetch from the durable action log, and "here are some events"
// is indistinguishable from "here are all of them" once delivered.
type ErrCursorGap struct {
	WorkspaceID string
	Requested   uint64
	Oldest      uint64
}

func (e *ErrCursorGap) Error() string {
	return fmt.Sprintf("event cursor %d is older than the retained window (oldest %d); refetch from the action log",
		e.Requested, e.Oldest)
}

// ErrCursorForeign reports a cursor issued for a different workspace.
var ErrCursorForeign = fmt.Errorf("event cursor belongs to a different workspace")

// ErrCursorUnknown reports a cursor ahead of anything this workspace ever
// issued — a forged or stale-from-another-process value.
//
// Distinct from a gap on purpose. A gap means "you missed events, refetch"; a
// client acting on that when nothing was lost would refetch history it already
// has. And the two must not collapse into success: silently returning nothing
// for a cursor from the future is the same shape as a successful catch-up.
var ErrCursorUnknown = fmt.Errorf("event cursor was never issued for this workspace")

// Since returns the events after a cursor, for one workspace.
//
// The workspace is the SUBSCRIPTION's, never the cursor's: a cursor naming
// another tenant is refused rather than honoured. Reading the workspace out of
// the cursor and trusting it would make a client-supplied string the authority
// on which tenant's history it gets.
func (b *replayBuffer) Since(workspaceID, cursor string) ([]bufferedEvent, error) {
	workspaceID = wsroot.Normalize(workspaceID)
	cursorWorkspace, seq, err := parseCursor(cursor)
	if err != nil {
		return nil, err
	}
	if cursorWorkspace != workspaceID {
		return nil, ErrCursorForeign
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	if seq > b.nextSeq[workspaceID] {
		// Ahead of anything this workspace ever issued. Checked before the
		// empty-buffer case so a forged cursor cannot read as "you are caught
		// up" in a workspace that simply has no traffic.
		return nil, ErrCursorUnknown
	}
	retained := b.events[workspaceID]
	if len(retained) == 0 {
		// Nothing retained and the cursor is not from the future: a workspace
		// whose traffic predates this process has nothing to replay, and
		// nothing was lost that the buffer could have held.
		return nil, nil
	}
	oldest := retained[0].seq
	if seq+1 < oldest {
		return nil, &ErrCursorGap{WorkspaceID: workspaceID, Requested: seq, Oldest: oldest}
	}
	out := make([]bufferedEvent, 0, len(retained))
	for _, buffered := range retained {
		if buffered.seq > seq {
			out = append(out, buffered)
		}
	}
	return out, nil
}

// formatCursor encodes a workspace and a position.
//
// The workspace is IN the cursor so a foreign one can be detected rather than
// silently applied to whatever stream the client happens to be subscribed to.
// It is not a secret and not authority — Since compares it to the
// subscription's workspace and refuses a mismatch.
func formatCursor(workspaceID string, seq uint64) string {
	return wsroot.Normalize(workspaceID) + ":" + strconv.FormatUint(seq, 10)
}

func parseCursor(cursor string) (string, uint64, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return "", 0, fmt.Errorf("event cursor is empty")
	}
	at := strings.LastIndex(cursor, ":")
	if at <= 0 || at == len(cursor)-1 {
		return "", 0, fmt.Errorf("event cursor %q is malformed", cursor)
	}
	seq, err := strconv.ParseUint(cursor[at+1:], 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("event cursor %q is malformed", cursor)
	}
	return wsroot.Normalize(cursor[:at]), seq, nil
}
