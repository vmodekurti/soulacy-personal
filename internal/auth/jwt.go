package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Issuer signs access tokens (HS256 JWTs) and manages opaque refresh tokens.
//
// Access tokens are signed JWTs with a short TTL (default 15m).
// Refresh tokens are random opaque hex strings stored in an in-memory map
// with a longer TTL (default 7d). They are single-use: each Refresh() call
// rotates the token and issues a new one.
//
// NOTE: The refresh store is in-memory only. A gateway restart invalidates all
// outstanding refresh tokens (users need to re-authenticate via /auth/token).
// For persistent refresh tokens across restarts, store them in Postgres —
// that's a Task #31/32 concern.
type Issuer struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	store      *refreshStore
}

func newIssuer(secret string, accessTTL, refreshTTL time.Duration) (*Issuer, error) {
	if secret == "" {
		// Generate an ephemeral key. Not persistent across restarts.
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("generate ephemeral jwt secret: %w", err)
		}
		secret = hex.EncodeToString(b)
	}
	return &Issuer{
		secret:     []byte(secret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
		store:      newRefreshStore(),
	}, nil
}

// Issue creates a new access + refresh token pair for the given identity.
// subject is the user identifier (e.g. "admin" or an OIDC sub).
// Returns the signed access token, opaque refresh token, and access TTL in seconds.
func (iss *Issuer) Issue(subject, email, role string) (accessToken, refreshToken string, expiresIn int, err error) {
	now := time.Now()
	cl := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(iss.accessTTL)),
			Issuer:    "soulacy",
		},
		Email: email,
		Role:  role,
		Kind:  "access",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, cl)
	accessToken, err = tok.SignedString(iss.secret)
	if err != nil {
		return "", "", 0, fmt.Errorf("sign access token: %w", err)
	}
	refreshToken = iss.store.put(subject, email, role, now.Add(iss.refreshTTL))
	expiresIn = int(iss.accessTTL.Seconds())
	return
}

// VerifyAccess validates an access token and returns its claims.
// Returns an error if the token is expired, malformed, or is a refresh token.
func (iss *Issuer) VerifyAccess(tokenStr string) (*Claims, error) {
	cl := &Claims{}
	tok, err := jwt.ParseWithClaims(tokenStr, cl, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %q", t.Header["alg"])
		}
		return iss.secret, nil
	}, jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	if !tok.Valid {
		return nil, errors.New("invalid token")
	}
	if cl.Kind != "access" {
		return nil, errors.New("not an access token")
	}
	return cl, nil
}

// Refresh exchanges an opaque refresh token for a new access + refresh pair.
// The old refresh token is invalidated on use (rotation).
func (iss *Issuer) Refresh(refreshToken string) (newAccess, newRefresh string, expiresIn int, err error) {
	subject, email, role, ok := iss.store.get(refreshToken)
	if !ok {
		return "", "", 0, errors.New("invalid or expired refresh token")
	}
	return iss.Issue(subject, email, role)
}

// Close shuts down the background sweep goroutine. Idempotent.
func (iss *Issuer) Close() {
	iss.store.close()
}

// ---------------------------------------------------------------------------
// refreshStore — in-memory opaque refresh token store
// ---------------------------------------------------------------------------

type refreshEntry struct {
	subject, email, role string
	expiresAt            time.Time
}

type refreshStore struct {
	mu     sync.Mutex
	tokens map[string]refreshEntry
	quit   chan struct{}
	once   sync.Once
}

func newRefreshStore() *refreshStore {
	s := &refreshStore{
		tokens: make(map[string]refreshEntry),
		quit:   make(chan struct{}),
	}
	go s.sweepLoop()
	return s
}

// put stores a new refresh token and returns the opaque token string.
func (s *refreshStore) put(subject, email, role string, exp time.Time) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Unreachable on any supported OS — but the failure mode if it ever were
		// reachable is a refresh token built from 32 zero bytes, i.e. the same
		// token for every session. That must never be papered over with a nolint,
		// which is what used to be here (and with malformed syntax at that: an
		// em-dash is not a comment separator, so golangci read the explanation as
		// a list of linter names).
		panic("auth: crypto/rand unavailable, refusing to mint a predictable refresh token: " + err.Error())
	}
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	s.tokens[tok] = refreshEntry{subject: subject, email: email, role: role, expiresAt: exp}
	s.mu.Unlock()
	return tok
}

// get looks up a refresh token and deletes it (single-use rotation).
// Returns ok=false if the token is unknown or expired.
func (s *refreshStore) get(tok string) (subject, email, role string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, found := s.tokens[tok]
	if !found || time.Now().After(e.expiresAt) {
		delete(s.tokens, tok)
		return "", "", "", false
	}
	delete(s.tokens, tok) // rotate
	return e.subject, e.email, e.role, true
}

// close stops the sweep goroutine.
func (s *refreshStore) close() {
	s.once.Do(func() { close(s.quit) })
}

// sweepLoop removes expired refresh tokens every 15 minutes.
func (s *refreshStore) sweepLoop() {
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.quit:
			return
		case <-t.C:
			now := time.Now()
			s.mu.Lock()
			for k, e := range s.tokens {
				if now.After(e.expiresAt) {
					delete(s.tokens, k)
				}
			}
			s.mu.Unlock()
		}
	}
}
