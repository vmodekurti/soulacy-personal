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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Issuer signs access tokens (HS256 JWTs) and manages opaque refresh tokens.
//
// Access tokens are signed JWTs with a short TTL (default 15m).
// Refresh tokens are random opaque hex strings. Only their SHA-256 hashes are
// retained. They are single-use: each Refresh() call rotates the token and
// records the consumed hash so reuse revokes the complete token family.
type Issuer struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	store      *refreshStore
}

func newIssuer(secret string, accessTTL, refreshTTL time.Duration) (*Issuer, error) {
	return newIssuerWithStorePath(secret, accessTTL, refreshTTL, "")
}

func newIssuerWithStorePath(secret string, accessTTL, refreshTTL time.Duration, storePath string) (*Issuer, error) {
	if secret == "" {
		// Generate an ephemeral key. Not persistent across restarts.
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("generate ephemeral jwt secret: %w", err)
		}
		secret = hex.EncodeToString(b)
	}
	store, err := newRefreshStoreAt(storePath)
	if err != nil {
		return nil, fmt.Errorf("open refresh session store: %w", err)
	}
	return &Issuer{
		secret:     []byte(secret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
		store:      store,
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
	Subject       string
	Email         string
	Role          string
	PrincipalKind string

	OrganizationID string
	WorkspaceID    string
	MembershipID   string

	// AuthTime is when the human last proved who they are — a password, an
	// OIDC round trip, a key presented interactively — NOT when this token was
	// minted (MU-030 criterion 5).
	//
	// THE DISTINCTION IS THE WHOLE FEATURE. `iat` is the obvious field to
	// reach for and it is wrong here: an access token is silently rotated
	// every fifteen minutes for as long as the browser is open, so an
	// iat-freshness check is satisfied forever by a session nobody has touched
	// — which is precisely the hijacked session step-up exists to stop.
	// AuthTime is therefore carried UNCHANGED through every rotation, and only
	// an explicit re-authentication moves it.
	//
	// Zero means "this credential never involved an interactive
	// authentication" — a service account, or the deployment's static key.
	// That is an answer rather than a missing value, and it is why the
	// recent-auth check has to decide what to do about principals that cannot
	// be prompted rather than treating zero as "very stale".
	AuthTime time.Time
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
		// AuthTime is deliberately NOT taken from the resolver either, and it
		// is not re-asserted here: issueInFamilyAt below takes it as an
		// explicit argument from the consumed entry, which is one mechanism
		// rather than two. A resolver re-reads MEMBERSHIP and has no idea when
		// the human last authenticated, so whatever it returns is zero —
		// taking it would reset the step-up clock on every silent rotation,
		// which is the one thing this field must not do.
		//
		// An assignment here as well was the first version, and mutation
		// testing showed deleting it changed nothing: it was defensive
		// redundancy whose comment claimed to be the mechanism, which is worse
		// than absent because the real one then goes unexamined.
		if strings.TrimSpace(current.Email) == "" {
			current.Email = entry.identity.Email
		}
		if strings.TrimSpace(current.PrincipalKind) == "" {
			current.PrincipalKind = entry.identity.PrincipalKind
		}
		id = current
	}
	return iss.issueInFamilyAt(id, entry.familyID, entry.authTime)
}

func (iss *Issuer) issueInFamily(id TokenIdentity, familyID string) (accessToken, refreshToken string, expiresIn int, err error) {
	return iss.issueInFamilyAt(id, familyID, id.AuthTime)
}

