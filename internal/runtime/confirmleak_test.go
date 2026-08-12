package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// Resolve was the only place a pending approval was deleted. A run that hit its
// timeout, or a user who closed the tab, therefore left the entry in the broker
// forever — holding the tool-call arguments, and still shown by
// GET /api/v1/approvals as if someone could still act on it.
func TestConfirm_CancelledRunDoesNotLeaveAPendingApproval(t *testing.T) {
	e := newMinimalEngine(t)

	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithConfirmSender(ctx, func(req ConfirmRequest) <-chan bool {
		// Register exactly as the gateway's SSE handler does, then never answer.
		return e.Broker().RegisterRequest(req, "a", "s")
	})

	def := &agent.Definition{ID: "a", ConfirmTools: []string{"*"}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.maybeConfirm(ctx, def, message.ToolCall{Name: "shell_exec", Arguments: map[string]any{"cmd": "ls"}})
	}()

	// Wait until the approval is actually pending, then abandon the run.
	deadline := time.After(2 * time.Second)
	for len(e.Broker().List()) == 0 {
		select {
		case <-deadline:
			t.Fatal("the confirmation was never registered; this test would prove nothing")
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("maybeConfirm did not return after the context was cancelled")
	}

	if got := e.Broker().List(); len(got) != 0 {
		t.Fatalf("the abandoned approval is still pending after the run was cancelled: %+v", got)
	}
}
