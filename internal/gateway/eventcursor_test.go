// eventcursor_test.go — MU-026 criterion 4: resume from a bounded cursor,
// without the buffer becoming a second delivery path that skips authorization.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/storage"
	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/message"
)

type fakeDurableReplay struct{ window storage.EventReplayWindow }

func (f fakeDurableReplay) ReplayEvents(_ context.Context, workspaceID string, after uint64, limit int) (storage.EventReplayWindow, error) {
	out := f.window
	out.Events = nil
	for _, event := range f.window.Events {
		if event.Event.WorkspaceID == workspaceID && event.ID > after {
			out.Events = append(out.Events, event)
		}
	}
	if len(out.Events) > limit {
		out.HasMore = true
		out.Events = out.Events[:limit]
	}
	return out, nil
}
func (f fakeDurableReplay) LatestEventCursor(context.Context, string) (uint64, error) {
	return f.window.Latest, nil
}

func hubWithReplay(t *testing.T, capacity int) *EventHub {
	t.Helper()
	h := NewEventHub(zap.NewNop(), nil)
	h.replay = newReplayBuffer(capacity)
	return h
}

func emit(t *testing.T, h *EventHub, workspaceID, agentID, sessionID string) string {
	t.Helper()
	event := message.Event{Type: "tool.result", WorkspaceID: workspaceID, AgentID: agentID, SessionID: sessionID}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return h.replay.Append(event, data)
}

// The ordinary case: a socket drops, the client comes back with its cursor and
// gets what it missed — and nothing it already had.
func TestAReconnectingClientResumesFromItsCursor(t *testing.T) {
	h := hubWithReplay(t, 16)
	h.SetEventAuthorizer(func(eventPrincipal, message.Event) bool { return true })

	emit(t, h, "ws_a", "bot", "s1")
	cursor := emit(t, h, "ws_a", "bot", "s2")
	emit(t, h, "ws_a", "bot", "s3")
	emit(t, h, "ws_a", "bot", "s4")

	replayed, latest, err := h.ResumeSince(subscriber("ws_a", "operator:alice", "operator", false), cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 2 {
		t.Fatalf("replayed %d events, want the 2 after the cursor", len(replayed))
	}
	if latest == cursor {
		t.Fatal("the returned cursor did not advance")
	}
	// Presenting the new cursor immediately yields nothing: the client is
	// caught up, not stuck replaying.
	again, _, err := h.ResumeSince(subscriber("ws_a", "operator:alice", "operator", false), latest)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("a caught-up client replayed %d events", len(again))
	}
}

func TestAClientCanResumeOnAnotherReplicaFromDurableCursor(t *testing.T) {
	h := hubWithReplay(t, 16)
	h.durableReplay = fakeDurableReplay{window: storage.EventReplayWindow{
		Oldest: 40, Latest: 42,
		Events: []storage.CursorEvent{
			{ID: 41, Event: message.Event{Type: "tool.result", WorkspaceID: "ws_a", SessionID: "s1"}},
			{ID: 42, Event: message.Event{Type: "message.out", WorkspaceID: "ws_a", SessionID: "s1"}},
		},
	}}
	h.SetEventAuthorizer(func(eventPrincipal, message.Event) bool { return true })
	replayed, latest, err := h.ResumeSince(subscriber("ws_a", "operator:alice", "operator", false), formatCursor("ws_a", 40))
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 2 || latest != formatCursor("ws_a", 42) {
		t.Fatalf("cross-replica replay=%d latest=%q", len(replayed), latest)
	}
	if got := h.currentCursor("ws_a"); got != formatCursor("ws_a", 42) {
		t.Fatalf("current cursor=%q", got)
	}
}

