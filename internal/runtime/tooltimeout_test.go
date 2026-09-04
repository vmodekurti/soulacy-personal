package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestToolTimeoutError(t *testing.T) {
	const name = "mcp__notebooklm__research_status"
	d := 120 * time.Second

	t.Run("nil error passes through", func(t *testing.T) {
		if err := toolTimeoutError(name, d, nil, nil); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
	})

	t.Run("our tool timeout is enriched and actionable", func(t *testing.T) {
		err := toolTimeoutError(name, d, nil, context.DeadlineExceeded)
		if err == nil {
			t.Fatal("expected an error")
		}
		msg := err.Error()
		if !strings.Contains(msg, name) || !strings.Contains(msg, "tool_timeout") {
			t.Errorf("message should name the tool and the clock: %q", msg)
		}
		if !strings.Contains(msg, d.String()) {
			t.Errorf("message should state the timeout value %s: %q", d, msg)
		}
		// The underlying deadline error is preserved for errors.Is consumers.
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("wrapped error should still match context.DeadlineExceeded")
		}
	})

	t.Run("run cancel/timeout is NOT misattributed to the tool", func(t *testing.T) {
		// Outer context already done → the run_timeout/cancel is the cause, so the
		// deadline error passes through unchanged (no tool_timeout advice).
		err := toolTimeoutError(name, d, context.Canceled, context.DeadlineExceeded)
		if strings.Contains(err.Error(), "tool_timeout") {
			t.Errorf("should not blame the tool when the run context is done: %q", err.Error())
		}
	})

	t.Run("non-deadline errors pass through unchanged", func(t *testing.T) {
		orig := errors.New("mcp: connection refused")
		err := toolTimeoutError(name, d, nil, orig)
		if err != orig {
			t.Errorf("non-deadline error should pass through unchanged, got %v", err)
		}
	})
}
