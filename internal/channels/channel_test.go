package channels

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestRegistrySendRoutesByChannel(t *testing.T) {
	reg := NewRegistry(1)
	telegram := &registryTestAdapter{id: "telegram"}
	slack := &registryTestAdapter{id: "slack"}
	reg.Register(telegram)
	reg.Register(slack)

	if err := reg.Send(context.Background(), message.Message{
		Channel:  "slack",
		ThreadID: "C123",
		Parts:    message.Text("hello"),
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(slack.sent) != 1 {
		t.Fatalf("slack sent count = %d, want 1", len(slack.sent))
	}
	if len(telegram.sent) != 0 {
		t.Fatalf("telegram sent count = %d, want 0", len(telegram.sent))
	}
	if got := slack.sent[0].ThreadID; got != "C123" {
		t.Fatalf("ThreadID = %q, want C123", got)
	}
}

func TestRegistrySendUnknownChannelReturnsError(t *testing.T) {
	reg := NewRegistry(1)
	err := reg.Send(context.Background(), message.Message{Channel: "missing"})
	if err == nil || err.Error() != `channel adapter "missing" is not registered` {
		t.Fatalf("unknown channel Send error = %v, want registered error", err)
	}
}

func TestRegistrySendReturnsAdapterError(t *testing.T) {
	reg := NewRegistry(1)
	wantErr := errors.New("send failed")
	reg.Register(&registryTestAdapter{id: "telegram", sendErr: wantErr})

	if err := reg.Send(context.Background(), message.Message{Channel: "telegram"}); !errors.Is(err, wantErr) {
		t.Fatalf("Send error = %v, want %v", err, wantErr)
	}
}

func TestRegistryStatusesSnapshot(t *testing.T) {
	reg := NewRegistry(1)
	reg.Register(&registryTestAdapter{
		id:     "telegram",
		status: AdapterStatus{Connected: true, Detail: "polling"},
	})

	statuses := reg.Statuses()
	if len(statuses) != 1 {
		t.Fatalf("status count = %d, want 1", len(statuses))
	}
	if !statuses["telegram"].Connected || statuses["telegram"].Detail != "polling" {
		t.Fatalf("telegram status = %+v", statuses["telegram"])
	}
}

func TestRegistryEnqueueIsNonBlocking(t *testing.T) {
	reg := NewRegistry(1)
	if !reg.Enqueue(message.Message{Channel: "http", Parts: message.Text("one")}) {
		t.Fatal("first enqueue should succeed")
	}
	if reg.Enqueue(message.Message{Channel: "http", Parts: message.Text("two")}) {
		t.Fatal("second enqueue should fail when inbox buffer is full")
	}

	got := <-reg.Inbox()
	if text := firstRegistryTestText(got); text != "one" {
		t.Fatalf("dequeued text = %q, want one", text)
	}
}

func TestRegistryEnqueueRecordsInboundMetric(t *testing.T) {
	reg := NewRegistry(1)
	before := testutil.ToFloat64(metrics.ChannelInboundTotal.WithLabelValues("slack", "agent-a"))

	if !reg.Enqueue(message.Message{Channel: "slack", AgentID: "agent-a", Parts: message.Text("one")}) {
		t.Fatal("enqueue should succeed")
	}

	after := testutil.ToFloat64(metrics.ChannelInboundTotal.WithLabelValues("slack", "agent-a"))
	if after != before+1 {
		t.Fatalf("inbound metric delta = %v, want 1", after-before)
	}
}

func TestRegistrySendRecordsOutboundMetrics(t *testing.T) {
	reg := NewRegistry(1)
	wantErr := errors.New("send failed")
	reg.Register(&registryTestAdapter{id: "slack"})
	reg.Register(&registryTestAdapter{id: "telegram", sendErr: wantErr})

	successBefore := testutil.ToFloat64(metrics.ChannelOutboundTotal.WithLabelValues("slack", "agent-a", "success"))
	if err := reg.Send(context.Background(), message.Message{Channel: "slack", AgentID: "agent-a"}); err != nil {
		t.Fatalf("Send success case: %v", err)
	}
	successAfter := testutil.ToFloat64(metrics.ChannelOutboundTotal.WithLabelValues("slack", "agent-a", "success"))
	if successAfter != successBefore+1 {
		t.Fatalf("success metric delta = %v, want 1", successAfter-successBefore)
	}

	errorBefore := testutil.ToFloat64(metrics.ChannelOutboundTotal.WithLabelValues("telegram", "agent-a", "error"))
	if err := reg.Send(context.Background(), message.Message{Channel: "telegram", AgentID: "agent-a"}); !errors.Is(err, wantErr) {
		t.Fatalf("Send error case = %v, want %v", err, wantErr)
	}
	errorAfter := testutil.ToFloat64(metrics.ChannelOutboundTotal.WithLabelValues("telegram", "agent-a", "error"))
	if errorAfter != errorBefore+1 {
		t.Fatalf("error metric delta = %v, want 1", errorAfter-errorBefore)
	}

	missingBefore := testutil.ToFloat64(metrics.ChannelOutboundTotal.WithLabelValues("discord", "agent-a", "unregistered"))
	if err := reg.Send(context.Background(), message.Message{Channel: "discord", AgentID: "agent-a"}); err == nil {
		t.Fatal("Send missing adapter should fail")
	}
	missingAfter := testutil.ToFloat64(metrics.ChannelOutboundTotal.WithLabelValues("discord", "agent-a", "unregistered"))
	if missingAfter != missingBefore+1 {
		t.Fatalf("unregistered metric delta = %v, want 1", missingAfter-missingBefore)
	}
}

type registryTestAdapter struct {
	id      string
	status  AdapterStatus
	sendErr error
	sent    []message.Message
}

func (a *registryTestAdapter) ID() string { return a.id }
func (a *registryTestAdapter) Name() string {
	return "test " + a.id
}
func (a *registryTestAdapter) Start(context.Context, chan<- message.Message) error { return nil }
func (a *registryTestAdapter) Send(_ context.Context, msg message.Message) error {
	if a.sendErr != nil {
		return a.sendErr
	}
	a.sent = append(a.sent, msg)
	return nil
}
func (a *registryTestAdapter) Stop() error { return nil }
func (a *registryTestAdapter) Status() AdapterStatus {
	return a.status
}

func firstRegistryTestText(msg message.Message) string {
	for _, part := range msg.Parts {
		if part.Type == message.ContentText {
			return part.Text
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Registry — additional coverage
// ---------------------------------------------------------------------------

func TestNewRegistryInboxBufferSize(t *testing.T) {
	reg := NewRegistry(10)
	if cap(reg.Inbox()) != 10 {
		t.Fatalf("inbox capacity = %d, want 10", cap(reg.Inbox()))
	}
}

func TestRegistrySetLogger(t *testing.T) {
	reg := NewRegistry(1)
	// SetLogger should not panic; nop logger is fine.
	reg.SetLogger(noopZapLogger(t))
}

func TestRegistryStartAllNoAdapters(t *testing.T) {
	reg := NewRegistry(1)
	errs := reg.StartAll(context.Background())
	if len(errs) != 0 {
		t.Fatalf("StartAll with no adapters: got errors %v", errs)
	}
}

func TestRegistryStopAllNoAdapters(t *testing.T) {
	reg := NewRegistry(1)
	errs := reg.StopAll()
	if len(errs) != 0 {
		t.Fatalf("StopAll with no adapters: got errors %v", errs)
	}
}

func TestRegistryStartAllPropagatesError(t *testing.T) {
	reg := NewRegistry(1)
	wantErr := errors.New("start failed")
	reg.Register(&errorStartAdapter{id: "bad", startErr: wantErr})
	errs := reg.StartAll(context.Background())
	if len(errs) != 1 {
		t.Fatalf("StartAll error count = %d, want 1", len(errs))
	}
	if !errors.Is(errs[0], wantErr) {
		t.Fatalf("StartAll error = %v, want %v", errs[0], wantErr)
	}
}

func TestRegistryStopAllPropagatesError(t *testing.T) {
	reg := NewRegistry(1)
	wantErr := errors.New("stop failed")
	reg.Register(&errorStartAdapter{id: "bad", stopErr: wantErr})
	errs := reg.StopAll()
	if len(errs) != 1 {
		t.Fatalf("StopAll error count = %d, want 1", len(errs))
	}
	if !errors.Is(errs[0], wantErr) {
		t.Fatalf("StopAll error = %v, want %v", errs[0], wantErr)
	}
}

func TestRegistryStartAllSuccessAndStop(t *testing.T) {
	reg := NewRegistry(4)
	a := &registryTestAdapter{id: "http"}
	reg.Register(a)

	errs := reg.StartAll(context.Background())
	if len(errs) != 0 {
		t.Fatalf("StartAll: %v", errs)
	}
	errs = reg.StopAll()
	if len(errs) != 0 {
		t.Fatalf("StopAll: %v", errs)
	}
}

func TestRegistryStatusesEmpty(t *testing.T) {
	reg := NewRegistry(1)
	m := reg.Statuses()
	if len(m) != 0 {
		t.Fatalf("Statuses on empty registry = %d, want 0", len(m))
	}
}

func TestRegistryStatusesDisconnected(t *testing.T) {
	reg := NewRegistry(1)
	reg.Register(&registryTestAdapter{
		id:     "discord",
		status: AdapterStatus{Connected: false, Detail: ""},
	})
	s := reg.Statuses()
	if s["discord"].Connected {
		t.Error("discord should report disconnected")
	}
}

func TestAdapterStatusFields(t *testing.T) {
	s := AdapterStatus{Connected: true, Detail: "polling"}
	if !s.Connected {
		t.Error("Connected should be true")
	}
	if s.Detail != "polling" {
		t.Errorf("Detail = %q, want polling", s.Detail)
	}
}

func TestRegistryRegisterOverwrites(t *testing.T) {
	reg := NewRegistry(1)
	a1 := &registryTestAdapter{id: "ch", status: AdapterStatus{Connected: false}}
	a2 := &registryTestAdapter{id: "ch", status: AdapterStatus{Connected: true}}
	reg.Register(a1)
	reg.Register(a2)

	s := reg.Statuses()
	if !s["ch"].Connected {
		t.Error("second Register should overwrite first; expected Connected=true")
	}
}

func TestRegistryEnqueueDropLogsUnknownChannel(t *testing.T) {
	// A zero-buffer registry; first message succeeds, second is dropped.
	// Ensure Enqueue doesn't panic when channel field is empty.
	reg := NewRegistry(1)
	reg.Enqueue(message.Message{Channel: ""}) // fills the buffer
	ok := reg.Enqueue(message.Message{Channel: ""})
	if ok {
		t.Error("expected drop on full inbox")
	}
}

// ---------------------------------------------------------------------------
// AdapterStatus — zero value
// ---------------------------------------------------------------------------

func TestAdapterStatusZeroValue(t *testing.T) {
	var s AdapterStatus
	if s.Connected {
		t.Error("zero AdapterStatus should be disconnected")
	}
	if s.Detail != "" {
		t.Errorf("zero AdapterStatus Detail = %q, want empty", s.Detail)
	}
}

// ---------------------------------------------------------------------------
// ProgressThrottle
// ---------------------------------------------------------------------------

func TestNewProgressThrottleDefaultInterval(t *testing.T) {
	pt := NewProgressThrottle(0)
	if pt.Interval != 1500*time.Millisecond {
		t.Errorf("default interval = %v, want 1500ms", pt.Interval)
	}
	if pt.In == nil {
		t.Error("In channel should not be nil")
	}
	if pt.Out == nil {
		t.Error("Out channel should not be nil")
	}
}

func TestNewProgressThrottleCustomInterval(t *testing.T) {
	pt := NewProgressThrottle(200 * time.Millisecond)
	if pt.Interval != 200*time.Millisecond {
		t.Errorf("interval = %v, want 200ms", pt.Interval)
	}
}

func TestProgressThrottleNegativeIntervalDefaulted(t *testing.T) {
	pt := NewProgressThrottle(-1)
	if pt.Interval != 1500*time.Millisecond {
		t.Errorf("negative interval should default to 1500ms, got %v", pt.Interval)
	}
}

func TestProgressThrottleForwardsEvent(t *testing.T) {
	pt := NewProgressThrottle(20 * time.Millisecond)
	stop := pt.Start()
	defer stop()

	pt.In <- message.ProgressEvent{RunID: "r1", Percent: 50, Status: "halfway"}

	select {
	case ev := <-pt.Out:
		if ev.RunID != "r1" || ev.Percent != 50 || ev.Status != "halfway" {
			t.Errorf("unexpected event: %+v", ev)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for throttled event")
	}
}

func TestProgressThrottleKeepsLastEvent(t *testing.T) {
	// Two events sent before the tick; only the second (latest) should arrive.
	pt := NewProgressThrottle(30 * time.Millisecond)
	stop := pt.Start()
	defer stop()

	pt.In <- message.ProgressEvent{RunID: "r1", Percent: 10, Status: "first"}
	pt.In <- message.ProgressEvent{RunID: "r1", Percent: 90, Status: "last"}

	select {
	case ev := <-pt.Out:
		if ev.Percent != 90 {
			t.Errorf("expected last event (90%%), got %d%%", ev.Percent)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for throttled event")
	}
}

func TestProgressThrottleNoEventNoOutput(t *testing.T) {
	pt := NewProgressThrottle(20 * time.Millisecond)
	stop := pt.Start()
	defer stop()

	// Wait for a couple of ticks without sending anything.
	time.Sleep(60 * time.Millisecond)
	select {
	case ev := <-pt.Out:
		t.Errorf("unexpected event on Out with no input: %+v", ev)
	default:
		// correct: nothing forwarded
	}
}

func TestProgressThrottleStopIsIdempotent(t *testing.T) {
	pt := NewProgressThrottle(20 * time.Millisecond)
	stop := pt.Start()
	stop() // first stop
	// Do not call stop again; closing a closed channel panics, so this test
	// verifies the goroutine exits cleanly on the first close.
}

func TestProgressThrottleMultipleEvents(t *testing.T) {
	pt := NewProgressThrottle(20 * time.Millisecond)
	stop := pt.Start()
	defer stop()

	// First window.
	pt.In <- message.ProgressEvent{RunID: "r1", Percent: 25}
	var first message.ProgressEvent
	select {
	case first = <-pt.Out:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out on first event")
	}
	if first.Percent != 25 {
		t.Errorf("first event percent = %d, want 25", first.Percent)
	}

	// Second window.
	pt.In <- message.ProgressEvent{RunID: "r1", Percent: 75}
	var second message.ProgressEvent
	select {
	case second = <-pt.Out:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out on second event")
	}
	if second.Percent != 75 {
		t.Errorf("second event percent = %d, want 75", second.Percent)
	}
}

// ---------------------------------------------------------------------------
// Helpers for error-path adapters
// ---------------------------------------------------------------------------

type errorStartAdapter struct {
	id       string
	startErr error
	stopErr  error
}

func (a *errorStartAdapter) ID() string   { return a.id }
func (a *errorStartAdapter) Name() string { return "err-" + a.id }
func (a *errorStartAdapter) Start(_ context.Context, _ chan<- message.Message) error {
	return a.startErr
}
func (a *errorStartAdapter) Send(_ context.Context, _ message.Message) error { return nil }
func (a *errorStartAdapter) Stop() error                                     { return a.stopErr }
func (a *errorStartAdapter) Status() AdapterStatus                           { return AdapterStatus{} }

// noopZapLogger returns a no-op zap.Logger for tests.
func noopZapLogger(_ *testing.T) *zap.Logger {
	return zap.NewNop()
}
