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
	"go.uber.org/zap"
)

const oidcFlowTTL = 10 * time.Minute

type oidcFlow struct {
	redirectURI     string
	returnTo        string
	reauthenticate  bool
	expectedSubject string
	verifier        string
	nonce           string
	client          string
	expiresAt       time.Time
	workspaceID     string
	invitationToken string
	provider        WorkspaceOIDCProvider
	validator       *OIDCValidator
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

func (s *oidcFlowStore) create(redirectURI, client, returnTo string, reauthenticate bool, expectedSubject, workspaceID, invitationToken string, provider WorkspaceOIDCProvider, validator *OIDCValidator) (state string, flow oidcFlow) {
	state = randomHex(32)
	flow = oidcFlow{redirectURI: redirectURI, returnTo: returnTo, reauthenticate: reauthenticate, expectedSubject: expectedSubject, verifier: base64.RawURLEncoding.EncodeToString(randomBytes(48)), nonce: randomHex(32), client: client, expiresAt: time.Now().Add(oidcFlowTTL), workspaceID: workspaceID, invitationToken: invitationToken, provider: provider, validator: validator}
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
	// Browser callbacks recover the exact redirect URI from the one-time flow.
	// CLI callers still supply a redirect URI, which must match byte-for-byte.
	if !ok || time.Now().After(flow.expiresAt) || (redirectURI != "" && !secretEqual(flow.redirectURI, redirectURI)) {
		return oidcFlow{}, false
	}
	return flow, true
}

const oidcCallbackPath = "/api/v1/auth/oidc/callback"

// browserOIDCRedirectURI derives the callback from the browser-facing request.
// This keeps localhost logins aligned with any user-selected listening port.
// A configured public HTTPS callback remains an explicit reverse-proxy override.
func (e *Engine) browserOIDCRedirectURI(c *fiber.Ctx) (string, bool) {
	requestURI := strings.TrimSpace(c.Protocol()) + "://" + strings.TrimSpace(string(c.Context().Host())) + oidcCallbackPath
	requestURL, requestOK := validBrowserCallback(requestURI)
	configuredURL, configuredOK := validBrowserCallback(strings.TrimSpace(e.cfg.OIDCRedirectURL))

	if configuredOK && !isLoopbackHost(configuredURL.Hostname()) {
		return configuredURL.String(), true
	}
	if requestOK {
		return requestURL.String(), true
	}
	if configuredOK {
		return configuredURL.String(), true
	}
	return "", false
}

func validBrowserCallback(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != oidcCallbackPath {
		return nil, false
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return nil, false
	}
	return u, true
}

func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
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
	RedirectURI     string `json:"redirect_uri"`
	Client          string `json:"client"`
	ReturnTo        string `json:"return_to"`
	Reauthenticate  bool   `json:"reauthenticate"`
	WorkspaceID     string `json:"workspace_id"`
	InvitationToken string `json:"invitation_token"`
}
type oidcCompleteRequest struct {
	State       string `json:"state"`
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
}

// oidcFlowError records only the stage that failed. Handlers log that stable,
// non-identifying stage while continuing to return the same generic response
// to the browser, so diagnostics improve without creating an account oracle or
// putting authorization codes/tokens in logs.
type oidcFlowError struct {
	stage       string
	err         error
	workspaceID string
}

func (e *oidcFlowError) Error() string        { return e.stage }
func (e *oidcFlowError) Unwrap() error        { return e.err }
func oidcStage(stage string, err error) error { return &oidcFlowError{stage: stage, err: err} }
func oidcStageForFlow(flow oidcFlow, stage string, err error) error {
	return &oidcFlowError{stage: stage, err: err, workspaceID: flow.workspaceID}
}
func oidcErrorForFlow(flow oidcFlow, err error) error {
	return oidcStageForFlow(flow, oidcFailureStage(err), err)
}
func oidcFailureStage(err error) string {
	var flowErr *oidcFlowError
	if errors.As(err, &flowErr) && flowErr.stage != "" {
		return flowErr.stage
	}
	return "unknown"
}

