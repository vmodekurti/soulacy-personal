package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
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

// TokenIdentity is everything a token asserts about who is calling.
//
// The tenancy fields are grouped into a struct rather than passed as three
// more positional strings because the failure mode of forgetting them is
// silent: an access token with no WorkspaceID authenticates fine and then
// resolves to the personal workspace everywhere downstream. In a multi-user
// deployment that means a signed-in member of ws_a acting with personal's
// authority — the token is valid, the request succeeds, and it is the wrong
// tenant. A zero-valued TokenIdentity is still personal, but now it is written
// down at the call site instead of being the shape of an omission.
type TokenIdentity struct {
	Subject string
	Email   string
	Role    string

	OrganizationID string
	WorkspaceID    string
	MembershipID   string
}

// IssueFor creates a new access + refresh token pair for one identity.
// Returns the signed access token, opaque refresh token, and access TTL in
// seconds.
//
// There is deliberately no Issue(subject, email, role) convenience: it was the
// only constructor for years, and every token it minted carried no tenant.
func (iss *Issuer) IssueFor(id TokenIdentity) (accessToken, refreshToken string, expiresIn int, err error) {
	return iss.issueInFamily(id, "")
}

// VerifyAccess validates an access token and returns its claims.
// Returns an error if the token is expired, malformed, or is a refresh token.
func (iss *Issuer) VerifyAccess(tokenStr string) (*Claims, error) {
	cl := &Claims{}
	tok, err := jwt.ParseWithClaims(tokenStr, cl, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
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
	if iss.store.accessRevoked(cl.ID) {
		return nil, errors.New("access token revoked")
	}
	return cl, nil
}

// Refresh exchanges an opaque refresh token for a new access + refresh pair.
// The old refresh token is invalidated on use (rotation).
func (iss *Issuer) Refresh(refreshToken string) (newAccess, newRefresh string, expiresIn int, err error) {
	return iss.RefreshAuthorized(refreshToken, nil)
}

// Reauthorizer re-reads a subject's live identity during a refresh. It returns
// the tenancy the *new* token should carry, or false to deny the refresh.
//
// Re-reading matters because a refresh family lives for days. A membership
// moved to a different workspace, or narrowed to a lesser role, would otherwise
// keep being replayed from the token minted when the member first signed in —
// the credential would outlive the authority it was granted under.
type Reauthorizer func(subject string) (TokenIdentity, bool)

// RefreshAuthorized consumes a token before consulting the live identity
// authorizer. A denied refresh revokes the complete family, preventing a
// suspended member from retrying with an older rotated token.
func (iss *Issuer) RefreshAuthorized(refreshToken string, reauthorize Reauthorizer) (newAccess, newRefresh string, expiresIn int, err error) {
	entry, status := iss.store.consume(refreshToken)
	if status != refreshValid {
		return "", "", 0, errors.New("invalid or expired refresh token")
	}
	id := entry.identity
	if reauthorize != nil {
		current, ok := reauthorize(entry.subject)
		if !ok {
			iss.store.revokeFamily(entry.familyID, entry.expiresAt)
			return "", "", 0, errors.New("invalid or expired refresh token")
		}
		// The subject is the one thing the caller does not get to change: it
		// identifies the family, and letting a resolver swap it would turn a
		// refresh into an impersonation primitive.
		current.Subject = entry.subject
		if strings.TrimSpace(current.Email) == "" {
			current.Email = entry.identity.Email
		}
		id = current
	}
	return iss.issueInFamily(id, entry.familyID)
}

func (iss *Issuer) issueInFamily(id TokenIdentity, familyID string) (accessToken, refreshToken string, expiresIn int, err error) {
	now := time.Now()
	cl := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID: randomHex(16), Subject: id.Subject,
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(iss.accessTTL)),
			Issuer: "soulacy",
		},
		Email: id.Email, Role: id.Role, Kind: "access",
		OrganizationID: id.OrganizationID, WorkspaceID: id.WorkspaceID, MembershipID: id.MembershipID,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, cl)
	accessToken, err = tok.SignedString(iss.secret)
	if err != nil {
		return "", "", 0, fmt.Errorf("sign access token: %w", err)
	}
	refreshToken = iss.store.put(id, familyID, now.Add(iss.refreshTTL))
	return accessToken, refreshToken, int(iss.accessTTL.Seconds()), nil
}

// Revoke invalidates the current access token and the complete refresh-token
// family. A stolen, previously rotated refresh token therefore cannot be used
// to keep a logged-out session alive.
func (iss *Issuer) Revoke(accessToken, refreshToken string) {
	if refreshToken != "" {
		iss.store.revokeFamilyFor(refreshToken)
	}
	if accessToken == "" {
		return
	}
	cl := &Claims{}
	if tok, err := jwt.ParseWithClaims(accessToken, cl, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return iss.secret, nil
	}, jwt.WithExpirationRequired()); err == nil && tok.Valid && cl.ID != "" && cl.ExpiresAt != nil {
		iss.store.revokeAccess(cl.ID, cl.ExpiresAt.Time)
	}
}

