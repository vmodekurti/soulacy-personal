package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
)

const oidcFlowTTL = 10 * time.Minute

type oidcFlow struct {
	redirectURI string
	verifier    string
	nonce       string
	client      string
	expiresAt   time.Time
}

type oidcFlowStore struct {
	mu      sync.Mutex
	flows   map[string]oidcFlow
	devices map[string]oidcDevice
	quit    chan struct{}
	once    sync.Once
}

type oidcDevice struct {
	code                string
	expiresAt, nextPoll time.Time
	interval            time.Duration
}

func newOIDCFlowStore() *oidcFlowStore {
	s := &oidcFlowStore{flows: map[string]oidcFlow{}, devices: map[string]oidcDevice{}, quit: make(chan struct{})}
	go s.sweep()
	return s
}

func (s *oidcFlowStore) putDevice(code string, expires time.Time, interval time.Duration) string {
	handle := randomHex(32)
	s.mu.Lock()
	s.devices[handle] = oidcDevice{code: code, expiresAt: expires, nextPoll: time.Now(), interval: interval}
	s.mu.Unlock()
	return handle
}
func (s *oidcFlowStore) device(handle string) (oidcDevice, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[handle]
	if !ok || time.Now().After(d.expiresAt) || time.Now().Before(d.nextPoll) {
		return oidcDevice{}, false
	}
	d.nextPoll = time.Now().Add(d.interval)
	s.devices[handle] = d
	return d, true
}
func (s *oidcFlowStore) deleteDevice(handle string) {
	s.mu.Lock()
	delete(s.devices, handle)
	s.mu.Unlock()
}

func (s *oidcFlowStore) create(redirectURI, client string) (state string, flow oidcFlow) {
	state = randomHex(32)
	flow = oidcFlow{redirectURI: redirectURI, verifier: base64.RawURLEncoding.EncodeToString(randomBytes(48)), nonce: randomHex(32), client: client, expiresAt: time.Now().Add(oidcFlowTTL)}
	s.mu.Lock()
	s.flows[state] = flow
	s.mu.Unlock()
	return state, flow
}

func (s *oidcFlowStore) consume(state, redirectURI string) (oidcFlow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	flow, ok := s.flows[state]
	delete(s.flows, state) // state is one-shot, including failed attempts
	if !ok || time.Now().After(flow.expiresAt) || !secretEqual(flow.redirectURI, redirectURI) {
		return oidcFlow{}, false
	}
	return flow, true
}

func (s *oidcFlowStore) close() { s.once.Do(func() { close(s.quit) }) }
func (s *oidcFlowStore) sweep() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.quit:
			return
		case now := <-t.C:
			s.mu.Lock()
			for key, flow := range s.flows {
				if now.After(flow.expiresAt) {
					delete(s.flows, key)
				}
			}
			for key, device := range s.devices {
				if now.After(device.expiresAt) {
					delete(s.devices, key)
				}
			}
			s.mu.Unlock()
		}
	}
}

type oidcStartRequest struct {
	RedirectURI string `json:"redirect_uri"`
	Client      string `json:"client"`
}
type oidcCompleteRequest struct {
	State       string `json:"state"`
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
}

// HandleOIDCConfig exposes only non-secret login metadata needed by clients.
func (e *Engine) HandleOIDCConfig(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"enabled": e.oidc != nil && e.issuer != nil, "mode": e.cfg.Mode, "device_flow": e.oidc != nil && e.oidc.discovery.DeviceAuthorizationEndpoint != ""})
}