// HandleOIDCConfig exposes only non-secret login metadata needed by clients.
func (e *Engine) HandleOIDCConfig(c *fiber.Ctx) error {
	if workspaceID := strings.TrimSpace(c.Query("workspace_id")); workspaceID != "" && e.workspaceProviderResolver != nil {
		provider, ok := e.workspaceProviderResolver(c.UserContext(), workspaceID)
		if ok {
			return c.JSON(fiber.Map{"enabled": e.issuer != nil, "mode": e.cfg.Mode, "provider_type": provider.ProviderType, "workspace_id": workspaceID})
		}
		// Workspaces created before per-workspace identity configuration use
		// the deployment provider until they are migrated.
		return c.JSON(fiber.Map{"enabled": e.oidc != nil && e.issuer != nil, "mode": e.cfg.Mode, "provider_type": oidcProviderType(e.cfg.OIDCIssuer), "workspace_id": workspaceID})
	}
	return c.JSON(fiber.Map{"enabled": e.oidc != nil && e.issuer != nil, "mode": e.cfg.Mode, "device_flow": e.oidc != nil && e.oidc.discovery.DeviceAuthorizationEndpoint != ""})
}

func oidcProviderType(issuer string) string {
	issuer = strings.ToLower(issuer)
	switch {
	case strings.Contains(issuer, "google"):
		return "google"
	case strings.Contains(issuer, "microsoftonline"):
		return "microsoft"
	case strings.Contains(issuer, "okta"):
		return "okta"
	case strings.Contains(issuer, "auth0"):
		return "auth0"
	case strings.Contains(issuer, "keycloak"):
		return "keycloak"
	default:
		return "custom"
	}
}

// HandleOIDCStart begins an Authorization Code + PKCE flow. CLI redirect URIs
// are restricted to loopback addresses; GUI uses the single operator-registered
// callback and cannot supply an arbitrary redirect.
func (e *Engine) HandleOIDCStart(c *fiber.Ctx) error {
	if e.issuer == nil || e.flows == nil {
		return authUnavailable(c)
	}
	req := oidcStartRequest{RedirectURI: c.Query("redirect_uri"), Client: c.Query("client"), ReturnTo: c.Query("return_to"), Reauthenticate: c.QueryBool("reauthenticate"), WorkspaceID: c.Query("workspace_id"), InvitationToken: c.Query("invitation_token")}
	if len(c.Body()) > 0 && c.Method() == fiber.MethodPost {
		if err := c.BodyParser(&req); err != nil {
			return authFailed(c)
		}
	}
	req.Client = strings.ToLower(strings.TrimSpace(req.Client))
	if req.Client == "" {
		req.Client = "gui"
	}
	var expectedSubject, loginHint string
	workspaceID := strings.TrimSpace(req.WorkspaceID)
	invitationToken := strings.TrimSpace(req.InvitationToken)
	if workspaceID == "" || len(invitationToken) > 1024 {
		invitationToken = ""
	}
	provider := WorkspaceOIDCProvider{Issuer: e.cfg.OIDCIssuer, ClientID: e.cfg.OIDCClientID, ClientSecret: e.cfg.OIDCClientSecret, Audience: e.cfg.OIDCAudience, Scopes: e.cfg.OIDCScopes}
	validator := e.oidc
	if workspaceID != "" {
		if e.workspaceAvailability != nil && !e.workspaceAvailability(c.UserContext(), workspaceID) {
			return authUnavailable(c)
		}
		if e.workspaceProviderResolver == nil {
			if validator == nil {
				return authUnavailable(c)
			}
		} else {
			resolved, ok := e.workspaceProviderResolver(c.UserContext(), workspaceID)
			if ok {
				provider = resolved
				var err error
				validator, err = e.workspaceValidator(provider)
				if err != nil {
					return authUnavailable(c)
				}
			} else if validator == nil {
				return authUnavailable(c)
			}
		}
	}
	if validator == nil || provider.ClientID == "" {
		return authUnavailable(c)
	}
	if req.Client == "gui" {
		var ok bool
		req.RedirectURI, ok = e.browserOIDCRedirectURI(c)
		req.ReturnTo = safeGUIReturnTo(req.ReturnTo)
		if !ok {
			return authUnavailable(c)
		}
		if req.Reauthenticate {
			claims, err := e.currentGUIIdentity(c)
			if err != nil {
				return authFailed(c)
			}
			expectedSubject, loginHint = claims.Subject, claims.Email
		}
	} else if req.Client != "cli" || !safeLoopbackRedirect(req.RedirectURI) {
		return authFailed(c)
	} else if req.Reauthenticate {
		return authFailed(c)
	}
	state, flow := e.flows.create(req.RedirectURI, req.Client, req.ReturnTo, req.Reauthenticate, expectedSubject, workspaceID, invitationToken, provider, validator)
	challenge := sha256.Sum256([]byte(flow.verifier))
	params := url.Values{
		"response_type": {"code"}, "client_id": {provider.ClientID}, "redirect_uri": {req.RedirectURI},
		"scope": {strings.Join(provider.Scopes, " ")}, "state": {state}, "nonce": {flow.nonce},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
	}
	if flow.reauthenticate {
		// This flow exists to satisfy the recent-auth gate, so a silent SSO
		// round trip is not sufficient proof that the administrator is still
		// present. These are standard OIDC authorization parameters; providers
		// that support step-up must interact with the user before returning.
		params.Set("prompt", "login")
		params.Set("max_age", "0")
		if loginHint != "" {
			params.Set("login_hint", loginHint)
		}
	}
	authURL := validator.discovery.AuthorizationEndpoint + "?" + params.Encode()
	if c.QueryBool("navigate") {
		return c.Redirect(authURL, fiber.StatusFound)
	}
	return c.JSON(fiber.Map{"authorization_url": authURL, "state": state, "expires_in": int(oidcFlowTTL.Seconds())})
}

