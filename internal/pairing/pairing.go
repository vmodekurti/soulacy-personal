// Package pairing issues short-lived, single-use tokens that let a mobile device
// pair with a running Soulacy gateway by scanning a QR code shown on the
// desktop. The desktop asks for a token, renders it (plus the gateway URL) as a
// QR code; the phone scans it and redeems the token to prove it was physically
// present at the desktop. Tokens are single-use and expire quickly, so a leaked
// QR image is low-risk.
//
// This is the pairing foundation for the mobile companion. It is deliberately
// storage-light (in-memory with expiry) and dependency-free so it is easy to
// test and reason about.
package pairing

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
	"sync"
	"time"
)

// DefaultTTL is how long a freshly minted pairing token remains valid.
const DefaultTTL = 2 * time.Minute

// Token is a redeemable pairing code.
type Token struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Expired reports whether the token is past its expiry as of now.
func (t Token) Expired(now time.Time) bool { return !now.Before(t.ExpiresAt) }

type entry struct {
	expiresAt time.Time
}

// Store mints and redeems pairing tokens. Safe for concurrent use.
type Store struct {
	mu     sync.Mutex
	tokens map[string]entry
	now    func() time.Time // injectable clock for tests
}

// NewStore creates an empty pairing store.
func NewStore() *Store {
	return &Store{tokens: map[string]entry{}, now: time.Now}
}

// Create mints a new single-use token valid for ttl (DefaultTTL when ttl<=0).
func (s *Store) Create(ttl time.Duration) (Token, error) {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	code, err := newCode()
	if err != nil {
		return Token{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	exp := s.now().Add(ttl)
	s.tokens[code] = entry{expiresAt: exp}
	return Token{Code: code, ExpiresAt: exp}, nil
}

// Redeem consumes a token. It returns true only if the token exists and is not
// expired; the token is removed either way (single use / no replay).
func (s *Store) Redeem(code string) bool {
	code = normalize(code)
	if code == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tokens[code]
	if !ok {
		return false
	}
	delete(s.tokens, code)
	return s.now().Before(e.expiresAt)
}

// Active returns the number of unexpired tokens (primarily for tests/metrics).
func (s *Store) Active() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	return len(s.tokens)
}

func (s *Store) sweepLocked() {
	now := s.now()
	for code, e := range s.tokens {
		if !now.Before(e.expiresAt) {
			delete(s.tokens, code)
		}
	}
}

// newCode returns a URL-safe, human-transcribable pairing code.
func newCode() (string, error) {
	buf := make([]byte, 10) // 80 bits
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)
	return strings.ToUpper(enc), nil
}

func normalize(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}
