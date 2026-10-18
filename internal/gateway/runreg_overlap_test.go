package gateway

import (
	"context"
	"testing"
)

func TestOverlappingSessionRunsRemainTrackedAndCancellable(t *testing.T) {
	r := newRunRegistry()
	first, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	second, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	doneFirst := r.Register("session", cancelFirst)
	doneSecond := r.Register("session", cancelSecond)
	if r.Active() != 2 {
		t.Fatal("overlapping request disappeared")
	}
	doneFirst()
	doneFirst() // cleanup is idempotent
	if r.Active() != 1 || !r.Cancel("session") {
		t.Fatal("finishing first request removed the second")
	}
	if second.Err() != context.Canceled || first.Err() != nil {
		t.Fatal("cancel affected the wrong request")
	}
	doneSecond()
	if r.Active() != 0 {
		t.Fatal("cancelled request leaked")
	}
}

func TestSessionCancelStopsBothActiveAndQueuedRequests(t *testing.T) {
	r := newRunRegistry()
	a, ca := context.WithCancel(context.Background())
	b, cb := context.WithCancel(context.Background())
	defer ca()
	defer cb()
	r.Register("s", ca)
	r.Register("s", cb)
	if !r.Cancel("s") || a.Err() == nil || b.Err() == nil || r.Active() != 0 {
		t.Fatal("session cancellation left work running")
	}
}
