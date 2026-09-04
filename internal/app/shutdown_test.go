package app

import (
	"errors"
	"strings"
	"testing"
)

// TestCloserStackLIFO verifies that closers run in reverse registration order.
func TestCloserStackLIFO(t *testing.T) {
	s := newCloserStack(nil)
	var order []string
	for _, name := range []string{"a", "b", "c", "d"} {
		name := name
		s.push(name, func() error {
			order = append(order, name)
			return nil
		})
	}
	if s.len() != 4 {
		t.Fatalf("len = %d, want 4", s.len())
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	got := strings.Join(order, ",")
	const want = "d,c,b,a"
	if got != want {
		t.Fatalf("close order = %q, want %q (LIFO)", got, want)
	}
}

// TestCloserStackAggregatesErrors verifies that every closer runs even when
// some fail, and that all failures are aggregated into the returned error.
func TestCloserStackAggregatesErrors(t *testing.T) {
	s := newCloserStack(nil)
	errFirst := errors.New("boom-first")  // registered first → runs last
	errThird := errors.New("boom-third")  // registered third → runs first

	ran := 0
	s.push("first", func() error { ran++; return errFirst })
	s.push("second", func() error { ran++; return nil })
	s.push("third", func() error { ran++; return errThird })

	err := s.Close()
	if ran != 3 {
		t.Fatalf("ran %d closers, want all 3 to run despite failures", ran)
	}
	if err == nil {
		t.Fatal("expected aggregated error, got nil")
	}
	// Both failing closers must be reachable via errors.Is on the join.
	if !errors.Is(err, errFirst) {
		t.Errorf("aggregated error missing first failure: %v", err)
	}
	if !errors.Is(err, errThird) {
		t.Errorf("aggregated error missing third failure: %v", err)
	}
	// The step name should be attached for attributability.
	if !strings.Contains(err.Error(), "third:") || !strings.Contains(err.Error(), "first:") {
		t.Errorf("aggregated error should be name-tagged, got: %v", err)
	}
}

// TestCloserStackNilFnIgnored verifies nil closers are skipped at registration.
func TestCloserStackNilFnIgnored(t *testing.T) {
	s := newCloserStack(nil)
	s.push("nil-step", nil)
	s.pushClose("nil-closer", nil)
	if s.len() != 0 {
		t.Fatalf("len = %d, want 0 (nil closers must be ignored)", s.len())
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close on empty stack returned error: %v", err)
	}
}

// TestCloserStackEmpty verifies Close on an empty stack is a no-op.
func TestCloserStackEmpty(t *testing.T) {
	s := newCloserStack(nil)
	if err := s.Close(); err != nil {
		t.Fatalf("Close on empty stack returned error: %v", err)
	}
}