// HandleOIDCStart begins an Authorization Code + PKCE flow. CLI redirect URIs
// are restricted to loopback addresses; GUI uses the single operator-registered
// callback and cannot supply an arbitrary redirect.
func (e *Engine) HandleOIDCStart(c *fiber.Ctx) error {
	if e.oidc == nil || e.issuer == nil || e.flows == nil {
		return authUnavailable(c)
	}
	req := oidcStartRequest{RedirectURI: c.Query("redirect_uri"), Client: c.Query("client")}
	if len(c.Body()) > 0 && c.Method() == fiber.MethodPost {
		if err := c.BodyParser(&req); err != nil {
			return authFailed(c)
		}
	}
	req.Client = strings.ToLower(strings.TrimSpace(req.Client))
	if req.Client == "" {
		req.Client = "gui"
	}
	if req.Client == "gui" {
		req.RedirectURI = e.cfg.OIDCRedirectURL
		if req.RedirectURI == "" {
			return authUnavailable(c)
		}
	} else if req.Client != "cli" || !safeLoopbackRedirect(req.RedirectURI) {
		return authFailed(c)
	}
	state, flow := e.flows.create(req.RedirectURI, req.Client)
	challenge := sha256.Sum256([]byte(flow.verifier))
	params := url.Values{
		"response_type": {"code"}, "client_id": {e.cfg.OIDCClientID}, "redirect_uri": {req.RedirectURI},
		"scope": {strings.Join(e.cfg.OIDCScopes, " ")}, "state": {state}, "nonce": {flow.nonce},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
	}
	authURL := e.oidc.discovery.AuthorizationEndpoint + "?" + params.Encode()
	if c.QueryBool("navigate") {
		return c.Redirect(authURL, fiber.StatusFound)
	}
	return c.JSON(fiber.Map{"authorization_url": authURL, "state": state, "expires_in": int(oidcFlowTTL.Seconds())})
}

// HandleOIDCCallback completes the GUI flow and stores tokens only in secure,
// HttpOnly same-origin cookies. Tokens never appear in redirect URLs/history.
func (e *Engine) HandleOIDCCallback(c *fiber.Ctx) error {
	access, refresh, expires, err := e.completeOIDC(c.Context(), oidcCompleteRequest{State: c.Query("state"), Code: c.Query("code"), RedirectURI: e.cfg.OIDCRedirectURL})
	if err != nil {
		return authFailed(c)
	}
	e.setAuthCookies(c, access, refresh, expires)
	return c.Redirect("/#auth=success", fiber.StatusFound)
}

// HandleOIDCComplete completes a CLI loopback flow. The authorization code is
// accepted only with its original one-time state and exact loopback redirect.
func (e *Engine) HandleOIDCComplete(c *fiber.Ctx) error {
	var req oidcCompleteRequest
	if err := c.BodyParser(&req); err != nil || !safeLoopbackRedirect(req.RedirectURI) {
		return authFailed(c)
	}
	access, refresh, expires, err := e.completeOIDC(c.Context(), req)
	if err != nil {
		return authFailed(c)
	}
	return c.JSON(fiber.Map{"access_token": access, "refresh_token": refresh, "expires_in": expires, "token_type": "Bearer"})
}

func (e *Engine) completeOIDC(ctx context.Context, req oidcCompleteRequest) (string, string, int, error) {
	if strings.TrimSpace(req.Code) == "" || strings.TrimSpace(req.State) == "" {
		return "", "", 0, errors.New("missing authorization response")
	}
	flow, ok := e.flows.consume(req.State, req.RedirectURI)
	if !ok {
		return "", "", 0, errors.New("invalid authorization state")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {req.Code}, "redirect_uri": {flow.redirectURI}, "client_id": {e.cfg.OIDCClientID}, "code_verifier": {flow.verifier}}
	if e.cfg.OIDCClientSecret != "" {
		form.Set("client_secret", e.cfg.OIDCClientSecret)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, e.oidc.discovery.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", 0, err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := e.oidc.client.Do(httpReq)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", 0, fmt.Errorf("token exchange failed")
	}
	var tokenResponse struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResponse); err != nil || tokenResponse.IDToken == "" {
		return "", "", 0, errors.New("provider returned no ID token")
	}
	return e.issueOIDCSession(ctx, tokenResponse.IDToken, flow.nonce)
}

func (e *Engine) issueOIDCSession(ctx context.Context, idToken, nonce string) (string, string, int, error) {
	claims, err := e.oidc.validate(idToken, nonce)
	if err != nil || claims.Subject == "" {
		return "", "", 0, errors.New("invalid ID token")
	}
	localSubject := deterministicOIDCSubject(e.oidc.issuer, claims.Subject)
	if e.identityLinker != nil {
		localSubject, err = e.identityLinker.LinkOIDCIdentity(ctx, e.oidc.issuer, claims.Subject, claims.Email, oidcEmailVerified(idToken), claims.Email)
		if err != nil || localSubject == "" {
			return "", "", 0, errors.New("identity linking failed")
		}
	}
	return e.issuer.Issue(localSubject, claims.Email, "viewer")
}

