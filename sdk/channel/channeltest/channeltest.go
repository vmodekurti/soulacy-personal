// Package channeltest is the official channel.Adapter conformance kit
// (Story E11). Extension authors run it out-of-tree against their adapter;
// the host runs it against every built-in in CI so the contract and the
// implementations cannot drift.
//
//	func TestMyAdapterConforms(t *testing.T) {
//	    channeltest.RunAdapterSuite(t, func() channel.Adapter {
//	        return mychannel.New("token", "agent-1")
//	    })
//	}
//
// The suite is intentionally network-free: it verifies the contract's
// shape and lifecycle safety (identity, status before start, graceful
// no-op sends, idempotent stop) — everything an adapter must guarantee
// WITHOUT a live platform connection. Live-connection behaviour belongs
// in the author's own integration tests.
package channeltest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/soulacy/soulacy/sdk/channel"
	"github.com/soulacy/soulacy/sdk/message"
)

// RunAdapterSuite runs the conformance checks against fresh adapters built
// by newAdapter. The factory is called once per subtest so checks never
// contaminate each other.
func RunAdapterSuite(t *testing.T, newAdapter func() channel.Adapter) {
	t.Helper()

	t.Run("ID_NonEmptyAndStable", func(t *testing.T) {
		a := newAdapter()
		id := a.ID()
		if id == "" {
			t.Fatal("conformance: ID() must return a non-empty identifier")
		}
		if a.ID() != id {
			t.Fatalf("conformance: ID() must be stable, got %q then %q", id, a.ID())
		}
	})

	t.Run("Name_NonEmpty", func(t *testing.T) {
		if newAdapter().Name() == "" {
			t.Fatal("conformance: Name() must return a human-readable name")
		}
	})

	t.Run("Status_SafeBeforeStart", func(t *testing.T) {
		a := newAdapter()
		defer failOnPanic(t, "Status() before Start()")
		_ = a.Status() // value unconstrained (webhook adapters may report ready); must not panic
	})

	t.Run("Send_EmptyMessage_NoPanic", func(t *testing.T) {
		a := newAdapter()
		defer failOnPanic(t, "Send() of an empty message")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		// No parts, no recipient: adapters must reject or no-op — never panic,
		// and never require a live connection to decide.
		_ = a.Send(ctx, message.Message{})
	})

	t.Run("Send_CancelledContext_FailsFast", func(t *testing.T) {
		// Story 19a: Send must honour the caller's context. With a
		// cancelled ctx and a non-empty message, the adapter must return
		// promptly with an error — never swallow the ctx into a fresh
		// Background() and fire the network call anyway.
		a := newAdapter()
		defer failOnPanic(t, "Send() with cancelled context")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		done := make(chan error, 1)
		go func() {
			done <- a.Send(ctx, message.Message{
				ThreadID: "1",
				Parts:    message.Text("conformance ctx probe"),
			})
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("conformance: Send(cancelled ctx) must return an error, got nil")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("conformance: Send(cancelled ctx) error must wrap context.Canceled (use %%w), got: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("conformance: Send(cancelled ctx) did not return within 5s — caller context ignored")
		}
	})

	t.Run("Stop_BeforeStart_NoPanic", func(t *testing.T) {
		a := newAdapter()
		defer failOnPanic(t, "Stop() before Start()")
		_ = a.Stop()
	})

	t.Run("Stop_Idempotent", func(t *testing.T) {
		a := newAdapter()
		defer failOnPanic(t, "second Stop()")
		_ = a.Stop()
		_ = a.Stop()
	})

	t.Run("Status_AfterStop_NotConnected", func(t *testing.T) {
		a := newAdapter()
		_ = a.Stop()
		if st := a.Status(); st.Connected && st.Detail == "" {
			t.Fatal("conformance: after Stop(), Status() should not report a bare connected state")
		}
	})
}

func failOnPanic(t *testing.T, what string) {
	t.Helper()
	if r := recover(); r != nil {
		t.Fatalf("conformance: %s panicked: %v", what, r)
	}
}
