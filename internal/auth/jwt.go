package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
// Refresh tokens outlive a restart. They did not, and the effect was that
// every gateway restart silently logged the phone out: the access token kept
// working for its fifteen minutes, then the app refreshed, the store had been
// wiped, and the only way back was to sign in again. A restart is a routine
// operation — a deploy, a config change, a reboot — and it should not cost
// someone their session on a device they are not holding.
//
// They are kept as hashes, so the file on disk is not a bag of usable bearer
// credentials: a stolen file cannot be replayed, the same reason passwords are
// not stored either.
type Issuer struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	store      *refreshStore
}

func newIssuer(secret string, accessTTL, refreshTTL time.Duration, refreshPath string) (*Issuer, error) {
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
		store:      newRefreshStore(refreshPath),
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

	// path is where the store is kept across restarts. Empty means memory
	// only, which is what tests and an unconfigured workspace get.
	path string
}

// newRefreshStore opens the store, loading anything a previous run left.
//
// A file that cannot be read or parsed is not fatal: the worst case of
// starting empty is the sign-in this whole change exists to avoid, while
// refusing to start would take the gateway down over a cache.
func newRefreshStore(path string) *refreshStore {
	s := &refreshStore{
		tokens: make(map[string]refreshEntry),
		quit:   make(chan struct{}),
		path:   path,
	}
	s.load()
	go s.sweepLoop()
	return s
}

// hashToken is what goes on disk. The token itself never does.
func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

type persistedEntry struct {
	Subject   string    `json:"subject"`
	Email     string    `json:"email,omitempty"`
	Role      string    `json:"role,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *refreshStore) load() {
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var stored map[string]persistedEntry
	if err := json.Unmarshal(data, &stored); err != nil {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, e := range stored {
		if now.After(e.ExpiresAt) {
			continue // expired while the gateway was down
		}
		s.tokens[h] = refreshEntry{subject: e.Subject, email: e.Email, role: e.Role, expiresAt: e.ExpiresAt}
	}
}

// saveLocked writes the store out. The caller holds the mutex.
//
// Written to a temporary file and renamed, so a crash mid-write leaves the
// previous file rather than a truncated one — losing every session is a worse
// failure than losing the last one.
func (s *refreshStore) saveLocked() {
	if s.path == "" {
		return
	}
	stored := make(map[string]persistedEntry, len(s.tokens))
	for h, e := range s.tokens {
		stored[h] = persistedEntry{Subject: e.subject, Email: e.email, Role: e.role, ExpiresAt: e.expiresAt}
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return
	}
	tmp := s.path + ".tmp"
	// 0600: these are session credentials, even hashed.
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, s.path)
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
	s.tokens[hashToken(tok)] = refreshEntry{subject: subject, email: email, role: role, expiresAt: exp}
	s.saveLocked()
	s.mu.Unlock()
	return tok
}

// get looks up a refresh token and deletes it (single-use rotation).
// Returns ok=false if the token is unknown or expired.
func (s *refreshStore) get(tok string) (subject, email, role string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := hashToken(tok)
	e, found := s.tokens[h]
	if !found || time.Now().After(e.expiresAt) {
		if found {
			delete(s.tokens, h)
			s.saveLocked()
		}
		return "", "", "", false
	}
	delete(s.tokens, h) // rotate
	s.saveLocked()
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
			removed := false
			for k, e := range s.tokens {
				if now.After(e.expiresAt) {
					delete(s.tokens, k)
					removed = true
				}
			}
			if removed {
				s.saveLocked()
			}
			s.mu.Unlock()
		}
	}
}
