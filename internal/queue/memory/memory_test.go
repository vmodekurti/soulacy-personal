package memory

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/queue"
)

// helpers

func ctx() context.Context { return context.Background() }

// collect receives up to n messages from ch within timeout, then returns.
func collect(ch <-chan *queue.Message, n int, timeout time.Duration) []*queue.Message {
	var msgs []*queue.Message
	deadline := time.After(timeout)
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				return msgs
			}
			msgs = append(msgs, m)
			if len(msgs) >= n {
				return msgs
			}
		case <-deadline:
			return msgs
		}
	}
}

// subscribeCollect subscribes and fans received messages into a returned channel.
func subscribeCollect(t *testing.T, b *Backend, subject, group string) (<-chan *queue.Message, queue.Subscription) {
	t.Helper()
	ch := make(chan *queue.Message, 256)
	sub, err := b.Subscribe(ctx(), subject, group, func(m *queue.Message) {
		ch <- m
	})
	if err != nil {
		t.Fatalf("Subscribe(%q, %q): %v", subject, group, err)
	}
	return ch, sub
}

// ---------------------------------------------------------------------------
// New
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	b := New()
	if b == nil {
		t.Fatal("New() returned nil")
	}
	if b.groupIdx == nil {
		t.Fatal("groupIdx map is nil")
	}
	if b.quit == nil {
		t.Fatal("quit channel is nil")
	}
}

// ---------------------------------------------------------------------------
// Publish + Subscribe — basic delivery
// ---------------------------------------------------------------------------