// THE property that makes replay safe. A reconnecting client is a new
// connection with a new principal: what it was allowed to see before is not the
// question. A buffer that replayed what it stored would be a delivery path with
// no permission check.
func TestReplayIsReauthorizedRatherThanReplayedAsStored(t *testing.T) {
	h := hubWithReplay(t, 16)
	// Everything is retained; only tool.result is authorized on the way out.
	h.SetEventAuthorizer(func(_ eventPrincipal, event message.Event) bool {
		return event.Type == "tool.result"
	})
	start := h.replay.Append(message.Event{Type: "tool.result", WorkspaceID: "ws_a"}, []byte(`{"seed":true}`))
	for _, secret := range []string{"secret.event", "tool.result", "secret.event"} {
		event := message.Event{Type: secret, WorkspaceID: "ws_a"}
		data, _ := json.Marshal(event)
		h.replay.Append(event, data)
	}

	replayed, _, err := h.ResumeSince(subscriber("ws_a", "operator:alice", "operator", false), start)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 1 {
		t.Fatalf("replayed %d events; the authorizer was bypassed on the resume path", len(replayed))
	}
}

// A cursor names a position in ONE tenant's stream. Presenting another
// workspace's cursor must be refused, not coerced — a guessed integer must not
// become a read into somebody else's history.
func TestAForeignCursorIsRefusedNotCoerced(t *testing.T) {
	h := hubWithReplay(t, 16)
	h.SetEventAuthorizer(func(eventPrincipal, message.Event) bool { return true })
	emit(t, h, "ws_victim", "bot", "s1")
	victimCursor := emit(t, h, "ws_victim", "bot", "s2")
	emit(t, h, "ws_victim", "bot", "s3")

	_, _, err := h.ResumeSince(subscriber("ws_attacker", "operator:mallory", "operator", false), victimCursor)
	if !errors.Is(err, ErrCursorForeign) {
		t.Fatalf("err = %v, want ErrCursorForeign", err)
	}
	// A hand-written cursor claiming the attacker's own workspace at a
	// position that workspace never reached is refused outright — and refused
	// as UNKNOWN rather than as a gap, because nothing was lost and telling a
	// client to refetch would send it after history it already has.
	_, _, err = h.ResumeSince(subscriber("ws_attacker", "operator:mallory", "operator", false), formatCursor("ws_attacker", 1))
	if !errors.Is(err, ErrCursorUnknown) {
		t.Fatalf("a forged cursor returned %v, want ErrCursorUnknown", err)
	}
}

// Sequences are per workspace, so one tenant's traffic cannot advance another's
// cursor and make its client think it missed events.
func TestSequencesAreIndependentPerWorkspace(t *testing.T) {
	h := hubWithReplay(t, 16)
	h.SetEventAuthorizer(func(eventPrincipal, message.Event) bool { return true })
	aStart := emit(t, h, "ws_a", "bot", "s1")
	for i := 0; i < 10; i++ {
		emit(t, h, "ws_b", "bot", "noise")
	}
	emit(t, h, "ws_a", "bot", "s2")

	replayed, _, err := h.ResumeSince(subscriber("ws_a", "operator:alice", "operator", false), aStart)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 1 {
		t.Fatalf("ws_a replayed %d events; another tenant's traffic moved its position", len(replayed))
	}
}

// A bounded buffer necessarily forgets. Silently starting from the oldest
// retained event looks exactly like a successful resume and loses everything in
// between — the worst outcome, because the client believes it is caught up.
func TestFallingOffTheWindowIsReportedAsAGap(t *testing.T) {
	h := hubWithReplay(t, 4)
	h.SetEventAuthorizer(func(eventPrincipal, message.Event) bool { return true })
	stale := emit(t, h, "ws_a", "bot", "s1")
	for i := 0; i < 10; i++ {
		emit(t, h, "ws_a", "bot", "later")
	}

	_, _, err := h.ResumeSince(subscriber("ws_a", "operator:alice", "operator", false), stale)
	var gap *ErrCursorGap
	if !errors.As(err, &gap) {
		t.Fatalf("err = %v, want ErrCursorGap", err)
	}
	if gap.Oldest == 0 {
		t.Fatal("the gap does not say what is still retained")
	}
}

