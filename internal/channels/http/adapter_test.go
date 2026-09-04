package http

import (
	"context"
	"sync"
	"testing"

	"github.com/soulacy/soulacy/pkg/message"
)

// ---- helpers ----------------------------------------------------------------

// makeInbox returns a buffered channel large enough to avoid blocking in tests.
func makeInbox(n int) chan message.Message {
	return make(chan message.Message, n)
}

func firstText(msg message.Message) string {
	for _, p := range msg.Parts {
		if p.Type == message.ContentText {
			return p.Text
		}
	}
	return ""
}

// ---- New --------------------------------------------------------------------

func TestNew_NonNil(t *testing.T) {
	a := New()
	if a == nil {
		t.Fatal("New() returned nil")
	}
}

func TestNew_ResponsesMapInitialised(t *testing.T) {
	a := New()
	// responses must be non-nil so that Receive can store entries without panic.
	if a.responses == nil {
		t.Fatal("New(): responses map is nil")
	}
	if len(a.responses) != 0 {
		t.Fatalf("New(): responses map len = %d, want 0", len(a.responses))
	}
}

// ---- ID / Name --------------------------------------------------------------

func TestID(t *testing.T) {
	a := New()
	if got := a.ID(); got != "http" {
		t.Fatalf("ID() = %q, want %q", got, "http")
	}
}

func TestName(t *testing.T) {
	a := New()
	if got := a.Name(); got != "HTTP Webhook" {
		t.Fatalf("Name() = %q, want %q", got, "HTTP Webhook")
	}
}

// ---- Start ------------------------------------------------------------------

func TestStart_SetsInboxAndConnected(t *testing.T) {
	a := New()
	inbox := makeInbox(1)
	if err := a.Start(context.Background(), inbox); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if a.inbox == nil {
		t.Fatal("Start(): inbox not set")
	}
	if !a.connected {
		t.Fatal("Start(): connected not set to true")
	}
}

func TestStart_ReturnsNilError(t *testing.T) {
	a := New()
	if err := a.Start(context.Background(), makeInbox(1)); err != nil {
		t.Fatalf("Start() returned unexpected error: %v", err)
	}
}

// ---- Status -----------------------------------------------------------------

func TestStatus_BeforeStart_NotConnected(t *testing.T) {
	a := New()
	s := a.Status()
	if s.Connected {
		t.Fatal("Status() before Start: Connected should be false")
	}
}

func TestStatus_AfterStart_Connected(t *testing.T) {
	a := New()
	_ = a.Start(context.Background(), makeInbox(1))
	s := a.Status()
	if !s.Connected {
		t.Fatal("Status() after Start: Connected should be true")
	}
}

func TestStatus_Detail(t *testing.T) {
	a := New()
	s := a.Status()
	if s.Detail == "" {
		t.Fatal("Status().Detail should be non-empty")
	}
}

// ---- Stop -------------------------------------------------------------------

