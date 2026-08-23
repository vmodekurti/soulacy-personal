package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

type flowTransport struct {
	tokenResponse func(url.Values) (int, string)
}

func (t flowTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	status, body := http.StatusNotFound, `{}`
	if req.URL.Path == "/token" {
		raw, _ := io.ReadAll(req.Body)
		form, _ := url.ParseQuery(string(raw))
		status, body = t.tokenResponse(form)
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

type captureLinker struct {
	issuer, subject string
	verified        bool
}

func (l *captureLinker) LinkOIDCIdentity(_ context.Context, issuer, subject, _ string, verified bool, _ string) (string, error) {
	l.issuer, l.subject, l.verified = issuer, subject, verified
	return "usr_0123456789abcdef0123456789abcdef", nil
}

func TestOIDCPKCEFlowValidatesStateNonceAndVerifiedSubject(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuerURL := "https://issuer.example"
	validator := &OIDCValidator{issuer: issuerURL, audience: "client", client: &http.Client{}, keys: map[string]any{"key": &key.PublicKey}, jwksURI: issuerURL + "/jwks", allowedAlgorithms: map[string]struct{}{"RS256": {}}, discovery: oidcDiscovery{Issuer: issuerURL, AuthorizationEndpoint: issuerURL + "/authorize", TokenEndpoint: issuerURL + "/token"}, quit: make(chan struct{})}
	validator.client.Transport = flowTransport{tokenResponse: func(form url.Values) (int, string) {
		if form.Get("code_verifier") == "" || form.Get("redirect_uri") != "http://127.0.0.1:32123/callback" {
			return http.StatusBadRequest, `{}`
		}
		// The test captures nonce from the start response below.
		return http.StatusInternalServerError, `{}`
	}}
	issuer, _ := newIssuer("01234567890123456789012345678901", time.Minute, time.Hour)
	defer issuer.Close()
	engine := &Engine{cfg: Config{Mode: "jwt", OIDCClientID: "client", OIDCAudience: "client", OIDCScopes: []string{"openid"}, JWTRefreshTTL: time.Hour}, issuer: issuer, oidc: validator, flows: newOIDCFlowStore(), log: zap.NewNop()}
	defer engine.flows.close()
	linker := &captureLinker{}
	engine.SetIdentityLinker(linker)
	app := fiber.New()
	app.Post("/start", engine.HandleOIDCStart)
	app.Post("/complete", engine.HandleOIDCComplete)
	startBody := []byte(`{"client":"cli","redirect_uri":"http://127.0.0.1:32123/callback"}`)
	startResp, _ := app.Test(newFiberRequest(http.MethodPost, "/start", startBody))
	defer startResp.Body.Close()
	if startResp.StatusCode != http.StatusOK {
		t.Fatalf("start status=%d", startResp.StatusCode)
	}
	var start struct {
		AuthorizationURL string `json:"authorization_url"`
		State            string `json:"state"`
	}
	if json.NewDecoder(startResp.Body).Decode(&start) != nil {
		t.Fatal("decode start")
	}
	authURL, _ := url.Parse(start.AuthorizationURL)
	nonce := authURL.Query().Get("nonce")
	if authURL.Query().Get("code_challenge_method") != "S256" || nonce == "" || authURL.Query().Get("state") != start.State {
		t.Fatal("missing PKCE, nonce, or state")
	}
	validator.client.Transport = flowTransport{tokenResponse: func(form url.Values) (int, string) {
		claims := jwt.MapClaims{"iss": issuerURL, "aud": "client", "sub": "provider-subject", "email": "verified@example.test", "email_verified": true, "nonce": nonce, "exp": time.Now().Add(time.Minute).Unix()}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = "key"
		signed, _ := tok.SignedString(key)
		raw, _ := json.Marshal(map[string]string{"id_token": signed})
		return http.StatusOK, string(raw)
	}}
	complete, _ := json.Marshal(map[string]string{"state": start.State, "code": "authorization-code", "redirect_uri": "http://127.0.0.1:32123/callback"})
	completeResp, _ := app.Test(newFiberRequest(http.MethodPost, "/complete", complete))
	defer completeResp.Body.Close()
	if completeResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(completeResp.Body)
		t.Fatalf("complete status=%d body=%s", completeResp.StatusCode, raw)
	}
	var issuedSession struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(completeResp.Body).Decode(&issuedSession); err != nil || issuedSession.AccessToken == "" {
		t.Fatalf("OIDC completion returned no access token: %v", err)
	}
	issuedClaims, err := issuer.VerifyAccess(issuedSession.AccessToken)
	if err != nil {
		t.Fatalf("issued OIDC access token was invalid: %v", err)
	}
	if issuedClaims.AuthTime < time.Now().Add(-time.Minute).Unix() {
		t.Fatalf("OIDC authentication did not refresh auth_time: %d", issuedClaims.AuthTime)
	}
	if issuedClaims.PrincipalKind != "user" {
		t.Fatalf("OIDC session principal kind = %q, want user", issuedClaims.PrincipalKind)
	}
	if linker.issuer != issuerURL || linker.subject != "provider-subject" || !linker.verified {
		t.Fatalf("identity was not linked from verified provider claims: %+v", linker)
	}
	// The state was consumed and cannot be replayed.
	replayResp, _ := app.Test(newFiberRequest(http.MethodPost, "/complete", complete))
	defer replayResp.Body.Close()
	if replayResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replay status=%d", replayResp.StatusCode)
	}
}

