package nats

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/soulacy/soulacy/internal/queue"
)

func TestSubscribeRejectsCancelledContextAndMissingHandler(t *testing.T) {
	b := &Backend{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Subscribe(ctx, "test", "", func(*queue.Message) {}); err != context.Canceled {
		t.Fatalf("cancelled subscribe: %v", err)
	}
	if _, err := b.Subscribe(context.Background(), "test", "", nil); err == nil {
		t.Fatal("accepted nil handler")
	}
}

// Runs against a disposable loopback broker when nats-server is installed.
func TestBrokerSubscriptionLifecycle(t *testing.T) {
	bin := os.Getenv("SOULACY_TEST_NATS_SERVER")
	if bin == "" {
		bin, _ = exec.LookPath("nats-server")
	}
	if bin == "" {
		t.Skip("nats-server is not installed")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	dir := t.TempDir()
	log, err := os.Create(filepath.Join(dir, "broker.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-js", "-a", "127.0.0.1", "-p", fmt.Sprint(port), "-sd", filepath.Join(dir, "data"))
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = log.Close() })
	url := fmt.Sprintf("nats://127.0.0.1:%d", port)
	ready := false
	for i := 0; i < 100; i++ {
		conn, err := natsgo.Connect(url, natsgo.Timeout(100*time.Millisecond), natsgo.NoReconnect())
		if err == nil {
			conn.Close()
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("local broker did not become ready")
	}
	b, err := New(Config{URL: url, StreamName: "review"})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan *queue.Message, 1)
	sub, err := b.Subscribe(ctx, "review.events", "", func(m *queue.Message) { received <- m })
	if err != nil {
		t.Fatal(err)
	}
	wrapped := sub.(*natsSub)
	info, err := wrapped.sub.ConsumerInfo()
	if err != nil {
		t.Fatal(err)
	}
	if info.Config.Durable != "" {
		t.Fatalf("temporary subscription created durable consumer %q", info.Config.Durable)
	}
	if err := b.Publish(ctx, "review.events", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-received:
		if string(msg.Data) != "hello" {
			t.Fatalf("payload %q", msg.Data)
		}
		if err := msg.Ack(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("message not delivered")
	}
	cancel()
	select {
	case <-wrapped.done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not unsubscribe")
	}
	if err := wrapped.Unsubscribe(); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	remaining := len(b.subs)
	b.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("retained %d subscriptions", remaining)
	}
	// Context-triggered consumer deletion is asynchronous in the NATS client.
	removed := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		state, err := b.js.StreamInfo("review")
		if err != nil {
			t.Fatal(err)
		}
		if state.State.Consumers == 0 {
			removed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !removed {
		t.Fatal("temporary consumer was not removed")
	}
	active, err := b.Subscribe(context.Background(), "review.events", "", func(*queue.Message) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-active.(*natsSub).done:
	default:
		t.Fatal("close left subscription open")
	}
	if _, err := b.Subscribe(context.Background(), "review.events", "", func(*queue.Message) {}); err == nil {
		t.Fatal("subscribed after close")
	}
}