func TestStop_ReturnsNilError(t *testing.T) {
	a := New()
	_ = a.Start(context.Background(), makeInbox(1))
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestStop_MarksDisconnected(t *testing.T) {
	a := New()
	_ = a.Start(context.Background(), makeInbox(1))
	_ = a.Stop()
	if a.connected {
		t.Fatal("Stop(): connected should be false")
	}
}

func TestStatus_AfterStop_NotConnected(t *testing.T) {
	a := New()
	_ = a.Start(context.Background(), makeInbox(1))
	_ = a.Stop()
	if a.Status().Connected {
		t.Fatal("Status() after Stop: Connected should be false")
	}
}

// ---- Receive ----------------------------------------------------------------

func TestReceive_ReturnsNonNilChannelAndNonEmptyMsgID(t *testing.T) {
	a := New()
	inbox := makeInbox(1)
	_ = a.Start(context.Background(), inbox)

	replyCh, msgID := a.Receive("agent1", "user1", "alice", "hello")
	if replyCh == nil {
		t.Fatal("Receive(): reply channel is nil")
	}
	if msgID == "" {
		t.Fatal("Receive(): msgID is empty")
	}
}

func TestReceive_CreatesEntryInResponsesMap(t *testing.T) {
	a := New()
	inbox := makeInbox(1)
	_ = a.Start(context.Background(), inbox)

	_, msgID := a.Receive("agent1", "user1", "alice", "hello")

	a.mu.Lock()
	_, exists := a.responses[msgID]
	a.mu.Unlock()

	if !exists {
		t.Fatalf("Receive(): no entry for msgID %q in responses map", msgID)
	}
}

func TestReceive_SendsMessageToInbox(t *testing.T) {
	a := New()
	inbox := makeInbox(1)
	_ = a.Start(context.Background(), inbox)

	_, _ = a.Receive("agent1", "user42", "bob", "world")

	select {
	case msg := <-inbox:
		if msg.AgentID != "agent1" {
			t.Fatalf("inbox msg AgentID = %q, want %q", msg.AgentID, "agent1")
		}
		if msg.UserID != "user42" {
			t.Fatalf("inbox msg UserID = %q, want %q", msg.UserID, "user42")
		}
		if msg.Username != "bob" {
			t.Fatalf("inbox msg Username = %q, want %q", msg.Username, "bob")
		}
		if got := firstText(msg); got != "world" {
			t.Fatalf("inbox msg text = %q, want %q", got, "world")
		}
		if msg.Channel != "http" {
			t.Fatalf("inbox msg Channel = %q, want %q", msg.Channel, "http")
		}
		if msg.Role != message.RoleUser {
			t.Fatalf("inbox msg Role = %q, want %q", msg.Role, message.RoleUser)
		}
	default:
		t.Fatal("Receive(): nothing delivered to inbox channel")
	}
}

func TestReceive_InboxMessage_IDMatchesMsgID(t *testing.T) {
	a := New()
	inbox := makeInbox(1)
	_ = a.Start(context.Background(), inbox)

	_, msgID := a.Receive("agent1", "user1", "alice", "hi")

	msg := <-inbox
	if msg.ID != msgID {
		t.Fatalf("inbox msg ID = %q, want %q", msg.ID, msgID)
	}
}

func TestReceive_InboxMessage_SessionIDContainsUserID(t *testing.T) {
	a := New()
	inbox := makeInbox(1)
	_ = a.Start(context.Background(), inbox)

	_, _ = a.Receive("agent1", "user99", "alice", "hi")
	msg := <-inbox
	if msg.SessionID != "http-user99" {
		t.Fatalf("SessionID = %q, want %q", msg.SessionID, "http-user99")
	}
}

func TestReceive_InboxMessage_ThreadIDEqualsUserID(t *testing.T) {
	a := New()
	inbox := makeInbox(1)
	_ = a.Start(context.Background(), inbox)

	_, _ = a.Receive("agent1", "user99", "alice", "hi")
	msg := <-inbox
	if msg.ThreadID != "user99" {
		t.Fatalf("ThreadID = %q, want %q", msg.ThreadID, "user99")
	}
}

func TestReceive_UniqueIDs(t *testing.T) {
	a := New()
	inbox := makeInbox(2)
	_ = a.Start(context.Background(), inbox)

	_, id1 := a.Receive("agent1", "user1", "alice", "first")
	_, id2 := a.Receive("agent1", "user1", "alice", "second")

	if id1 == id2 {
		t.Fatal("Receive() returned the same msgID for two calls")
	}
}

// ---- Release ----------------------------------------------------------------

func TestRelease_RemovesEntryFromMap(t *testing.T) {
	a := New()
	inbox := makeInbox(1)
	_ = a.Start(context.Background(), inbox)

	_, msgID := a.Receive("agent1", "user1", "alice", "hello")
	// Drain inbox so the goroutine doesn't block.
	<-inbox

	a.Release(msgID)

	a.mu.Lock()
	_, exists := a.responses[msgID]
	a.mu.Unlock()

	if exists {
		t.Fatalf("Release(): entry for %q still present after Release", msgID)
	}
}

func TestRelease_Idempotent(t *testing.T) {
	a := New()
	inbox := makeInbox(1)
	_ = a.Start(context.Background(), inbox)

	_, msgID := a.Receive("agent1", "user1", "alice", "hello")
	<-inbox

	// Two Release calls must not panic or return an error.
	a.Release(msgID)
	a.Release(msgID)
}

func TestRelease_UnknownID_NoOp(t *testing.T) {
	a := New()
	// Should not panic on an ID that was never registered.
	a.Release("non-existent-id")
}

// ---- Send -------------------------------------------------------------------

func TestSend_DeliversTextToReplyCh(t *testing.T) {
	a := New()
	inbox := makeInbox(1)
	_ = a.Start(context.Background(), inbox)

	replyCh, msgID := a.Receive("agent1", "user1", "alice", "question")
	<-inbox // drain inbox

	err := a.Send(context.Background(), message.Message{
		ID:    msgID,
		Parts: message.Text("the answer"),
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	select {
	case reply := <-replyCh:
		if reply != "the answer" {
			t.Fatalf("reply = %q, want %q", reply, "the answer")
		}
	default:
		t.Fatal("Send(): reply not delivered to reply channel")
	}
}

func TestSend_RemovesEntryFromMap(t *testing.T) {
	a := New()
	inbox := makeInbox(1)
	_ = a.Start(context.Background(), inbox)

	_, msgID := a.Receive("agent1", "user1", "alice", "question")
	<-inbox

	_ = a.Send(context.Background(), message.Message{
		ID:    msgID,
		Parts: message.Text("answer"),
	})

	a.mu.Lock()
	_, exists := a.responses[msgID]
	a.mu.Unlock()

	if exists {
		t.Fatalf("Send(): entry for %q still present after Send", msgID)
	}
}

func TestSend_NoOp_WhenMsgIDNotInMap(t *testing.T) {
	a := New()
	// No prior Receive — should be a silent no-op.
	err := a.Send(context.Background(), message.Message{
		ID:    "ghost-id",
		Parts: message.Text("ignored"),
	})
	if err != nil {
		t.Fatalf("Send() with unknown msgID error = %v, want nil", err)
	}
}

func TestSend_NoOp_WhenEmptyParts(t *testing.T) {
	a := New()
	inbox := makeInbox(1)
	_ = a.Start(context.Background(), inbox)

	replyCh, msgID := a.Receive("agent1", "user1", "alice", "question")
	<-inbox

	err := a.Send(context.Background(), message.Message{
		ID:    msgID,
		Parts: nil,
	})
	if err != nil {
		t.Fatalf("Send() with empty parts error = %v, want nil", err)
	}

	// replyCh must not have received anything.
	select {
	case v := <-replyCh:
		t.Fatalf("Send() with empty parts unexpectedly delivered %q to reply channel", v)
	default:
		// correct — nothing delivered
	}

	// Entry should still exist because Send returned early without touching the map.
	a.mu.Lock()
	_, exists := a.responses[msgID]
	a.mu.Unlock()
	if !exists {
		t.Fatal("Send() with empty parts should not remove entry from responses map")
	}
}

func TestSend_ReturnsNilError(t *testing.T) {
	a := New()
	inbox := makeInbox(1)
	_ = a.Start(context.Background(), inbox)

	_, msgID := a.Receive("agent1", "user1", "alice", "q")
	<-inbox

	err := a.Send(context.Background(), message.Message{
		ID:    msgID,
		Parts: message.Text("a"),
	})
	if err != nil {
		t.Fatalf("Send() error = %v, want nil", err)
	}
}

// ---- Receive + Send round-trip ----------------------------------------------

func TestRoundTrip_ReceiveThenSend(t *testing.T) {
	a := New()
	inbox := makeInbox(1)
	_ = a.Start(context.Background(), inbox)

	replyCh, msgID := a.Receive("agent1", "user1", "alice", "ping")

	inbound := <-inbox
	if inbound.ID != msgID {
		t.Fatalf("inbound.ID = %q, want %q", inbound.ID, msgID)
	}

	// Simulate the engine replying.
	err := a.Send(context.Background(), message.Message{
		ID:    inbound.ID,
		Parts: message.Text("pong"),
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	reply := <-replyCh
	if reply != "pong" {
		t.Fatalf("reply = %q, want pong", reply)
	}
}

func TestRoundTrip_MultipleSequential(t *testing.T) {
	a := New()
	inbox := makeInbox(4)
	_ = a.Start(context.Background(), inbox)

	for i, pair := range []struct{ q, r string }{
		{"first question", "first answer"},
		{"second question", "second answer"},
		{"third question", "third answer"},
	} {
		replyCh, msgID := a.Receive("agent1", "user1", "alice", pair.q)
		inbound := <-inbox
		if inbound.ID != msgID {
			t.Fatalf("pair %d: ID mismatch", i)
		}
		_ = a.Send(context.Background(), message.Message{
			ID:    msgID,
			Parts: message.Text(pair.r),
		})
		got := <-replyCh
		if got != pair.r {
			t.Fatalf("pair %d: reply = %q, want %q", i, got, pair.r)
		}
	}
}

// ---- Release after Send (double-free) ---------------------------------------

func TestRelease_AfterSend_IsNoOp(t *testing.T) {
	a := New()
	inbox := makeInbox(1)
	_ = a.Start(context.Background(), inbox)

	_, msgID := a.Receive("agent1", "user1", "alice", "q")
	<-inbox

	_ = a.Send(context.Background(), message.Message{
		ID:    msgID,
		Parts: message.Text("a"),
	})
	// Release after Send already cleaned up — must be a no-op.
	a.Release(msgID)
}

// ---- Concurrent Receive + Release -------------------------------------------

func TestConcurrentReceiveRelease_NoRaceOrPanic(t *testing.T) {
	a := New()
	// Large inbox to prevent Receive from blocking under high concurrency.
	inbox := makeInbox(200)
	_ = a.Start(context.Background(), inbox)

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, msgID := a.Receive("agent1", "userX", "x", "concurrent")
			a.Release(msgID)
		}()
	}
	wg.Wait()

	// All goroutines released their entries; map must be empty.
	a.mu.Lock()
	remaining := len(a.responses)
	a.mu.Unlock()

	if remaining != 0 {
		t.Fatalf("after concurrent Receive+Release, %d entries remain in map", remaining)
	}
}

func TestConcurrentSend_NoRaceOrPanic(t *testing.T) {
	a := New()
	const n = 50
	inbox := makeInbox(n)
	_ = a.Start(context.Background(), inbox)

	ids := make([]string, n)
	for i := 0; i < n; i++ {
		_, ids[i] = a.Receive("agent1", "userX", "x", "msg")
	}

	// Drain inbox.
	for i := 0; i < n; i++ {
		<-inbox
	}

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		msgID := ids[i]
		go func() {
			defer wg.Done()
			_ = a.Send(context.Background(), message.Message{
				ID:    msgID,
				Parts: message.Text("reply"),
			})
		}()
	}
	wg.Wait()
}