// issueInFamilyAt is where auth_time survives rotation. It takes the value as
// an argument rather than reading id.AuthTime, so a caller that hands it a
// freshly resolved identity — which is every refresh — cannot lose it by
// omission. See MU-030 criterion 5.
func (iss *Issuer) issueInFamilyAt(id TokenIdentity, familyID string, authTime time.Time) (accessToken, refreshToken string, expiresIn int, err error) {
	id.AuthTime = authTime
	now := time.Now()
	cl := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID: randomHex(16), Subject: id.Subject,
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(iss.accessTTL)),
			Issuer: "soulacy",
		},
		Email: id.Email, Role: id.Role, Kind: "access", PrincipalKind: id.PrincipalKind,
		OrganizationID: id.OrganizationID, WorkspaceID: id.WorkspaceID, MembershipID: id.MembershipID,
	}
	if !id.AuthTime.IsZero() {
		cl.AuthTime = id.AuthTime.UTC().Unix()
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, cl)
	accessToken, err = tok.SignedString(iss.secret)
	if err != nil {
		return "", "", 0, fmt.Errorf("sign access token: %w", err)
	}
	refreshToken, err = iss.store.put(id, familyID, now.Add(iss.refreshTTL))
	if err != nil {
		return "", "", 0, fmt.Errorf("persist refresh token: %w", err)
	}
	return accessToken, refreshToken, int(iss.accessTTL.Seconds()), nil
}

// Reauthenticate mints a fresh pair for an identity that has just proved
// itself again, stamping AuthTime with now (MU-030 criterion 5).
//
// It starts a NEW family rather than continuing the old one. Step-up is
// meaningful only if the elevated authority is bound to the credential the
// human just proved: continuing the family would let a refresh token stolen
// before the step-up inherit the elevation on its next rotation, which is
// exactly the attacker step-up is meant to exclude.
func (iss *Issuer) Reauthenticate(id TokenIdentity) (accessToken, refreshToken string, expiresIn int, err error) {
	id.AuthTime = time.Now().UTC()
	return iss.issueInFamilyAt(id, "", id.AuthTime)
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
// refreshStore — opaque refresh token store with optional durable journal
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
	// authTime survives rotation. It lives on the ENTRY rather than being
	// re-derived from the identity, because RefreshAuthorized replaces the
	// identity wholesale with a freshly resolved one — and a resolver that
	// returns a zero AuthTime would otherwise silently reset the clock on
	// every refresh, turning the check back into an iat check.
	authTime time.Time
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
	path            string
	persistErr      error
}

func newRefreshStore() *refreshStore {
	s, err := newRefreshStoreAt("")
	if err != nil {
		panic(err)
	}
	return s
}

func newRefreshStoreAt(path string) (*refreshStore, error) {
	s := &refreshStore{
		tokens:          make(map[[32]byte]refreshEntry),
		consumed:        make(map[[32]byte]consumedEntry),
		revokedFamilies: make(map[string]time.Time),
		revokedAccess:   make(map[string]time.Time),
		quit:            make(chan struct{}),
		path:            strings.TrimSpace(path),
	}
	if s.path != "" {
		if err := s.load(); err != nil {
			return nil, err
		}
	}
	go s.sweepLoop()
	return s, nil
}

const refreshStoreVersion = 1

type refreshStoreJournal struct {
	Version         int                   `json:"version"`
	Tokens          []refreshTokenRecord  `json:"tokens,omitempty"`
	Consumed        []consumedTokenRecord `json:"consumed,omitempty"`
	RevokedFamilies []expiryRecord        `json:"revoked_families,omitempty"`
	RevokedAccess   []expiryRecord        `json:"revoked_access,omitempty"`
}

type refreshTokenRecord struct {
	Hash      string        `json:"hash"`
	Identity  TokenIdentity `json:"identity"`
	Subject   string        `json:"subject"`
	FamilyID  string        `json:"family_id"`
	ExpiresAt time.Time     `json:"expires_at"`
	AuthTime  time.Time     `json:"auth_time,omitempty"`
}

