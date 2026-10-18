package runtime

import (
	"context"
	"fmt"
)

type sessionTurnKey struct{ session *Session }

// acquireTurn serializes one conversation while allowing other conversations
// to proceed. Callers pin the session with inUse while holding or awaiting it.
func (s *Session) acquireTurn(ctx context.Context) (context.Context, func(), error) {
	key := sessionTurnKey{s}
	if ctx.Value(key) != nil {
		return ctx, nil, fmt.Errorf("recursive turn in the same agent conversation is not supported")
	}
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	s.mu.Lock()
	if s.turnGate == nil {
		s.turnGate = make(chan struct{}, 1)
	}
	gate := s.turnGate
	s.mu.Unlock()
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return ctx, nil, err
		}
		return context.WithValue(ctx, key, true), func() { <-gate }, nil
	case <-ctx.Done():
		return ctx, nil, ctx.Err()
	}
}
