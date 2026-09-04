package pairing

import (
	"testing"
	"time"
)

func TestCreateAndRedeem_Single(t *testing.T) {
	s := NewStore()
	tok, err := s.Create(time.Minute)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tok.Code == "" {
		t.Fatalf("empty code")
	}
	if !s.Redeem(tok.Code) {
		t.Fatalf("first redeem should succeed")
	}
	if s.Redeem(tok.Code) {
		t.Fatalf("second redeem must fail (single use)")
	}
}

func TestRedeem_CaseInsensitiveAndTrimmed(t *testing.T) {
	s := NewStore()
	tok, _ := s.Create(time.Minute)
	if !s.Redeem("  " + lower(tok.Code) + "  ") {
		t.Fatalf("redeem should normalize case/whitespace")
	}
}

func TestRedeem_Expired(t *testing.T) {
	s := NewStore()
	base := time.Now()
	s.now = func() time.Time { return base }
	tok, _ := s.Create(time.Minute)
	// advance past expiry
	s.now = func() time.Time { return base.Add(2 * time.Minute) }
	if s.Redeem(tok.Code) {
		t.Fatalf("expired token must not redeem")
	}
}

func TestRedeem_Unknown(t *testing.T) {
	s := NewStore()
	if s.Redeem("NOPE") {
		t.Fatalf("unknown code must not redeem")
	}
}

func TestActive_SweepsExpired(t *testing.T) {
	s := NewStore()
	base := time.Now()
	s.now = func() time.Time { return base }
	_, _ = s.Create(time.Minute)
	_, _ = s.Create(time.Minute)
	if s.Active() != 2 {
		t.Fatalf("active = %d, want 2", s.Active())
	}
	s.now = func() time.Time { return base.Add(3 * time.Minute) }
	if s.Active() != 0 {
		t.Fatalf("active after expiry = %d, want 0", s.Active())
	}
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		}
	}
	return string(b)
}