// Bounded per WORKSPACE, not globally: one busy tenant must not evict every
// other tenant's replay window.
func TestOneBusyTenantDoesNotEvictAnothersWindow(t *testing.T) {
	h := hubWithReplay(t, 4)
	h.SetEventAuthorizer(func(eventPrincipal, message.Event) bool { return true })
	quietCursor := emit(t, h, "ws_quiet", "bot", "s1")
	for i := 0; i < 1000; i++ {
		emit(t, h, "ws_busy", "bot", "flood")
	}
	emit(t, h, "ws_quiet", "bot", "s2")

	replayed, _, err := h.ResumeSince(subscriber("ws_quiet", "operator:alice", "operator", false), quietCursor)
	if err != nil {
		t.Fatalf("the quiet tenant lost its window to a busy one: %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("replayed %d", len(replayed))
	}
	// And the busy tenant's retention is still capped.
	h.replay.mu.RLock()
	retained := len(h.replay.events["ws_busy"])
	h.replay.mu.RUnlock()
	if retained > 4 {
		t.Fatalf("the busy tenant retains %d events, cap is 4", retained)
	}
}

// An unstamped event belongs to the personal workspace, the same rule every
// store on this branch applies — so a single-tenant deployment resumes too.
func TestAnUnstampedEventResumesInThePersonalWorkspace(t *testing.T) {
	h := hubWithReplay(t, 16)
	h.SetEventAuthorizer(func(eventPrincipal, message.Event) bool { return true })
	cursor := h.replay.Append(message.Event{Type: "tool.result"}, []byte(`{}`))
	h.replay.Append(message.Event{Type: "tool.result"}, []byte(`{}`))

	replayed, _, err := h.ResumeSince(subscriber(wsroot.PersonalWorkspaceID, "operator:local", "operator", false), cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 1 {
		t.Fatalf("personal replayed %d", len(replayed))
	}
}

// A malformed cursor is an error, not a silent full replay: handing a client
// the entire retained window because it sent nonsense is a leak in the shape of
// a convenience.
func TestAMalformedCursorIsRefused(t *testing.T) {
	h := hubWithReplay(t, 16)
	h.SetEventAuthorizer(func(eventPrincipal, message.Event) bool { return true })
	emit(t, h, "ws_a", "bot", "s1")
	for _, cursor := range []string{"", "garbage", "ws_a:", ":12", "ws_a:notanumber", "12"} {
		if _, _, err := h.ResumeSince(subscriber("ws_a", "operator:alice", "operator", false), cursor); err == nil {
			t.Errorf("cursor %q was accepted", cursor)
		}
	}
}

// Replay still respects the real authorizer, including its workspace rules.
func TestReplayHonoursTheRealEventAuthorizer(t *testing.T) {
	server := withCfg(&Server{}, &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeTeam}})
	h := hubWithReplay(t, 16)
	h.SetEventAuthorizer(server.authorizeEvent)

	start := h.replay.Append(message.Event{Type: "tool.call", WorkspaceID: "ws_a", AgentID: "bot"}, []byte(`{"seed":true}`))
	event := message.Event{Type: "tool.call", WorkspaceID: "ws_victim", AgentID: "bot"}
	data, _ := json.Marshal(event)
	h.replay.Append(event, data)

	// The attacker's own cursor cannot even name the victim's stream, so the
	// interesting case is a subscriber in ws_a resuming and finding a
	// foreign-workspace event in the buffer.
	replayed, _, err := h.ResumeSince(subscriber("ws_a", "operator:alice", "operator", false), start)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range replayed {
		var decoded message.Event
		if err := json.Unmarshal(raw, &decoded); err != nil {
			continue
		}
		if decoded.WorkspaceID == "ws_victim" {
			t.Fatal("replay delivered another workspace's event")
		}
	}
}
