package queue

import (
	"errors"
	"sync"
	"testing"
)

func TestAckIsConcurrentAndIdempotent(t *testing.T) {
	calls := 0
	msg := NewMessage("a", nil, func() error { calls++; return nil })
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			if err := msg.Ack(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("acknowledged %d times", calls)
	}
}
func TestFailedAckCanBeRetried(t *testing.T) {
	calls := 0
	msg := NewMessage("a", nil, func() error {
		calls++
		if calls == 1 {
			return errors.New("offline")
		}
		return nil
	})
	if msg.Ack() == nil {
		t.Fatal("failure was hidden")
	}
	if err := msg.Ack(); err != nil {
		t.Fatal(err)
	}
	if err := msg.Ack(); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}
