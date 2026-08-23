package queue

import (
	"errors"
	"sync/atomic"
	"testing"
)

func TestMessageAckIsIdempotentAndKeepsTheBackendResult(t *testing.T) {
	want := errors.New("ack failed")
	var calls atomic.Int64
	msg := NewMessage("subject", nil, func() error {
		calls.Add(1)
		return want
	})
	if err := msg.Ack(); !errors.Is(err, want) {
		t.Fatalf("first Ack error = %v, want %v", err, want)
	}
	if err := msg.Ack(); !errors.Is(err, want) {
		t.Fatalf("second Ack error = %v, want preserved result", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("backend ack calls = %d, want 1", calls.Load())
	}
}