func (e *Engine) HandleOIDCDeviceStart(c *fiber.Ctx) error {
	if e.oidc == nil || e.issuer == nil || e.flows == nil || e.oidc.discovery.DeviceAuthorizationEndpoint == "" {
		return authUnavailable(c)
	}
	form := url.Values{"client_id": {e.cfg.OIDCClientID}, "scope": {strings.Join(e.cfg.OIDCScopes, " ")}}
	req, _ := http.NewRequestWithContext(c.Context(), http.MethodPost, e.oidc.discovery.DeviceAuthorizationEndpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := e.oidc.client.Do(req)
	if err != nil {
		return authFailed(c)
	}
	defer resp.Body.Close()
	var provider struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&provider) != nil || provider.DeviceCode == "" || provider.UserCode == "" || provider.VerificationURI == "" {
		return authFailed(c)
	}
	if provider.ExpiresIn <= 0 || provider.ExpiresIn > 1800 {
		provider.ExpiresIn = 600
	}
	if provider.Interval < 1 {
		provider.Interval = 5
	}
	handle := e.flows.putDevice(provider.DeviceCode, time.Now().Add(time.Duration(provider.ExpiresIn)*time.Second), time.Duration(provider.Interval)*time.Second)
	return c.JSON(fiber.Map{"device_handle": handle, "user_code": provider.UserCode, "verification_uri": provider.VerificationURI, "verification_uri_complete": provider.VerificationURIComplete, "expires_in": provider.ExpiresIn, "interval": provider.Interval})
}

func (e *Engine) HandleOIDCDevicePoll(c *fiber.Ctx) error {
	if e.oidc == nil || e.flows == nil {
		return authUnavailable(c)
	}
	var input struct {
		DeviceHandle string `json:"device_handle"`
	}
	if c.BodyParser(&input) != nil {
		return authFailed(c)
	}
	device, ok := e.flows.device(input.DeviceHandle)
	if !ok {
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{"status": "pending"})
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {device.code}, "client_id": {e.cfg.OIDCClientID}}
	if e.cfg.OIDCClientSecret != "" {
		form.Set("client_secret", e.cfg.OIDCClientSecret)
	}
	req, _ := http.NewRequestWithContext(c.Context(), http.MethodPost, e.oidc.discovery.TokenEndpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := e.oidc.client.Do(req)
	if err != nil {
		return authFailed(c)
	}
	defer resp.Body.Close()
	var result struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result.Error == "authorization_pending" || result.Error == "slow_down" {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"status": "pending"})
	}
	if resp.StatusCode != http.StatusOK || result.IDToken == "" {
		e.flows.deleteDevice(input.DeviceHandle)
		return authFailed(c)
	}
	e.flows.deleteDevice(input.DeviceHandle)
	access, refresh, expires, err := e.issueOIDCSession(c.Context(), result.IDToken, "")
	if err != nil {
		return authFailed(c)
	}
	return c.JSON(fiber.Map{"access_token": access, "refresh_token": refresh, "expires_in": expires, "token_type": "Bearer"})
}

func safeLoopbackRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	host := u.Hostname()
	if host != "localhost" && net.ParseIP(host) == nil {
		return false
	}
	if host != "localhost" && !net.ParseIP(host).IsLoopback() {
		return false
	}
	port := u.Port()
	return port != "" && u.Path == "/callback"
}

func deterministicOIDCSubject(issuer, subject string) string {
	sum := sha256.Sum256([]byte(issuer + "\x00" + subject))
	return "oidc_" + hex.EncodeToString(sum[:16])
}
func randomBytes(n int) []byte { b := make([]byte, n); copy(b, mustRandom(n)); return b }
func mustRandom(n int) []byte {
	raw, err := hex.DecodeString(randomHex(n))
	if err != nil {
		panic(err)
	}
	return raw
}
func authFailed(c *fiber.Ctx) error {
	return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "authentication failed"})
}
func authUnavailable(c *fiber.Ctx) error {
	return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "interactive login is unavailable"})
}

func oidcEmailVerified(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		EmailVerified bool `json:"email_verified"`
	}
	return json.Unmarshal(raw, &claims) == nil && claims.EmailVerified
}
