package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSessionTurnCancellationAndRecursionDoNotStealActiveTurn(t *testing.T) {
	s := &Session{}
	ctx, release, err := s.acquireTurn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.acquireTurn(ctx); err == nil {
		t.Fatal("recursive turn must fail instead of deadlocking")
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := s.acquireTurn(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting turn: %v", err)
	}
	if len(s.turnGate) != 1 {
		t.Fatal("cancelled waiter released the active turn")
	}
	release()
	_, release, err = s.acquireTurn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestDifferentSessionsDoNotShareTurnGate(t *testing.T) {
	a, b := &Session{}, &Session{}
	ctx, releaseA, err := a.acquireTurn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()
	_, releaseB, err := b.acquireTurn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	releaseB()
}
