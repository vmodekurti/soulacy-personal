package gateway

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
)

// The restart handoff must not die when the previous instance still holds the
// port: it should wait and take over as soon as the socket is released.
func TestListenWithRestartHandoffWaitsForPort(t *testing.T) {
	// Hold a real port, exactly like an exiting-but-not-yet-gone gateway.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("seed listener: %v", err)
	}
	addr := held.Addr().String()

	// Release it shortly, mimicking the old process finally exiting.
	go func() { time.Sleep(600 * time.Millisecond); _ = held.Close() }()

	start := time.Now()
	ln, err := listenWithRestartHandoff(context.Background(), addr, zap.NewNop())
	if err != nil {
		t.Fatalf("handoff should have acquired the port, got: %v", err)
	}
	defer ln.Close()
	if waited := time.Since(start); waited < 300*time.Millisecond {
		t.Fatalf("expected to wait for the port to free (~600ms); returned in %s — did it retry?", waited)
	}
	if ln.Addr().String() != addr {
		t.Fatalf("bound %s, want %s", ln.Addr().String(), addr)
	}
}

// A caller cancellation while waiting must return promptly, not hang the window.
func TestListenWithRestartHandoffHonorsContext(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("seed listener: %v", err)
	}
	defer held.Close()
	addr := held.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()

	_, err = listenWithRestartHandoff(ctx, addr, zap.NewNop())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}
