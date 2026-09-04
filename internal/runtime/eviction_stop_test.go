package runtime

import (
	"testing"
	"time"
)

// TestSessionEvictionStopsCleanly verifies the eviction sweep goroutine starts
// and can be stopped without hanging or panicking, and that Stop is idempotent
// (S2.2 — the goroutine must not leak on shutdown).
func TestSessionEvictionStopsCleanly(t *testing.T) {
	e := &Engine{}
	e.SetSessionEviction(time.Hour, 100)
	e.StartSessionEviction(time.Second)

	done := make(chan struct{})
	go func() {
		e.StopSessionEviction()
		e.StopSessionEviction() // idempotent — must not panic on double close
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StopSessionEviction hung — goroutine would leak on shutdown")
	}
}
