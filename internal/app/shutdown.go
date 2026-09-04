package app

// shutdown.go — the ordered shutdown stack used by App.Run (Story ARCH-4).
//
// Run constructs subsystems top-to-bottom; each subsystem that owns a resource
// registers a closer on the stack as it comes up. When Run returns, the stack
// is drained in LIFO order — the reverse of construction — which mirrors the
// original deferred-Close() semantics exactly while making the ordering
// explicit, testable, and inspectable.

import (
	"errors"

	"go.uber.org/zap"
)

// closer is a single named shutdown step. The name is purely diagnostic and is
// attached to any error the step returns and to the per-closer failure log.
type closer struct {
	name string
	fn   func() error
}

// closerStack is a last-in-first-out stack of shutdown steps. The zero value is
// ready to use. It is NOT safe for concurrent registration; Run registers all
// closers from a single goroutine before draining, which matches how Go's
// own deferred-call stack behaves.
type closerStack struct {
	log     *zap.Logger
	closers []closer
}

// newCloserStack returns an empty stack. log may be nil (used only to surface
// individual closer failures during Close).
func newCloserStack(log *zap.Logger) *closerStack {
	return &closerStack{log: log}
}

// push registers fn to run during Close. Closers run in reverse registration
// order (LIFO). A nil fn is ignored so call sites can register conditionally
// without a guard.
func (s *closerStack) push(name string, fn func() error) {
	if fn == nil {
		return
	}
	s.closers = append(s.closers, closer{name: name, fn: fn})
}

// pushClose adapts the common io.Closer-shaped `Close() error` method to a
// named closer.
func (s *closerStack) pushClose(name string, c interface{ Close() error }) {
	if c == nil {
		return
	}
	s.push(name, c.Close)
}

// len reports how many closers are currently registered.
func (s *closerStack) len() int { return len(s.closers) }

// Close runs every registered closer in LIFO order (reverse of registration),
// aggregating their errors. Every closer always runs even if an earlier one
// fails; the returned error joins all failures (nil when none fail). Each
// failure is also logged individually when a logger is present.
func (s *closerStack) Close() error {
	var errs []error
	for i := len(s.closers) - 1; i >= 0; i-- {
		c := s.closers[i]
		if err := c.fn(); err != nil {
			if s.log != nil {
				s.log.Warn("shutdown step failed", zap.String("step", c.name), zap.Error(err))
			}
			errs = append(errs, &namedError{name: c.name, err: err})
		}
	}
	return errors.Join(errs...)
}

// namedError tags a closer error with the step name so aggregated shutdown
// failures stay attributable.
type namedError struct {
	name string
	err  error
}

func (e *namedError) Error() string { return e.name + ": " + e.err.Error() }
func (e *namedError) Unwrap() error { return e.err }