func newFiberRequest(method, path string, body []byte) *http.Request {
	req, _ := http.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestRefreshReplayRevokesRotatedFamilyAndLogoutRevokesAccess(t *testing.T) {
	issuer, err := newIssuer("01234567890123456789012345678901", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer issuer.Close()
	access, refresh, _, _ := issuer.IssueFor(TokenIdentity{Subject: "user", Email: "", Role: "viewer"})
	_, rotated, _, err := issuer.Refresh(refresh)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = issuer.Refresh(refresh); err == nil {
		t.Fatal("refresh replay was accepted")
	}
	if _, _, _, err = issuer.Refresh(rotated); err == nil {
		t.Fatal("family remained valid after replay")
	}
	if _, err = issuer.VerifyAccess(access); err != nil {
		t.Fatalf("access unexpectedly invalid: %v", err)
	}
	issuer.Revoke(access, "")
	if _, err = issuer.VerifyAccess(access); err == nil {
		t.Fatal("logged-out access token remained valid")
	}
}

func TestRefreshAuthorizationDenialRevokesFamily(t *testing.T) {
	issuer, err := newIssuer("01234567890123456789012345678901", time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer issuer.Close()
	_, refresh, _, err := issuer.IssueFor(TokenIdentity{Subject: "suspended-user", Email: "", Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := issuer.RefreshAuthorized(refresh, func(subject string) (TokenIdentity, bool) {
		return TokenIdentity{Subject: subject}, subject != "suspended-user"
	}); err == nil {
		t.Fatal("suspended user refresh succeeded")
	}
	if _, _, _, err := issuer.Refresh(refresh); err == nil {
		t.Fatal("denied refresh token family remained usable")
	}
}

func TestSafeLoopbackRedirectRejectsRemoteAndConfusedURLs(t *testing.T) {
	valid := []string{"http://127.0.0.1:1234/callback", "http://[::1]:4444/callback", "http://localhost:9999/callback"}
	for _, candidate := range valid {
		if !safeLoopbackRedirect(candidate) {
			t.Errorf("rejected %s", candidate)
		}
	}
	invalid := []string{"https://127.0.0.1:1/callback", "http://127.0.0.1.evil.test:1/callback", "http://localhost:1/other", "http://localhost/callback", "http://user@localhost:1/callback"}
	for _, candidate := range invalid {
		if safeLoopbackRedirect(candidate) {
			t.Errorf("accepted %s", candidate)
		}
	}
}

func TestGUIReturnDestinationIsAllowlisted(t *testing.T) {
	if got := safeGUIReturnTo("/admin/setup"); got != "/admin/setup" {
		t.Fatalf("admin return = %q", got)
	}
	if got := safeGUIReturnTo("/admin/setup?resume=workspace-create"); got != "/admin/setup?resume=workspace-create" {
		t.Fatalf("workspace-create return = %q", got)
	}
	for _, candidate := range []string{"https://evil.example", "//evil.example", "/admin/setup?next=https://evil.example", "/"} {
		if got := safeGUIReturnTo(candidate); got != "/#auth=success" {
			t.Errorf("unsafe return %q became %q", candidate, got)
		}
	}
}

func TestOIDCFailureRedirectUsesSafeMessagesAndWorkspaceDestination(t *testing.T) {
	tests := []struct {
		name, stage, providerError, workspaceID, want string
	}{
		{"expired state", "state", "", "", "/?auth_error=session_expired"},
		{"provider cancellation", "authorization_response", "access_denied", "", "/?auth_error=cancelled"},
		{"workspace membership", "membership", "", "ws_customer", "/w/ws_customer?auth_error=not_authorized"},
		{"generic workspace failure", "token_exchange", "", "ws_customer", "/w/ws_customer?auth_error=sign_in_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &oidcFlowError{stage: tt.stage, err: errors.New("internal detail"), workspaceID: tt.workspaceID}
			if got := oidcFailureRedirect(err, tt.providerError); got != tt.want {
				t.Fatalf("redirect = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOIDCReauthenticationForcesProviderInteraction(t *testing.T) {
	issuer, err := newIssuer("01234567890123456789012345678901", time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer issuer.Close()
	validator := &OIDCValidator{
		discovery: oidcDiscovery{AuthorizationEndpoint: "https://issuer.example/authorize"},
		quit:      make(chan struct{}),
	}
	engine := &Engine{
		cfg: Config{
			Mode: "jwt", OIDCClientID: "client", OIDCScopes: []string{"openid"},
			OIDCRedirectURL: "http://localhost:1947/api/v1/auth/oidc/callback",
		},
		issuer: issuer, oidc: validator, flows: newOIDCFlowStore(), log: zap.NewNop(),
	}
	defer engine.flows.close()
	app := fiber.New()
	app.Get("/start", engine.HandleOIDCStart)
	access, _, _, err := issuer.IssueFor(TokenIdentity{Subject: "usr_owner", Email: "owner@example.test", Role: "owner", AuthTime: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	req := newFiberRequest(http.MethodGet, "/start?client=gui&reauthenticate=true&return_to=%2Fadmin%2Fsetup%3Fresume%3Dworkspace-create", nil)
	req.AddCookie(&http.Cookie{Name: "soulacy_access", Value: access})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start status=%d", resp.StatusCode)
	}
	var body struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	authURL, err := url.Parse(body.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if authURL.Query().Get("prompt") != "login" || authURL.Query().Get("max_age") != "0" {
		t.Fatalf("step-up parameters missing from %s", body.AuthorizationURL)
	}
	if authURL.Query().Get("login_hint") != "owner@example.test" {
		t.Fatalf("step-up was not bound to the current owner's email: %s", body.AuthorizationURL)
	}
}

func TestOIDCStartDerivesCallbackFromCustomLocalPort(t *testing.T) {
	issuer, err := newIssuer("01234567890123456789012345678901", time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer issuer.Close()
	validator := &OIDCValidator{
		discovery: oidcDiscovery{AuthorizationEndpoint: "https://issuer.example/authorize"},
		quit:      make(chan struct{}),
	}
	engine := &Engine{
		// This simulates an older installation whose persisted local callback
		// still names the previous port.
		cfg: Config{
			Mode: "jwt", OIDCClientID: "client", OIDCScopes: []string{"openid"},
			OIDCRedirectURL: "http://localhost:18789/api/v1/auth/oidc/callback",
		},
		issuer: issuer, oidc: validator, flows: newOIDCFlowStore(), log: zap.NewNop(),
	}
	defer engine.flows.close()
	app := fiber.New()
	app.Get("/start", engine.HandleOIDCStart)
	req := newFiberRequest(http.MethodGet, "/start?client=gui", nil)
	req.Host = "localhost:1947"
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	authURL, err := url.Parse(body.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := authURL.Query().Get("redirect_uri"), "http://localhost:1947/api/v1/auth/oidc/callback"; got != want {
		t.Fatalf("redirect_uri = %q, want %q", got, want)
	}
}

func TestOIDCStartPreservesExplicitPublicCallback(t *testing.T) {
	engine := &Engine{cfg: Config{OIDCRedirectURL: "https://agents.example.com/api/v1/auth/oidc/callback"}}
	app := fiber.New()
	app.Get("/callback", func(c *fiber.Ctx) error {
		got, ok := engine.browserOIDCRedirectURI(c)
		return c.JSON(fiber.Map{"redirect_uri": got, "ok": ok})
	})
	req := newFiberRequest(http.MethodGet, "/callback", nil)
	req.Host = "localhost:27777"
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		RedirectURI string `json:"redirect_uri"`
		OK          bool   `json:"ok"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.RedirectURI != "https://agents.example.com/api/v1/auth/oidc/callback" {
		t.Fatalf("public override not preserved: %+v", body)
	}
}

func TestOIDCFlowCallbackRecoversBoundRedirectURI(t *testing.T) {
	store := newOIDCFlowStore()
	defer store.close()
	state, want := store.create("http://localhost:27777/api/v1/auth/oidc/callback", "gui", "", false, "", "", "", WorkspaceOIDCProvider{}, nil)
	got, ok := store.consume(state, "")
	if !ok || got.redirectURI != want.redirectURI {
		t.Fatalf("callback did not recover redirect: ok=%v got=%q want=%q", ok, got.redirectURI, want.redirectURI)
	}
}

func TestOIDCStartUsesWorkspaceProvider(t *testing.T) {
	issuer, err := newIssuer("01234567890123456789012345678901", time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer issuer.Close()
	provider := WorkspaceOIDCProvider{
		ProviderType: "okta", Issuer: "https://tenant.okta.example",
		ClientID: "workspace-client", Audience: "workspace-client",
		Scopes: []string{"openid", "email"},
	}
	validator := &OIDCValidator{
		discovery: oidcDiscovery{AuthorizationEndpoint: "https://tenant.okta.example/authorize"},
		quit:      make(chan struct{}),
	}
	engine := &Engine{
		cfg:    Config{Mode: "jwt", OIDCRedirectURL: "http://localhost:1947/api/v1/auth/oidc/callback"},
		issuer: issuer, flows: newOIDCFlowStore(), log: zap.NewNop(),
		providerValidators: map[string]*OIDCValidator{provider.Issuer + "\x00" + provider.Audience: validator},
	}
	defer engine.flows.close()
	engine.SetWorkspaceOIDCProviderResolver(func(_ context.Context, workspaceID string) (WorkspaceOIDCProvider, bool) {
		return provider, workspaceID == "ws_customer"
	})
	app := fiber.New()
	app.Get("/start", engine.HandleOIDCStart)
	resp, err := app.Test(newFiberRequest(http.MethodGet, "/start?client=gui&workspace_id=ws_customer&invitation_token=invite-secret", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("start status=%d body=%s", resp.StatusCode, raw)
	}
	var body struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(body.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "tenant.okta.example" || parsed.Query().Get("client_id") != "workspace-client" || parsed.Query().Get("scope") != "openid email" {
		t.Fatalf("workspace provider was not used: %s", body.AuthorizationURL)
	}
	state := parsed.Query().Get("state")
	engine.flows.mu.Lock()
	flow := engine.flows.flows[state]
	engine.flows.mu.Unlock()
	if flow.workspaceID != "ws_customer" || flow.invitationToken != "invite-secret" {
		t.Fatalf("workspace invitation context was not preserved in OIDC state: %+v", flow)
	}
}

func TestOIDCStartAllowsLegacyWorkspaceDeploymentProviderFallback(t *testing.T) {
	issuer, err := newIssuer("01234567890123456789012345678901", time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer issuer.Close()
	validator := &OIDCValidator{
		discovery: oidcDiscovery{AuthorizationEndpoint: "https://accounts.example/authorize"},
		quit:      make(chan struct{}),
	}
	engine := &Engine{
		cfg: Config{
			Mode: "jwt", OIDCRedirectURL: "http://localhost:1947/api/v1/auth/oidc/callback",
			OIDCIssuer: "https://accounts.example", OIDCClientID: "deployment-client",
			OIDCAudience: "deployment-client", OIDCScopes: []string{"openid", "email"},
		},
		issuer: issuer, oidc: validator, flows: newOIDCFlowStore(), log: zap.NewNop(),
		providerValidators: map[string]*OIDCValidator{},
	}
	defer engine.flows.close()
	engine.SetWorkspaceOIDCAvailabilityResolver(func(_ context.Context, workspaceID string) bool {
		return workspaceID == "ws_legacy"
	})
	engine.SetWorkspaceOIDCProviderResolver(func(context.Context, string) (WorkspaceOIDCProvider, bool) {
		return WorkspaceOIDCProvider{}, false
	})
	app := fiber.New()
	app.Get("/start", engine.HandleOIDCStart)
	resp, err := app.Test(newFiberRequest(http.MethodGet, "/start?client=gui&workspace_id=ws_legacy", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("start status=%d body=%s", resp.StatusCode, raw)
	}
	var body struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(body.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("client_id") != "deployment-client" {
		t.Fatalf("deployment provider fallback was not used: %s", body.AuthorizationURL)
	}
}

func TestOIDCStartRejectsUnavailableWorkspaceBeforeProviderFallback(t *testing.T) {
	issuer, err := newIssuer("01234567890123456789012345678901", time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer issuer.Close()
	engine := &Engine{
		cfg:    Config{Mode: "jwt", OIDCRedirectURL: "http://localhost:1947/api/v1/auth/oidc/callback"},
		issuer: issuer, oidc: &OIDCValidator{}, flows: newOIDCFlowStore(), log: zap.NewNop(),
	}
	defer engine.flows.close()
	engine.SetWorkspaceOIDCAvailabilityResolver(func(context.Context, string) bool { return false })
	app := fiber.New()
	app.Get("/start", engine.HandleOIDCStart)
	resp, err := app.Test(newFiberRequest(http.MethodGet, "/start?client=gui&workspace_id=ws_missing", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
}

func TestOIDCCallbackAvailabilityAllowsLegacyProviderFallback(t *testing.T) {
	engine := &Engine{}
	engine.SetWorkspaceOIDCAvailabilityResolver(func(_ context.Context, workspaceID string) bool {
		return workspaceID == "ws_legacy"
	})
	engine.SetWorkspaceOIDCProviderResolver(func(context.Context, string) (WorkspaceOIDCProvider, bool) {
		return WorkspaceOIDCProvider{}, false
	})
	if !engine.workspaceAvailableForOIDCCallback(context.Background(), "ws_legacy") {
		t.Fatal("legacy workspace using the deployment provider was rejected at callback")
	}
	if engine.workspaceAvailableForOIDCCallback(context.Background(), "ws_suspended") {
		t.Fatal("unavailable workspace was accepted at callback")
	}
}

func TestOIDCValidatorRejectsUnadvertisedAlgorithm(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	v := &OIDCValidator{issuer: "issuer", audience: "client", keys: map[string]any{"key": &key.PublicKey}, allowedAlgorithms: map[string]struct{}{"RS512": {}}, client: &http.Client{}, quit: make(chan struct{})}
	claims := jwt.MapClaims{"iss": "issuer", "aud": "client", "sub": "sub", "exp": time.Now().Add(time.Minute).Unix()}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "key"
	signed, _ := tok.SignedString(key)
	if _, err := v.Validate(signed); err == nil {
		t.Fatal("accepted an algorithm not advertised by discovery")
	}
}

func rsaJWK(key *rsa.PublicKey) (string, string) {
	return base64.RawURLEncoding.EncodeToString(key.N.Bytes()), base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
}