// Close shuts down the background sweep goroutine. Idempotent.
func (iss *Issuer) Close() {
	iss.store.close()
}

// ---------------------------------------------------------------------------
// refreshStore — in-memory opaque refresh token store
// ---------------------------------------------------------------------------

type refreshEntry struct {
	// identity is what the next access token in this family will assert,
	// including its tenancy. Stored rather than re-derived so a deployment with
	// no live membership source still refreshes into the same workspace it
	// signed in to; a deployment that has one overrides it on every refresh.
	identity  TokenIdentity
	subject   string
	familyID  string
	expiresAt time.Time
}

type consumedEntry struct {
	familyID  string
	expiresAt time.Time
}
type refreshStatus uint8

const (
	refreshInvalid refreshStatus = iota
	refreshValid
	refreshReused
)

type refreshStore struct {
	mu              sync.Mutex
	tokens          map[[32]byte]refreshEntry
	consumed        map[[32]byte]consumedEntry
	revokedFamilies map[string]time.Time
	revokedAccess   map[string]time.Time
	quit            chan struct{}
	once            sync.Once
}

func newRefreshStore() *refreshStore {
	s := &refreshStore{
		tokens:          make(map[[32]byte]refreshEntry),
		consumed:        make(map[[32]byte]consumedEntry),
		revokedFamilies: make(map[string]time.Time),
		revokedAccess:   make(map[string]time.Time),
		quit:            make(chan struct{}),
	}
	go s.sweepLoop()
	return s
}

// put stores a new refresh token and returns the opaque token string.
func (s *refreshStore) put(id TokenIdentity, familyID string, exp time.Time) string {
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
	if familyID == "" {
		familyID = randomHex(16)
	}
	s.mu.Lock()
	s.tokens[sha256.Sum256([]byte(tok))] = refreshEntry{identity: id, subject: id.Subject, familyID: familyID, expiresAt: exp}
	s.mu.Unlock()
	return tok
}

// get looks up a refresh token and deletes it (single-use rotation).
// Returns ok=false if the token is unknown or expired.
func (s *refreshStore) consume(tok string) (refreshEntry, refreshStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := sha256.Sum256([]byte(tok))
	if used, found := s.consumed[h]; found {
		s.revokeFamilyLocked(used.familyID, used.expiresAt)
		return refreshEntry{}, refreshReused
	}
	e, found := s.tokens[h]
	if !found || time.Now().After(e.expiresAt) {
		delete(s.tokens, h)
		return refreshEntry{}, refreshInvalid
	}
	if exp, revoked := s.revokedFamilies[e.familyID]; revoked && time.Now().Before(exp) {
		delete(s.tokens, h)
		return refreshEntry{}, refreshInvalid
	}
	delete(s.tokens, h)
	s.consumed[h] = consumedEntry{familyID: e.familyID, expiresAt: e.expiresAt}
	return e, refreshValid
}

func (s *refreshStore) revokeFamilyFor(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := sha256.Sum256([]byte(tok))
	if e, ok := s.tokens[h]; ok {
		s.revokeFamilyLocked(e.familyID, e.expiresAt)
		return
	}
	if e, ok := s.consumed[h]; ok {
		s.revokeFamilyLocked(e.familyID, e.expiresAt)
	}
}

func (s *refreshStore) revokeFamily(family string, exp time.Time) {
	s.mu.Lock()
	s.revokeFamilyLocked(family, exp)
	s.mu.Unlock()
}

func (s *refreshStore) revokeFamilyLocked(family string, exp time.Time) {
	s.revokedFamilies[family] = exp
	for h, e := range s.tokens {
		if e.familyID == family {
			delete(s.tokens, h)
		}
	}
}

func (s *refreshStore) revokeAccess(id string, exp time.Time) {
	s.mu.Lock()
	s.revokedAccess[id] = exp
	s.mu.Unlock()
}
func (s *refreshStore) accessRevoked(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.revokedAccess[id]
	return ok && time.Now().Before(exp)
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
			for k, e := range s.consumed {
				if now.After(e.expiresAt) {
					delete(s.consumed, k)
				}
			}
			for k, exp := range s.revokedFamilies {
				if now.After(exp) {
					delete(s.revokedFamilies, k)
				}
			}
			for k, exp := range s.revokedAccess {
				if now.After(exp) {
					delete(s.revokedAccess, k)
				}
			}
			s.mu.Unlock()
		}
	}
}

func randomHex(bytes int) string {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