// currentGUIIdentity binds a step-up flow to the already authenticated human.
// The start endpoint is public so that first-time sign-in works, therefore it
// verifies the HttpOnly access cookie directly rather than relying on auth
// middleware that intentionally does not wrap this route.
func (e *Engine) currentGUIIdentity(c *fiber.Ctx) (*Claims, error) {
	if e.issuer == nil {
		return nil, errors.New("session issuer unavailable")
	}
	claims, err := e.issuer.VerifyAccess(c.Cookies("soulacy_access"))
	if err != nil || claims == nil || strings.TrimSpace(claims.Subject) == "" {
		return nil, errors.New("authenticated session required")
	}
	if kind := strings.TrimSpace(claims.PrincipalKind); kind != "" && kind != "user" {
		return nil, errors.New("interactive user required")
	}
	return claims, nil
}

// HandleOIDCCallback completes the GUI flow and stores tokens only in secure,
// HttpOnly same-origin cookies. Tokens never appear in redirect URLs/history.
func (e *Engine) HandleOIDCCallback(c *fiber.Ctx) error {
	// The callback URI is already bound to the one-time state. Recover it from
	// that flow so a configurable port cannot drift between start and callback.
	access, refresh, expires, returnTo, err := e.completeOIDC(c.Context(), oidcCompleteRequest{State: c.Query("state"), Code: c.Query("code")})
	if err != nil {
		if e.log != nil {
			e.log.Warn("auth: OIDC callback failed", zap.String("stage", oidcFailureStage(err)))
		}
		return c.Redirect(oidcFailureRedirect(err, c.Query("error")), fiber.StatusFound)
	}
	e.setAuthCookies(c, access, refresh, expires)
	return c.Redirect(returnTo, fiber.StatusFound)
}