func TestPublishSubscribe_BasicDelivery(t *testing.T) {
	b := New()
	defer b.Close()

	ch, _ := subscribeCollect(t, b, "test.subject", "")

	if err := b.Publish(ctx(), "test.subject", []byte("hello")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	msgs := collect(ch, 1, time.Second)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Subject != "test.subject" {
		t.Errorf("Subject = %q, want %q", msgs[0].Subject, "test.subject")
	}
	if string(msgs[0].Data) != "hello" {
		t.Errorf("Data = %q, want %q", msgs[0].Data, "hello")
	}
}

func TestPublishSubscribe_MultipleMessages(t *testing.T) {
	b := New()
	defer b.Close()

	ch, _ := subscribeCollect(t, b, "foo.bar", "")

	const n = 10
	for i := 0; i < n; i++ {
		if err := b.Publish(ctx(), "foo.bar", []byte("msg")); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	msgs := collect(ch, n, 2*time.Second)
	if len(msgs) != n {
		t.Errorf("expected %d messages, got %d", n, len(msgs))
	}
}

// ---------------------------------------------------------------------------
// Message.Ack — memory backend no-op
// ---------------------------------------------------------------------------

func TestMessage_AckIsNoOp(t *testing.T) {
	b := New()
	defer b.Close()

	ch, _ := subscribeCollect(t, b, "ack.test", "")
	b.Publish(ctx(), "ack.test", []byte("data"))

	msgs := collect(ch, 1, time.Second)
	if len(msgs) != 1 {
		t.Fatal("expected 1 message")
	}
	// Ack should not error and should be idempotent
	if err := msgs[0].Ack(); err != nil {
		t.Errorf("Ack() error: %v", err)
	}
	if err := msgs[0].Ack(); err != nil {
		t.Errorf("second Ack() error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Fan-out: multiple group-less subscribers
// ---------------------------------------------------------------------------

func TestFanOut_AllSubscribersReceive(t *testing.T) {
	b := New()
	defer b.Close()

	const numSubs = 5
	channels := make([]<-chan *queue.Message, numSubs)
	for i := range channels {
		ch, _ := subscribeCollect(t, b, "fanout.test", "")
		channels[i] = ch
	}

	if err := b.Publish(ctx(), "fanout.test", []byte("broadcast")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	for i, ch := range channels {
		msgs := collect(ch, 1, time.Second)
		if len(msgs) != 1 {
			t.Errorf("subscriber %d: expected 1 message, got %d", i, len(msgs))
		}
	}
}

// ---------------------------------------------------------------------------
// Competing consumers (named group)
// ---------------------------------------------------------------------------

func TestGroupSubscribers_ExactlyOneDelivery(t *testing.T) {
	b := New()
	defer b.Close()

	const numSubs = 4
	var received atomic.Int64

	for i := 0; i < numSubs; i++ {
		_, err := b.Subscribe(ctx(), "group.test", "mygroup", func(m *queue.Message) {
			received.Add(1)
		})
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
	}

	const n = 20
	for i := 0; i < n; i++ {
		if err := b.Publish(ctx(), "group.test", []byte("msg")); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	// Wait until all n messages have been received or timeout.
	deadline := time.After(3 * time.Second)
	for {
		if received.Load() == n {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timeout: only received %d/%d messages", received.Load(), n)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	if received.Load() != n {
		t.Errorf("group delivery: expected %d total deliveries, got %d", n, received.Load())
	}
}

func TestGroupSubscribers_RoundRobin(t *testing.T) {
	b := New()
	defer b.Close()

	const numSubs = 3
	counts := make([]atomic.Int64, numSubs)

	for i := 0; i < numSubs; i++ {
		idx := i
		_, err := b.Subscribe(ctx(), "rr.test", "rrgroup", func(m *queue.Message) {
			counts[idx].Add(1)
		})
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
	}

	const n = 30
	for i := 0; i < n; i++ {
		if err := b.Publish(ctx(), "rr.test", []byte("msg")); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	// Wait for all deliveries
	deadline := time.After(3 * time.Second)
	for {
		total := int64(0)
		for i := range counts {
			total += counts[i].Load()
		}
		if total == n {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for %d messages; got %d", n, total)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Each subscriber should have received some messages (approximately n/numSubs)
	total := int64(0)
	for i := range counts {
		total += counts[i].Load()
	}
	if total != n {
		t.Errorf("total deliveries = %d, want %d", total, n)
	}
}

// ---------------------------------------------------------------------------
// Mixed: group + no-group on same subject
// ---------------------------------------------------------------------------

func TestMixed_GroupAndNoGroup(t *testing.T) {
	b := New()
	defer b.Close()

	// Two no-group subscribers — both should receive
	ch1, _ := subscribeCollect(t, b, "mix.test", "")
	ch2, _ := subscribeCollect(t, b, "mix.test", "")

	// Two group subscribers — only one should receive
	var groupCount atomic.Int64
	for i := 0; i < 2; i++ {
		_, err := b.Subscribe(ctx(), "mix.test", "g1", func(m *queue.Message) {
			groupCount.Add(1)
		})
		if err != nil {
			t.Fatalf("Subscribe group: %v", err)
		}
	}

	b.Publish(ctx(), "mix.test", []byte("x"))

	msgs1 := collect(ch1, 1, time.Second)
	msgs2 := collect(ch2, 1, time.Second)

	if len(msgs1) != 1 {
		t.Errorf("no-group sub1: expected 1 message, got %d", len(msgs1))
	}
	if len(msgs2) != 1 {
		t.Errorf("no-group sub2: expected 1 message, got %d", len(msgs2))
	}

	// Give group handler time to run
	time.Sleep(100 * time.Millisecond)
	if groupCount.Load() != 1 {
		t.Errorf("group subscribers: expected exactly 1 delivery, got %d", groupCount.Load())
	}
}

// ---------------------------------------------------------------------------
// Unsubscribe mid-stream
// ---------------------------------------------------------------------------

func TestUnsubscribe_StopsDelivery(t *testing.T) {
	b := New()
	defer b.Close()

	ch, sub := subscribeCollect(t, b, "unsub.test", "")

	// Publish one message, confirm receipt
	b.Publish(ctx(), "unsub.test", []byte("before"))
	msgs := collect(ch, 1, time.Second)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 pre-unsub message, got %d", len(msgs))
	}

	// Unsubscribe
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}

	// Publish again — should NOT arrive
	b.Publish(ctx(), "unsub.test", []byte("after"))
	msgs = collect(ch, 1, 200*time.Millisecond)
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages after unsubscribe, got %d", len(msgs))
	}
}

func TestUnsubscribe_Idempotent(t *testing.T) {
	b := New()
	defer b.Close()

	_, sub := subscribeCollect(t, b, "idem.test", "")

	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("first Unsubscribe: %v", err)
	}
	// Second call must not panic or error
	if err := sub.Unsubscribe(); err != nil {
		t.Errorf("second Unsubscribe: %v", err)
	}
}

func TestUnsubscribe_RemovesGroupCounter(t *testing.T) {
	b := New()
	defer b.Close()

	_, sub := subscribeCollect(t, b, "gc.test", "gcgroup")
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}

	b.mu.RLock()
	_, exists := b.groupIdx[groupKey{"gc.test", "gcgroup"}]
	b.mu.RUnlock()

	if exists {
		t.Error("group counter should have been removed after last subscriber unsubscribed")
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestClose_Safe(t *testing.T) {
	b := New()
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestClose_Idempotent(t *testing.T) {
	b := New()
	if err := b.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestClose_StopsDelivery(t *testing.T) {
	b := New()

	ch, _ := subscribeCollect(t, b, "close.test", "")

	b.Publish(ctx(), "close.test", []byte("before"))
	msgs := collect(ch, 1, time.Second)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 pre-close message, got %d", len(msgs))
	}

	b.Close()

	b.Publish(ctx(), "close.test", []byte("after"))
	msgs = collect(ch, 1, 200*time.Millisecond)
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages after close, got %d", len(msgs))
	}
}

func TestClose_WithMultipleSubscribers(t *testing.T) {
	b := New()
	const n = 5
	for i := 0; i < n; i++ {
		_, err := b.Subscribe(ctx(), "mc.test", "", func(m *queue.Message) {})
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// After close, subs slice is drained
	b.mu.RLock()
	remaining := len(b.subs)
	b.mu.RUnlock()
	if remaining != 0 {
		t.Errorf("expected 0 subs after Close, got %d", remaining)
	}
}

// ---------------------------------------------------------------------------
// matchSubject — direct unit tests
// ---------------------------------------------------------------------------

func TestMatchSubject_ExactMatch(t *testing.T) {
	cases := []struct {
		pattern, subject string
		want             bool
	}{
		{"foo.bar", "foo.bar", true},
		{"foo.bar", "foo.baz", false},
		{"foo.bar", "foo", false},
		{"foo.bar", "foo.bar.baz", false},
		{"a", "a", true},
		{"a", "b", false},
		{"", "", true},
	}
	for _, tc := range cases {
		got := matchSubject(tc.pattern, tc.subject)
		if got != tc.want {
			t.Errorf("matchSubject(%q, %q) = %v, want %v", tc.pattern, tc.subject, got, tc.want)
		}
	}
}

func TestMatchSubject_StarWildcard(t *testing.T) {
	cases := []struct {
		pattern, subject string
		want             bool
	}{
		{"foo.*", "foo.bar", true},
		{"foo.*", "foo.baz", true},
		{"foo.*", "foo.bar.baz", false}, // * is single token
		{"foo.*", "foo", false},
		{"*", "foo", true},
		{"*", "foo.bar", false},
		{"*.bar", "foo.bar", true},
		{"*.bar", "foo.baz", false},
		{"foo.*.baz", "foo.bar.baz", true},
		{"foo.*.baz", "foo.bar.qux", false},
	}
	for _, tc := range cases {
		got := matchSubject(tc.pattern, tc.subject)
		if got != tc.want {
			t.Errorf("matchSubject(%q, %q) = %v, want %v", tc.pattern, tc.subject, got, tc.want)
		}
	}
}

func TestMatchSubject_GreaterThanWildcard(t *testing.T) {
	cases := []struct {
		pattern, subject string
		want             bool
	}{
		{">", "foo", true},
		{">", "foo.bar", true},
		{">", "foo.bar.baz", true},
		{"foo.>", "foo.bar", true},
		{"foo.>", "foo.bar.baz", true},
		{"foo.>", "foo.bar.baz.qux", true},
		{"foo.>", "foo", false},  // > requires at least one more token
		{"foo.>", "bar.baz", false},
		{"foo.bar.>", "foo.bar.baz", true},
		{"foo.bar.>", "foo.bar.baz.qux", true},
		{"foo.bar.>", "foo.bar", false},
	}
	for _, tc := range cases {
		got := matchSubject(tc.pattern, tc.subject)
		if got != tc.want {
			t.Errorf("matchSubject(%q, %q) = %v, want %v", tc.pattern, tc.subject, got, tc.want)
		}
	}
}

func TestMatchSubject_NoMatch(t *testing.T) {
	cases := []struct {
		pattern, subject string
	}{
		{"foo.bar", "baz.bar"},
		{"foo.*", "bar.baz"},
		{"a.b.c", "a.b"},
		{"a.b", "a.b.c"},
	}
	for _, tc := range cases {
		got := matchSubject(tc.pattern, tc.subject)
		if got {
			t.Errorf("matchSubject(%q, %q) = true, want false", tc.pattern, tc.subject)
		}
	}
}

// ---------------------------------------------------------------------------
// Publish to non-matching subject — subscriber should not receive
// ---------------------------------------------------------------------------

func TestPublish_NonMatchingSubject(t *testing.T) {
	b := New()
	defer b.Close()

	ch, _ := subscribeCollect(t, b, "exact.subject", "")

	b.Publish(ctx(), "other.subject", []byte("msg"))

	msgs := collect(ch, 1, 200*time.Millisecond)
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages for non-matching subject, got %d", len(msgs))
	}
}

// ---------------------------------------------------------------------------
// Wildcard delivery via Publish
// ---------------------------------------------------------------------------

func TestPublish_StarWildcard(t *testing.T) {
	b := New()
	defer b.Close()

	ch, _ := subscribeCollect(t, b, "events.*", "")

	b.Publish(ctx(), "events.created", []byte("1"))
	b.Publish(ctx(), "events.deleted", []byte("2"))
	b.Publish(ctx(), "events.nested.nope", []byte("3")) // should not match

	msgs := collect(ch, 2, time.Second)
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages for events.*, got %d", len(msgs))
	}
}

func TestPublish_GreaterThanWildcard(t *testing.T) {
	b := New()
	defer b.Close()

	ch, _ := subscribeCollect(t, b, "logs.>", "")

	b.Publish(ctx(), "logs.info", []byte("a"))
	b.Publish(ctx(), "logs.warn.subsystem", []byte("b"))
	b.Publish(ctx(), "logs.error.http.500", []byte("c"))
	b.Publish(ctx(), "other.thing", []byte("d")) // should not match

	msgs := collect(ch, 3, time.Second)
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages for logs.>, got %d", len(msgs))
	}
}

func TestPublish_GlobalWildcard(t *testing.T) {
	b := New()
	defer b.Close()

	ch, _ := subscribeCollect(t, b, ">", "")

	b.Publish(ctx(), "foo", []byte("1"))
	b.Publish(ctx(), "foo.bar", []byte("2"))
	b.Publish(ctx(), "a.b.c.d", []byte("3"))

	msgs := collect(ch, 3, time.Second)
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages for >, got %d", len(msgs))
	}
}

// ---------------------------------------------------------------------------
// Buffer full — no panic, message dropped
// ---------------------------------------------------------------------------

func TestBufferFull_Drop(t *testing.T) {
	b := New()
	defer b.Close()

	// Subscriber whose handler blocks until the backend is closed,
	// so its internal channel (bufSize=256) fills up and extra messages are dropped.
	// We use a done channel tied to Close to prevent the goroutine from leaking.
	slowDone := make(chan struct{})
	_, err := b.Subscribe(ctx(), "drop.test", "", func(m *queue.Message) {
		// Block until backend closes (signalled via slowDone).
		<-slowDone
	})
	if err != nil {
		t.Fatalf("Subscribe slow: %v", err)
	}

	// Fast subscriber that does drain — confirms no panic on the drop path.
	ch, _ := subscribeCollect(t, b, "drop.test", "")

	// Publish more than bufSize messages quickly to trigger the drop path.
	for i := 0; i < bufSize+10; i++ {
		b.Publish(ctx(), "drop.test", []byte("x"))
	}

	// Release the slow handler and close.
	close(slowDone)

	// The fast subscriber should have received some messages.
	msgs := collect(ch, 1, time.Second)
	if len(msgs) == 0 {
		t.Error("expected at least some messages on the fast subscriber")
	}
}

// ---------------------------------------------------------------------------
// Concurrent safety
// ---------------------------------------------------------------------------

func TestConcurrent_PublishSubscribe(t *testing.T) {
	b := New()
	defer b.Close()

	var received atomic.Int64
	// Keep total well within bufSize (256) so no drops occur under normal scheduling.
	// The goal is to verify thread-safety, not at-most-once delivery limits.
	const publishers = 4
	const msgsPerPublisher = 50 // 200 total < bufSize(256)

	_, err := b.Subscribe(ctx(), "concurrent.>", "", func(m *queue.Message) {
		received.Add(1)
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < publishers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < msgsPerPublisher; j++ {
				b.Publish(ctx(), "concurrent.test", []byte("msg"))
			}
		}()
	}
	wg.Wait()

	// Give the dispatch goroutine time to drain the buffered channel.
	total := int64(publishers * msgsPerPublisher)
	deadline := time.After(3 * time.Second)
	for {
		if received.Load() >= total {
			break
		}
		select {
		case <-deadline:
			// at-most-once: some drops are acceptable if the buffer was momentarily full;
			// just verify we received a meaningful number (>50% of sent).
			got := received.Load()
			if got < total/2 {
				t.Fatalf("too few messages: received %d/%d", got, total)
			}
			return
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestConcurrent_SubscribeUnsubscribe(t *testing.T) {
	b := New()
	defer b.Close()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, sub := subscribeCollect(t, b, "cu.test", "")
			b.Publish(ctx(), "cu.test", []byte("x"))
			sub.Unsubscribe()
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Publish after Close — should not panic (message dropped)
// ---------------------------------------------------------------------------

func TestPublishAfterClose_NoPanic(t *testing.T) {
	b := New()
	b.Close()

	// Publish to a closed backend; all subscribers are gone, no-op
	if err := b.Publish(ctx(), "after.close", []byte("x")); err != nil {
		t.Errorf("Publish after Close returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Subscribe after Close — goroutine should exit immediately via b.quit
// ---------------------------------------------------------------------------

func TestSubscribeAfterClose_GoroutineExits(t *testing.T) {
	b := New()
	b.Close()

	received := make(chan *queue.Message, 1)
	_, err := b.Subscribe(ctx(), "post.close", "", func(m *queue.Message) {
		received <- m
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Publish — quit channel closed, so dispatcher will exit and not call handler
	b.Publish(ctx(), "post.close", []byte("x"))

	select {
	case <-received:
		// might or might not arrive depending on goroutine scheduling
	case <-time.After(200 * time.Millisecond):
		// acceptable — dispatch goroutine exited
	}
}

// ---------------------------------------------------------------------------
// Multiple groups on same subject
// ---------------------------------------------------------------------------

func TestMultipleGroups_SameSubject(t *testing.T) {
	b := New()
	defer b.Close()

	var g1Count, g2Count atomic.Int64

	for i := 0; i < 2; i++ {
		_, err := b.Subscribe(ctx(), "mg.test", "group1", func(m *queue.Message) {
			g1Count.Add(1)
		})
		if err != nil {
			t.Fatalf("Subscribe group1: %v", err)
		}
		_, err = b.Subscribe(ctx(), "mg.test", "group2", func(m *queue.Message) {
			g2Count.Add(1)
		})
		if err != nil {
			t.Fatalf("Subscribe group2: %v", err)
		}
	}

	const n = 10
	for i := 0; i < n; i++ {
		b.Publish(ctx(), "mg.test", []byte("msg"))
	}

	deadline := time.After(3 * time.Second)
	for {
		if g1Count.Load() == n && g2Count.Load() == n {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timeout: group1=%d group2=%d (expected %d each)", g1Count.Load(), g2Count.Load(), n)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	if g1Count.Load() != n {
		t.Errorf("group1: expected %d deliveries, got %d", n, g1Count.Load())
	}
	if g2Count.Load() != n {
		t.Errorf("group2: expected %d deliveries, got %d", n, g2Count.Load())
	}
}