type consumedTokenRecord struct {
	Hash      string    `json:"hash"`
	FamilyID  string    `json:"family_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type expiryRecord struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
}

func decodeRefreshHash(raw string) ([32]byte, error) {
	var result [32]byte
	b, err := hex.DecodeString(raw)
	if err != nil || len(b) != len(result) {
		return result, errors.New("invalid refresh token hash")
	}
	copy(result[:], b)
	return result, nil
}

// load restores only non-expired hashes and revocations. The journal contains
// no bearer credential: a filesystem reader cannot turn a stored hash back
// into a usable refresh token.
func (s *refreshStore) load() error {
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("refresh session store must be a regular file")
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("secure refresh session store: %w", err)
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	var journal refreshStoreJournal
	if err := json.Unmarshal(b, &journal); err != nil {
		return fmt.Errorf("decode refresh session store: %w", err)
	}
	if journal.Version != refreshStoreVersion {
		return fmt.Errorf("unsupported refresh session store version %d", journal.Version)
	}
	now := time.Now()
	for _, record := range journal.Tokens {
		if !record.ExpiresAt.After(now) || record.FamilyID == "" {
			continue
		}
		h, err := decodeRefreshHash(record.Hash)
		if err != nil {
			return err
		}
		s.tokens[h] = refreshEntry{identity: record.Identity, subject: record.Subject, familyID: record.FamilyID, expiresAt: record.ExpiresAt, authTime: record.AuthTime}
	}
	for _, record := range journal.Consumed {
		if !record.ExpiresAt.After(now) || record.FamilyID == "" {
			continue
		}
		h, err := decodeRefreshHash(record.Hash)
		if err != nil {
			return err
		}
		s.consumed[h] = consumedEntry{familyID: record.FamilyID, expiresAt: record.ExpiresAt}
	}
	for _, record := range journal.RevokedFamilies {
		if record.ID != "" && record.ExpiresAt.After(now) {
			s.revokedFamilies[record.ID] = record.ExpiresAt
		}
	}
	for _, record := range journal.RevokedAccess {
		if record.ID != "" && record.ExpiresAt.After(now) {
			s.revokedAccess[record.ID] = record.ExpiresAt
		}
	}
	return nil
}

func (s *refreshStore) journalLocked() refreshStoreJournal {
	journal := refreshStoreJournal{Version: refreshStoreVersion}
	for hash, entry := range s.tokens {
		journal.Tokens = append(journal.Tokens, refreshTokenRecord{Hash: hex.EncodeToString(hash[:]), Identity: entry.identity, Subject: entry.subject, FamilyID: entry.familyID, ExpiresAt: entry.expiresAt, AuthTime: entry.authTime})
	}
	for hash, entry := range s.consumed {
		journal.Consumed = append(journal.Consumed, consumedTokenRecord{Hash: hex.EncodeToString(hash[:]), FamilyID: entry.familyID, ExpiresAt: entry.expiresAt})
	}
	for id, expiry := range s.revokedFamilies {
		journal.RevokedFamilies = append(journal.RevokedFamilies, expiryRecord{ID: id, ExpiresAt: expiry})
	}
	for id, expiry := range s.revokedAccess {
		journal.RevokedAccess = append(journal.RevokedAccess, expiryRecord{ID: id, ExpiresAt: expiry})
	}
	sort.Slice(journal.Tokens, func(i, j int) bool { return journal.Tokens[i].Hash < journal.Tokens[j].Hash })
	sort.Slice(journal.Consumed, func(i, j int) bool { return journal.Consumed[i].Hash < journal.Consumed[j].Hash })
	sort.Slice(journal.RevokedFamilies, func(i, j int) bool { return journal.RevokedFamilies[i].ID < journal.RevokedFamilies[j].ID })
	sort.Slice(journal.RevokedAccess, func(i, j int) bool { return journal.RevokedAccess[i].ID < journal.RevokedAccess[j].ID })
	return journal
}

// persistLocked atomically replaces a permission-restricted journal. It is
// called while s.mu is held so token rotation and its durable record are one
// ordered operation from the issuer's perspective.
func (s *refreshStore) persistLocked() error {
	if s.path == "" {
		return nil
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create refresh session directory: %w", err)
	}
	b, err := json.Marshal(s.journalLocked())
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".refresh-sessions-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	return os.Chmod(s.path, 0o600)
}

func (s *refreshStore) recordPersistLocked() {
	if s.persistErr != nil {
		return
	}
	if err := s.persistLocked(); err != nil {
		// A process that cannot durably record rotation/revocation must not
		// continue accepting credentials on a divergent in-memory view.
		s.persistErr = err
	}
}

// put stores a new refresh token and returns the opaque token string.
func (s *refreshStore) put(id TokenIdentity, familyID string, exp time.Time) (string, error) {
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
	if s.persistErr != nil {
		s.mu.Unlock()
		return "", s.persistErr
	}
	h := sha256.Sum256([]byte(tok))
	s.tokens[h] = refreshEntry{identity: id, subject: id.Subject, familyID: familyID, expiresAt: exp, authTime: id.AuthTime}
	if err := s.persistLocked(); err != nil {
		delete(s.tokens, h)
		s.persistErr = err
		s.mu.Unlock()
		return "", err
	}
	s.mu.Unlock()
	return tok, nil
}

// get looks up a refresh token and deletes it (single-use rotation).
// Returns ok=false if the token is unknown or expired.
func (s *refreshStore) consume(tok string) (refreshEntry, refreshStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persistErr != nil {
		return refreshEntry{}, refreshInvalid
	}
	h := sha256.Sum256([]byte(tok))
	if used, found := s.consumed[h]; found {
		s.revokeFamilyLocked(used.familyID, used.expiresAt)
		s.recordPersistLocked()
		return refreshEntry{}, refreshReused
	}
	e, found := s.tokens[h]
	if !found || time.Now().After(e.expiresAt) {
		delete(s.tokens, h)
		s.recordPersistLocked()
		return refreshEntry{}, refreshInvalid
	}
	if exp, revoked := s.revokedFamilies[e.familyID]; revoked && time.Now().Before(exp) {
		delete(s.tokens, h)
		s.recordPersistLocked()
		return refreshEntry{}, refreshInvalid
	}
	delete(s.tokens, h)
	s.consumed[h] = consumedEntry{familyID: e.familyID, expiresAt: e.expiresAt}
	s.recordPersistLocked()
	return e, refreshValid
}

func (s *refreshStore) revokeFamilyFor(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := sha256.Sum256([]byte(tok))
	if e, ok := s.tokens[h]; ok {
		s.revokeFamilyLocked(e.familyID, e.expiresAt)
		s.recordPersistLocked()
		return
	}
	if e, ok := s.consumed[h]; ok {
		s.revokeFamilyLocked(e.familyID, e.expiresAt)
		s.recordPersistLocked()
	}
}

func (s *refreshStore) revokeFamily(family string, exp time.Time) {
	s.mu.Lock()
	s.revokeFamilyLocked(family, exp)
	s.recordPersistLocked()
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
	s.recordPersistLocked()
	s.mu.Unlock()
}
func (s *refreshStore) accessRevoked(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.revokedAccess[id]
	return s.persistErr != nil || ok && time.Now().Before(exp)
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
			changed := false
			for k, e := range s.tokens {
				if now.After(e.expiresAt) {
					delete(s.tokens, k)
					changed = true
				}
			}
			for k, e := range s.consumed {
				if now.After(e.expiresAt) {
					delete(s.consumed, k)
					changed = true
				}
			}
			for k, exp := range s.revokedFamilies {
				if now.After(exp) {
					delete(s.revokedFamilies, k)
					changed = true
				}
			}
			for k, exp := range s.revokedAccess {
				if now.After(exp) {
					delete(s.revokedAccess, k)
					changed = true
				}
			}
			if changed {
				s.recordPersistLocked()
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