// HandleOIDCComplete completes a CLI loopback flow. The authorization code is
// accepted only with its original one-time state and exact loopback redirect.
func (e *Engine) HandleOIDCComplete(c *fiber.Ctx) error {
	var req oidcCompleteRequest
	if err := c.BodyParser(&req); err != nil || !safeLoopbackRedirect(req.RedirectURI) {
		return authFailed(c)
	}
	access, refresh, expires, _, err := e.completeOIDC(c.Context(), req)
	if err != nil {
		return authFailed(c)
	}
	return c.JSON(fiber.Map{"access_token": access, "refresh_token": refresh, "expires_in": expires, "token_type": "Bearer"})
}

func (e *Engine) completeOIDC(ctx context.Context, req oidcCompleteRequest) (string, string, int, string, error) {
	if strings.TrimSpace(req.State) == "" {
		return "", "", 0, "", oidcStage("authorization_response", errors.New("missing authorization response"))
	}
	flow, ok := e.flows.consume(req.State, req.RedirectURI)
	if !ok {
		return "", "", 0, "", oidcStage("state", errors.New("invalid authorization state"))
	}
	if flow.workspaceID != "" {
		if !e.workspaceAvailableForOIDCCallback(ctx, flow.workspaceID) {
			return "", "", 0, "", oidcStageForFlow(flow, "workspace_unavailable", errors.New("workspace is not accepting sign-ins"))
		}
	}
	if strings.TrimSpace(req.Code) == "" {
		return "", "", 0, "", oidcStageForFlow(flow, "authorization_response", errors.New("missing authorization response"))
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {req.Code}, "redirect_uri": {flow.redirectURI}, "client_id": {flow.provider.ClientID}, "code_verifier": {flow.verifier}}
	if flow.provider.ClientSecret != "" {
		form.Set("client_secret", flow.provider.ClientSecret)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, flow.validator.discovery.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", 0, "", oidcStageForFlow(flow, "token_request", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := flow.validator.client.Do(httpReq)
	if err != nil {
		return "", "", 0, "", oidcStageForFlow(flow, "token_transport", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", 0, "", oidcStageForFlow(flow, "token_exchange", fmt.Errorf("token exchange failed"))
	}
	var tokenResponse struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResponse); err != nil || tokenResponse.IDToken == "" {
		return "", "", 0, "", oidcStageForFlow(flow, "token_response", errors.New("provider returned no ID token"))
	}
	access, refresh, expires, err := e.issueOIDCSessionWithProvider(ctx, tokenResponse.IDToken, flow.nonce, flow.expectedSubject, flow.workspaceID, flow.invitationToken, flow.validator)
	if err != nil {
		return "", "", 0, "", oidcErrorForFlow(flow, err)
	}
	return access, refresh, expires, safeGUIReturnTo(flow.returnTo), nil
}

func (e *Engine) workspaceAvailableForOIDCCallback(ctx context.Context, workspaceID string) bool {
	if e.workspaceAvailability != nil {
		return e.workspaceAvailability(ctx, workspaceID)
	}
	if e.workspaceProviderResolver != nil {
		// Compatibility for embedders that have not wired the dedicated
		// lifecycle resolver. The application wires it, which lets legacy
		// workspaces intentionally use the deployment provider.
		current, resolved := e.workspaceProviderResolver(ctx, workspaceID)
		return resolved && current.ClientID != "" && current.Issuer != ""
	}
	return true
}

func (e *Engine) issueOIDCSession(ctx context.Context, idToken, nonce, expectedSubject string) (string, string, int, error) {
	return e.issueOIDCSessionWithProvider(ctx, idToken, nonce, expectedSubject, "", "", e.oidc)
}

func (e *Engine) issueOIDCSessionWithProvider(ctx context.Context, idToken, nonce, expectedSubject, workspaceID, invitationToken string, validator *OIDCValidator) (string, string, int, error) {
	if validator == nil {
		return "", "", 0, oidcStage("provider", errors.New("identity provider unavailable"))
	}
	claims, err := validator.validate(idToken, nonce)
	if err != nil || claims.Subject == "" {
		return "", "", 0, oidcStage("id_token", errors.New("invalid ID token"))
	}
	localSubject := deterministicOIDCSubject(validator.issuer, claims.Subject)
	if e.identityLinker != nil {
		localSubject, err = e.identityLinker.LinkOIDCIdentity(ctx, validator.issuer, claims.Subject, claims.Email, oidcEmailVerified(idToken), claims.Email)
		if err != nil || localSubject == "" {
			stage := "identity_link"
			var safeStager interface{ SafeStage() string }
			if errors.As(err, &safeStager) && safeStager.SafeStage() != "" {
				stage += "." + safeStager.SafeStage()
			}
			return "", "", 0, oidcStage(stage, errors.New("identity linking failed"))
		}
	}
	if expectedSubject != "" && !secretEqual(localSubject, expectedSubject) {
		return "", "", 0, oidcStage("reauthentication_subject", errors.New("reauthentication identity changed"))
	}
	// A signed-in person acts as a workspace member, so the token carries the
	// membership the resolver reports. With no resolver (personal deployments)
	// this is the un-tenanted token those installations have always issued.
	base := TokenIdentity{
		Subject: localSubject, Email: claims.Email, Role: "viewer", PrincipalKind: "user",
	}
	var id TokenIdentity
	var ok bool
	if workspaceID != "" && invitationToken != "" && e.workspaceInvitationAccepter != nil {
		id, ok = e.workspaceInvitationAccepter(ctx, invitationToken, localSubject, workspaceID)
		id.Email = claims.Email
		id.PrincipalKind = "user"
	} else if workspaceID != "" && e.workspaceIdentityResolver != nil {
		id, ok = e.workspaceIdentityResolver(ctx, localSubject, workspaceID)
		id.Email = claims.Email
		id.PrincipalKind = "user"
	} else {
		id, ok = e.tokenIdentityFor(ctx, base)
	}
	if !ok {
		return "", "", 0, oidcStage("membership", errors.New("no active workspace membership"))
	}
	// Completing an OIDC authorization flow is interactive proof, including
	// the explicit step-up path. Stamp auth_time now and begin a new refresh
	// family so the resumed high-impact operation does not immediately ask the
	// administrator to authenticate again.
	access, refresh, expires, err := e.issuer.Reauthenticate(id)
	if err != nil {
		return "", "", 0, oidcStage("session_issue", err)
	}
	return access, refresh, expires, nil
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
	access, refresh, expires, err := e.issueOIDCSession(c.Context(), result.IDToken, "", "")
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

// safeGUIReturnTo is intentionally an allowlist, not a same-origin URL parse.
// A return destination enters the OIDC state before authentication and is used
// after it, so accepting an arbitrary URL here would turn the callback into an
// open redirect. Add named application destinations only as they are needed.
func safeGUIReturnTo(raw string) string {
	switch strings.TrimSpace(raw) {
	case "/admin/setup", "/admin/setup?resume=workspace-create", "/admin/login":
		return strings.TrimSpace(raw)
	default:
		return "/#auth=success"
	}
}

// oidcFailureRedirect turns browser callback failures into a safe, useful UI
// destination. Only a small public error vocabulary crosses the redirect; the
// precise stage remains in structured server logs and cannot become an account
// or tenant-membership oracle. A known workspace returns to its branded access
// page, while invalid/expired state fails closed to the public landing page.
func oidcFailureRedirect(err error, providerError string) string {
	code := "sign_in_failed"
	stage := oidcFailureStage(err)
	switch {
	case strings.TrimSpace(providerError) != "":
		code = "cancelled"
	case stage == "state":
		code = "session_expired"
	case stage == "membership":
		code = "not_authorized"
	case stage == "workspace_unavailable":
		code = "workspace_unavailable"
	}
	base := "/"
	var flowErr *oidcFlowError
	if errors.As(err, &flowErr) && strings.TrimSpace(flowErr.workspaceID) != "" {
		base = "/w/" + url.PathEscape(strings.TrimSpace(flowErr.workspaceID))
	}
	return base + "?auth_error=" + url.QueryEscape(code)
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
