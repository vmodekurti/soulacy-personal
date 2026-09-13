// auth_test.go — unit tests for the Soulacy auth subsystem.
//
// Coverage:
//   - Issuer: Issue/VerifyAccess/Refresh lifecycle, expiry, wrong-secret,
//     single-use refresh rotation.
//   - secretEqual: constant-time comparison helper.
//   - Engine.Middleware: open mode, static API key, query-param fallback,
//     JWT mode with local tokens, managed sk_ API keys.
//   - Engine handlers: /auth/token, /auth/refresh, /auth/me.
//   - JWKS parsers: parseRSAPublicKey and parseECPublicKey roundtrip and
//     error paths.
//
// No external network calls — OIDC integration tests are omitted because
// newOIDCValidator does a synchronous discovery fetch. The JWKS key parsers
// (pure math) are tested directly instead.
//
// No httptest.NewServer — Fiber App.Test is used for all HTTP interactions
// (consistent with the project-wide constraint).
package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/auth/apikeys"
	"github.com/soulacy/soulacy/internal/httptestutil"
)

// ---------------------------------------------------------------------------
// Helpers shared across all sub-groups
// ---------------------------------------------------------------------------

// newTestIssuer creates an Issuer with a fixed secret and the given TTLs.
func newTestIssuer(t *testing.T, accessTTL, refreshTTL time.Duration) *Issuer {
	t.Helper()
	iss, err := newIssuer("test-secret-123", accessTTL, refreshTTL)
	if err != nil {
		t.Fatalf("newIssuer: %v", err)
	}
	t.Cleanup(iss.Close)
	return iss
}

// newTestEngine creates an auth Engine in the given mode.
// staticKey may be "" for open mode. log is always a nop.
func newTestEngine(t *testing.T, mode, staticKey string) *Engine {
	t.Helper()
	cfg := Config{Mode: mode, JWTSecret: "auth-test-secret"}
	e, err := New(cfg, staticKey, zap.NewNop())
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	t.Cleanup(e.Close)
	return e
}

// newAuthApp builds a minimal Fiber app wired with the engine's middleware
// and the three auth handler routes.
func newAuthApp(e *Engine) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(e.Middleware())
	app.Get("/me", e.HandleMe)
	app.Post("/token", e.HandleTokenRequest)
	app.Post("/refresh", e.HandleRefresh)
	return app
}

// fiberJSON calls app.Test with an optional Bearer token and JSON body.
// Returns the HTTP status and decoded response body map.
func fiberJSON(t *testing.T, app *fiber.App, method, path, bearer, body string) (int, map[string]any) {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, path, bodyReader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := app.Test(httptestutil.WithHost(req), 5000)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// fiberJSONWithQuery is like fiberJSON but also sets an api_key query parameter.
func fiberJSONWithQuery(t *testing.T, app *fiber.App, method, path, apiKeyParam string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, path+"?api_key="+apiKeyParam, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := app.Test(httptestutil.WithHost(req), 5000)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// ---------------------------------------------------------------------------
// fakeAPIKeyStore — test double for apikeys.Store
// ---------------------------------------------------------------------------

type fakeAPIKeyStore struct {
	keys map[string]apikeys.APIKey // plaintext → APIKey
}

func (f *fakeAPIKeyStore) Create(_ context.Context, _ string, _ []string) (string, apikeys.APIKey, error) {
	return "", apikeys.APIKey{}, fmt.Errorf("not implemented")
}

func (f *fakeAPIKeyStore) Validate(_ context.Context, plaintext string) (apikeys.APIKey, error) {
	if k, ok := f.keys[plaintext]; ok {
		return k, nil
	}
	return apikeys.APIKey{}, apikeys.ErrInvalidKey
}

func (f *fakeAPIKeyStore) Revoke(_ context.Context, _ string) error {
	return fmt.Errorf("not implemented")
}

func (f *fakeAPIKeyStore) List(_ context.Context, _ bool) ([]apikeys.APIKey, error) {
	return nil, nil
}

func (f *fakeAPIKeyStore) Close() error { return nil }

// ---------------------------------------------------------------------------
// Group 1: Issuer / JWT lifecycle
// ---------------------------------------------------------------------------

// TestIssuerIssueAndVerify covers the happy path: issue a token pair, verify
// the access token, and confirm the claims match the input identity.
func TestIssuerIssueAndVerify(t *testing.T) {
	iss := newTestIssuer(t, 15*time.Minute, 7*24*time.Hour)

	access, refresh, expiresIn, err := iss.Issue("alice", "alice@example.com", "admin")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if access == "" || refresh == "" {
		t.Fatal("Issue returned empty token(s)")
	}
	if expiresIn <= 0 {
		t.Fatalf("expiresIn = %d, want > 0", expiresIn)
	}

	cl, err := iss.VerifyAccess(access)
	if err != nil {
		t.Fatalf("VerifyAccess: %v", err)
	}
	if cl.Subject != "alice" {
		t.Errorf("Subject = %q, want alice", cl.Subject)
	}
	if cl.Email != "alice@example.com" {
		t.Errorf("Email = %q, want alice@example.com", cl.Email)
	}
	if cl.Role != "admin" {
		t.Errorf("Role = %q, want admin", cl.Role)
	}
	if cl.Kind != "access" {
		t.Errorf("Kind = %q, want access", cl.Kind)
	}
	if cl.Issuer != "soulacy" {
		t.Errorf("Issuer = %q, want soulacy", cl.Issuer)
	}
}

// TestIssuerVerifyRejectsExpiredToken checks that an already-expired access
// token is rejected by VerifyAccess.
func TestIssuerVerifyRejectsExpiredToken(t *testing.T) {
	// Negative TTL → ExpiresAt is in the past at issue time.
	iss := newTestIssuer(t, -time.Second, time.Hour)

	access, _, _, err := iss.Issue("bob", "", "viewer")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	_, err = iss.VerifyAccess(access)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
}

// TestIssuerVerifyRejectsWrongSecret confirms that a token signed with a
// different key is rejected even if it parses as structurally valid JWT.
func TestIssuerVerifyRejectsWrongSecret(t *testing.T) {
	issuerA := newTestIssuer(t, 15*time.Minute, time.Hour)
	issuerB := newTestIssuer(t, 15*time.Minute, time.Hour)

	// Issue from A, try to verify with B's different secret.
	issuerBDifferent, err := newIssuer("completely-different-secret", 15*time.Minute, time.Hour)
	if err != nil {
		t.Fatalf("newIssuer: %v", err)
	}
	defer issuerBDifferent.Close()

	access, _, _, err := issuerA.Issue("charlie", "", "operator")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	_, err = issuerBDifferent.VerifyAccess(access)
	if err == nil {
		t.Fatal("expected error for wrong-secret token, got nil")
	}

	// Sanity check: B can verify its own token.
	accessB, _, _, err := issuerB.Issue("dave", "", "operator")
	if err != nil {
		t.Fatalf("Issue B: %v", err)
	}
	if _, err := issuerB.VerifyAccess(accessB); err != nil {
		t.Fatalf("VerifyAccess own token: %v", err)
	}
}

// TestIssuerVerifyRejectsRefreshTokenAsAccess ensures the kind guard works:
// a refresh token (opaque string, not a JWT) is rejected by VerifyAccess.
func TestIssuerVerifyRejectsRefreshTokenAsAccess(t *testing.T) {
	iss := newTestIssuer(t, 15*time.Minute, time.Hour)
	_, refresh, _, err := iss.Issue("eve", "", "admin")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// The refresh token is an opaque hex string, not a JWT — VerifyAccess
	// should fail to parse it as a token.
	_, err = iss.VerifyAccess(refresh)
	if err == nil {
		t.Fatal("expected error when using refresh token as access token, got nil")
	}
}

// TestIssuerRefreshRotates verifies that each Refresh() call consumes the
// old token (single-use rotation) and issues a fresh pair.
func TestIssuerRefreshRotates(t *testing.T) {
	iss := newTestIssuer(t, 15*time.Minute, time.Hour)
	_, refresh, _, err := iss.Issue("frank", "frank@example.com", "admin")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// First use succeeds.
	newAccess, newRefresh, expiresIn, err := iss.Refresh(refresh)
	if err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	if newAccess == "" || newRefresh == "" || expiresIn <= 0 {
		t.Fatal("Refresh returned empty field(s)")
	}

	// Second use with the same old token must fail (token was rotated).
	_, _, _, err = iss.Refresh(refresh)
	if err == nil {
		t.Fatal("expected error on second use of refresh token, got nil")
	}

	// The newly issued access token must be valid and carry the same identity.
	cl, err := iss.VerifyAccess(newAccess)
	if err != nil {
		t.Fatalf("VerifyAccess new token: %v", err)
	}
	if cl.Subject != "frank" {
		t.Errorf("Subject after refresh = %q, want frank", cl.Subject)
	}
}

// TestIssuerRefreshInvalidToken confirms that an unknown opaque string is
// rejected by Refresh.
func TestIssuerRefreshInvalidToken(t *testing.T) {
	iss := newTestIssuer(t, 15*time.Minute, time.Hour)
	_, _, _, err := iss.Refresh("not-a-valid-refresh-token")
	if err == nil {
		t.Fatal("expected error for invalid refresh token, got nil")
	}
}

// ---------------------------------------------------------------------------
// Group 2: secretEqual
// ---------------------------------------------------------------------------

func TestSecretEqual(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
		eq   bool
	}{
		{"equal", "secret", "secret", true},
		{"wrong", "wrong", "secret", false},
		{"empty got", "", "secret", false},
		{"empty want", "secret", "", false},
		{"both empty", "", "", false}, // empty want always returns false
		{"prefix only", "secre", "secret", false},
		{"case sensitive", "Secret", "secret", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := secretEqual(tc.got, tc.want)
			if got != tc.eq {
				t.Errorf("secretEqual(%q, %q) = %v, want %v", tc.got, tc.want, got, tc.eq)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Group 3: Engine.Middleware via Fiber app.Test
// ---------------------------------------------------------------------------

// TestMiddlewareNoCredentialsFailsClosed verifies an allocated engine without
// a usable verifier cannot silently make the gateway public.
func TestMiddlewareNoCredentialsFailsClosed(t *testing.T) {
	e := newTestEngine(t, "apikey", "") // apikey mode, no static key
	app := newAuthApp(e)

	status, body := fiberJSON(t, app, http.MethodGet, "/me", "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("credential-free /me status = %d body = %v", status, body)
	}
}

func TestEffectiveTracksUsableVerifier(t *testing.T) {
	without := newTestEngine(t, "apikey", "")
	if without.Effective() {
		t.Fatal("allocated engine without credentials reported effective")
	}
	withKey := newTestEngine(t, "apikey", "static-key")
	if !withKey.Effective() {
		t.Fatal("static-key engine did not report effective")
	}
	withIssuer := newTestEngine(t, "jwt", "")
	if !withIssuer.Effective() {
		t.Fatal("working local JWT issuer did not report effective")
	}
}

func TestOIDCDiscoveryFailureFatalWhenOnlyAuthMethod(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer issuer.Close()
	if _, err := New(Config{Mode: "apikey", OIDCIssuer: issuer.URL}, "", zap.NewNop()); err == nil {
		t.Fatal("OIDC-only discovery failure did not abort auth construction")
	}

	// A usable static verifier may keep serving while the optional OIDC path is
	// unavailable; the engine must still truthfully report effective.
	e, err := New(Config{Mode: "apikey", OIDCIssuer: issuer.URL}, "fallback-key", zap.NewNop())
	if err != nil {
		t.Fatalf("static fallback should survive OIDC outage: %v", err)
	}
	defer e.Close()
	if !e.Effective() {
		t.Fatal("static fallback not effective")
	}
}

// TestMiddlewareStaticKeyCorrectAndWrong verifies Bearer token auth:
// correct key → 200, wrong key → 401, no key → 401.
func TestMiddlewareStaticKeyCorrectAndWrong(t *testing.T) {
	e := newTestEngine(t, "apikey", "my-static-key")
	app := newAuthApp(e)

	// Correct key.
	status, _ := fiberJSON(t, app, http.MethodGet, "/me", "my-static-key", "")
	if status != http.StatusOK {
		t.Fatalf("correct key: status = %d, want 200", status)
	}

	// Wrong key.
	status, _ = fiberJSON(t, app, http.MethodGet, "/me", "wrong-key", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong key: status = %d, want 401", status)
	}

	// No key.
	status, _ = fiberJSON(t, app, http.MethodGet, "/me", "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("no key: status = %d, want 401", status)
	}
}

// TestMiddlewareQueryParamFallback verifies ?api_key= as the WebSocket-friendly
// fallback for endpoints that cannot set headers.
func TestMiddlewareQueryParamFallback(t *testing.T) {
	e := newTestEngine(t, "apikey", "ws-key")
	app := newAuthApp(e)

	// Correct key via query param.
	status, _ := fiberJSONWithQuery(t, app, http.MethodGet, "/me", "ws-key")
	if status != http.StatusOK {
		t.Fatalf("query param correct: status = %d, want 200", status)
	}

	// Wrong key via query param.
	status, _ = fiberJSONWithQuery(t, app, http.MethodGet, "/me", "bad")
	if status != http.StatusUnauthorized {
		t.Fatalf("query param wrong: status = %d, want 401", status)
	}
}

// TestMiddlewareJWTModeAcceptsLocalToken verifies that in jwt mode a locally
// issued access JWT passes the middleware and the static key still works as
// a fallback.
func TestMiddlewareJWTModeAcceptsLocalToken(t *testing.T) {
	e := newTestEngine(t, "jwt", "fallback-key")
	app := newAuthApp(e)

	// Static key still works in JWT mode.
	status, _ := fiberJSON(t, app, http.MethodGet, "/me", "fallback-key", "")
	if status != http.StatusOK {
		t.Fatalf("jwt mode static key: status = %d, want 200", status)
	}

	// Obtain a local JWT via the token endpoint (bearer required because the
	// global middleware also guards /token; the handler's own api_key check
	// provides the bootstrap security).
	tokenBody := `{"api_key":"fallback-key"}`
	status, body := fiberJSON(t, app, http.MethodPost, "/token", "fallback-key", tokenBody)
	if status != http.StatusOK {
		t.Fatalf("token endpoint: status = %d body = %v", status, body)
	}
	accessToken, _ := body["access_token"].(string)
	if accessToken == "" {
		t.Fatalf("token endpoint returned no access_token: %v", body)
	}

	// Use the local JWT to hit the guarded endpoint.
	status, _ = fiberJSON(t, app, http.MethodGet, "/me", accessToken, "")
	if status != http.StatusOK {
		t.Fatalf("local JWT: status = %d, want 200", status)
	}

	// Browser sessions authenticate with the HttpOnly access cookie and send
	// no JavaScript-readable Authorization header after a relaunch.
	cookieReq, err := http.NewRequest(http.MethodGet, "/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	cookieReq.AddCookie(&http.Cookie{Name: "soulacy_access", Value: accessToken})
	cookieResp, err := app.Test(httptestutil.WithHost(cookieReq), 5000)
	if err != nil {
		t.Fatal(err)
	}
	_ = cookieResp.Body.Close()
	if cookieResp.StatusCode != http.StatusOK {
		t.Fatalf("access cookie: status = %d, want 200", cookieResp.StatusCode)
	}

	// Tampered token must fail.
	status, _ = fiberJSON(t, app, http.MethodGet, "/me", accessToken+"tampered", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("tampered JWT: status = %d, want 401", status)
	}
}

// TestMiddlewareManagedAPIKey verifies that a sk_ prefixed key is validated
// against the configured apiKeyStore, and that an unregistered sk_ key is
// rejected.
func TestMiddlewareManagedAPIKey(t *testing.T) {
	e := newTestEngine(t, "apikey", "master-key")
	fakeKey := "sk_test0001"
	store := &fakeAPIKeyStore{
		keys: map[string]apikeys.APIKey{
			fakeKey: {ID: "key-id-1", Name: "CI Bot"},
		},
	}
	e.SetAPIKeyStore(store)
	app := newAuthApp(e)

	// Known managed key.
	status, _ := fiberJSON(t, app, http.MethodGet, "/me", fakeKey, "")
	if status != http.StatusOK {
		t.Fatalf("managed key valid: status = %d, want 200", status)
	}

	// Unknown managed key (not in the store).
	status, _ = fiberJSON(t, app, http.MethodGet, "/me", "sk_unknown9999", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("managed key unknown: status = %d, want 401", status)
	}
}

// ---------------------------------------------------------------------------
// Group 4: Engine handler integration via Fiber app.Test
// ---------------------------------------------------------------------------

// TestHandleTokenRequestAPIKeyModeReturns404 verifies the endpoint says
// "JWT mode not enabled" in apikey mode.
func TestHandleTokenRequestAPIKeyModeReturns404(t *testing.T) {
	e := newTestEngine(t, "apikey", "key")
	app := newAuthApp(e)

	status, body := fiberJSON(t, app, http.MethodPost, "/token", "key", `{"api_key":"key"}`)
	if status != http.StatusNotFound {
		t.Fatalf("apikey mode /token: status = %d body = %v", status, body)
	}
}

// TestHandleTokenRequestJWTModeWrongKey rejects requests that supply a key
// that doesn't match the static key.
func TestHandleTokenRequestJWTModeWrongKey(t *testing.T) {
	e := newTestEngine(t, "jwt", "right-key")
	app := newAuthApp(e)

	status, body := fiberJSON(t, app, http.MethodPost, "/token", "right-key", `{"api_key":"wrong-key"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong key /token: status = %d body = %v", status, body)
	}
}

// TestHandleTokenRequestJWTModeSuccess issues tokens in JWT mode and verifies
// the response shape.
func TestHandleTokenRequestJWTModeSuccess(t *testing.T) {
	e := newTestEngine(t, "jwt", "real-key")
	app := newAuthApp(e)

	status, body := fiberJSON(t, app, http.MethodPost, "/token", "real-key", `{"api_key":"real-key"}`)
	if status != http.StatusOK {
		t.Fatalf("/token success: status = %d body = %v", status, body)
	}
	if body["access_token"] == "" {
		t.Errorf("missing access_token in response: %v", body)
	}
	if body["refresh_token"] == "" {
		t.Errorf("missing refresh_token in response: %v", body)
	}
	if body["token_type"] != "Bearer" {
		t.Errorf("token_type = %v, want Bearer", body["token_type"])
	}
	if body["expires_in"] == nil {
		t.Errorf("missing expires_in in response: %v", body)
	}
}

func TestHandleTokenRequestJWTModeSetsPersistentBrowserCookies(t *testing.T) {
	e := newTestEngine(t, "jwt", "real-key")
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/token", e.HandleTokenRequest)

	req, err := http.NewRequest(http.MethodPost, "/token", strings.NewReader(`{"api_key":"real-key"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(httptestutil.WithHost(req), 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	cookies := map[string]*http.Cookie{}
	for _, cookie := range resp.Cookies() {
		cookies[cookie.Name] = cookie
	}
	for _, name := range []string{"soulacy_access", "soulacy_refresh"} {
		cookie := cookies[name]
		if cookie == nil {
			t.Fatalf("missing %s cookie", name)
		}
		if !cookie.HttpOnly || cookie.MaxAge <= 0 {
			t.Fatalf("%s cookie must be HttpOnly and persistent: %#v", name, cookie)
		}
	}
	if cookies["soulacy_access"].Path != "/" {
		t.Fatalf("access cookie path = %q, want /", cookies["soulacy_access"].Path)
	}
	if cookies["soulacy_refresh"].Path != "/api/v1/auth" {
		t.Fatalf("refresh cookie path = %q", cookies["soulacy_refresh"].Path)
	}
}

// TestHandleRefreshInvalidToken returns 401 for unknown refresh tokens.
func TestHandleRefreshInvalidToken(t *testing.T) {
	e := newTestEngine(t, "jwt", "key")
	app := newAuthApp(e)

	status, _ := fiberJSON(t, app, http.MethodPost, "/refresh", "key", `{"refresh_token":"not-valid"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("invalid refresh: status = %d, want 401", status)
	}
}

// TestHandleRefreshSuccess obtains a token pair via /token, then exchanges the
// refresh token via /refresh and verifies a new access token is returned.
func TestHandleRefreshSuccess(t *testing.T) {
	e := newTestEngine(t, "jwt", "key")
	app := newAuthApp(e)

	// Get initial tokens.
	status, body := fiberJSON(t, app, http.MethodPost, "/token", "key", `{"api_key":"key"}`)
	if status != http.StatusOK {
		t.Fatalf("/token: status = %d body = %v", status, body)
	}
	refreshToken, _ := body["refresh_token"].(string)

	// Exchange refresh token.
	refreshBody := fmt.Sprintf(`{"refresh_token":%q}`, refreshToken)
	status, body = fiberJSON(t, app, http.MethodPost, "/refresh", "key", refreshBody)
	if status != http.StatusOK {
		t.Fatalf("/refresh: status = %d body = %v", status, body)
	}
	if body["access_token"] == "" {
		t.Errorf("refresh response missing access_token: %v", body)
	}

	// Second use of the same refresh token must fail (single-use rotation).
	status, _ = fiberJSON(t, app, http.MethodPost, "/refresh", "key", refreshBody)
	if status != http.StatusUnauthorized {
		t.Fatalf("second refresh: status = %d, want 401", status)
	}
}

func TestHandleRefreshUsesAndRotatesBrowserCookie(t *testing.T) {
	e := newTestEngine(t, "jwt", "key")
	_, refresh, _, err := e.issuer.Issue("admin", "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/refresh", e.HandleRefresh)
	req, err := http.NewRequest(http.MethodPost, "/refresh", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "soulacy_refresh", Value: refresh})
	resp, err := app.Test(httptestutil.WithHost(req), 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cookie refresh: status = %d", resp.StatusCode)
	}
	rotated := ""
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "soulacy_refresh" {
			rotated = cookie.Value
		}
	}
	if rotated == "" || rotated == refresh {
		t.Fatal("refresh cookie was not rotated")
	}
}

// TestHandleMeAPIKeyMode verifies /me returns minimal info in apikey mode.
func TestHandleMeAPIKeyMode(t *testing.T) {
	e := newTestEngine(t, "apikey", "key")
	app := newAuthApp(e)

	status, body := fiberJSON(t, app, http.MethodGet, "/me", "key", "")
	if status != http.StatusOK {
		t.Fatalf("/me apikey: status = %d body = %v", status, body)
	}
	if body["mode"] != "apikey" {
		t.Errorf("mode = %v, want apikey", body["mode"])
	}
	if body["role"] != "admin" {
		t.Errorf("role = %v, want admin", body["role"])
	}
}

// TestHandleMeJWTMode verifies /me returns claims from a valid JWT.
func TestHandleMeJWTMode(t *testing.T) {
	e := newTestEngine(t, "jwt", "key")
	app := newAuthApp(e)

	// Obtain a token.
	_, tokenBody := fiberJSON(t, app, http.MethodPost, "/token", "key", `{"api_key":"key"}`)
	accessToken, _ := tokenBody["access_token"].(string)

	status, body := fiberJSON(t, app, http.MethodGet, "/me", accessToken, "")
	if status != http.StatusOK {
		t.Fatalf("/me jwt: status = %d body = %v", status, body)
	}
	if body["mode"] != "jwt" {
		t.Errorf("mode = %v, want jwt", body["mode"])
	}
	if body["sub"] == nil || body["sub"] == "" {
		t.Errorf("sub missing from /me jwt response: %v", body)
	}
}

// ---------------------------------------------------------------------------
// Group 5: JWKS key parsers (pure math, no network)
// ---------------------------------------------------------------------------

// TestParseRSAPublicKeyRoundtrip generates a real RSA key, encodes it to
// base64url JWKS fields, decodes it back, and verifies the key matches.
func TestParseRSAPublicKeyRoundtrip(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	pub := &privKey.PublicKey

	nEncoded := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	eEncoded := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())

	parsed, err := parseRSAPublicKey(nEncoded, eEncoded)
	if err != nil {
		t.Fatalf("parseRSAPublicKey: %v", err)
	}
	if parsed.N.Cmp(pub.N) != 0 {
		t.Error("parsed N does not match original")
	}
	if parsed.E != pub.E {
		t.Errorf("parsed E = %d, want %d", parsed.E, pub.E)
	}
}

// TestParseRSAPublicKeyErrors covers invalid base64url inputs.
func TestParseRSAPublicKeyErrors(t *testing.T) {
	// Invalid base64 for n.
	if _, err := parseRSAPublicKey("!!invalid!!", "AQAB"); err == nil {
		t.Error("expected error for invalid n base64, got nil")
	}
	// Invalid base64 for e.
	if _, err := parseRSAPublicKey("AAAA", "!!invalid!!"); err == nil {
		t.Error("expected error for invalid e base64, got nil")
	}
	// Zero exponent (decoded 0x00 bytes).
	if _, err := parseRSAPublicKey("AAAA", "AA"); err == nil {
		t.Error("expected error for zero exponent, got nil")
	}
}

// TestParseECPublicKeyRoundtrip generates a P-256 key, encodes it, and
// verifies round-trip through parseECPublicKey.
func TestParseECPublicKeyRoundtrip(t *testing.T) {
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate EC key: %v", err)
	}
	pub := &privKey.PublicKey

	xEncoded := base64.RawURLEncoding.EncodeToString(pub.X.Bytes())
	yEncoded := base64.RawURLEncoding.EncodeToString(pub.Y.Bytes())

	parsed, err := parseECPublicKey("P-256", xEncoded, yEncoded)
	if err != nil {
		t.Fatalf("parseECPublicKey: %v", err)
	}
	if parsed.X.Cmp(pub.X) != 0 || parsed.Y.Cmp(pub.Y) != 0 {
		t.Error("parsed EC key coordinates do not match original")
	}
	if parsed.Curve != elliptic.P256() {
		t.Error("parsed curve is not P-256")
	}
}

// TestParseECPublicKeyUnsupportedCurve verifies that unknown curves are rejected.
func TestParseECPublicKeyUnsupportedCurve(t *testing.T) {
	if _, err := parseECPublicKey("P-999", "AAAA", "AAAA"); err == nil {
		t.Error("expected error for unsupported curve, got nil")
	}
}

// TestParseECPublicKeyInvalidBase64 covers invalid coordinate encoding.
func TestParseECPublicKeyInvalidBase64(t *testing.T) {
	if _, err := parseECPublicKey("P-256", "!!bad!!", "AAAA"); err == nil {
		t.Error("expected error for invalid x base64, got nil")
	}
	if _, err := parseECPublicKey("P-256", "AAAA", "!!bad!!"); err == nil {
		t.Error("expected error for invalid y base64, got nil")
	}
}

// ---------------------------------------------------------------------------
// Group 6: Config defaults
// ---------------------------------------------------------------------------

// TestConfigApplyDefaults verifies that applyDefaults fills in all zero fields.
func TestConfigApplyDefaults(t *testing.T) {
	cfg := Config{}
	cfg.applyDefaults()

	if cfg.Mode != "apikey" {
		t.Errorf("default Mode = %q, want apikey", cfg.Mode)
	}
	if cfg.JWTAccessTTL != 15*time.Minute {
		t.Errorf("default JWTAccessTTL = %v, want 15m", cfg.JWTAccessTTL)
	}
	if cfg.JWTRefreshTTL != 7*24*time.Hour {
		t.Errorf("default JWTRefreshTTL = %v, want 168h", cfg.JWTRefreshTTL)
	}

	// OIDCAudience should be copied from OIDCClientID when empty.
	cfg2 := Config{OIDCClientID: "my-client"}
	cfg2.applyDefaults()
	if cfg2.OIDCAudience != "my-client" {
		t.Errorf("OIDCAudience = %q, want my-client", cfg2.OIDCAudience)
	}

	// OIDCAudience explicitly set should not be overwritten.
	cfg3 := Config{OIDCClientID: "my-client", OIDCAudience: "my-audience"}
	cfg3.applyDefaults()
	if cfg3.OIDCAudience != "my-audience" {
		t.Errorf("OIDCAudience = %q, should not be overwritten", cfg3.OIDCAudience)
	}
}

// Compile-time guard: fakeAPIKeyStore must satisfy apikeys.Store.
var _ apikeys.Store = (*fakeAPIKeyStore)(nil)

// ---------------------------------------------------------------------------
// Group 7: Engine.Mode helper
// ---------------------------------------------------------------------------

func TestEngineMode(t *testing.T) {
	for _, mode := range []string{"apikey", "jwt"} {
		e := newTestEngine(t, mode, "k")
		if got := e.Mode(); got != mode {
			t.Errorf("Mode() = %q, want %q", got, mode)
		}
	}
}

// ---------------------------------------------------------------------------
// Group 8: SetClaims / ClaimsFromCtx via Fiber context
// ---------------------------------------------------------------------------

// TestSetAndGetClaims verifies that claims written by SetClaims are
// correctly returned by ClaimsFromCtx on the same request context.
func TestSetAndGetClaims(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	want := &Claims{Email: "test@example.com", Role: "admin", Kind: "access"}
	app.Get("/claims-test", func(c *fiber.Ctx) error {
		SetClaims(c, want)
		got := ClaimsFromCtx(c)
		if got == nil {
			return c.Status(http.StatusInternalServerError).SendString("nil claims")
		}
		if got.Email != want.Email || got.Role != want.Role {
			return c.Status(http.StatusInternalServerError).
				SendString("claims mismatch")
		}
		return c.SendString("ok")
	})

	req, _ := http.NewRequest(http.MethodGet, "/claims-test", nil)
	resp, err := app.Test(httptestutil.WithHost(req), 1000)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
}

// TestClaimsFromCtxNilWhenAbsent confirms that ClaimsFromCtx returns nil
// when no claims have been stored (open/unauthenticated path).
func TestClaimsFromCtxNilWhenAbsent(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/no-claims", func(c *fiber.Ctx) error {
		cl := ClaimsFromCtx(c)
		if cl != nil {
			return c.Status(http.StatusInternalServerError).SendString("expected nil")
		}
		return c.SendString("ok")
	})

	req, _ := http.NewRequest(http.MethodGet, "/no-claims", nil)
	resp, err := app.Test(httptestutil.WithHost(req), 1000)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
}

// ---------------------------------------------------------------------------
// Group 9: newIssuer with empty secret (ephemeral key path)
// ---------------------------------------------------------------------------

// TestNewIssuerEphemeralSecret verifies that passing an empty JWTSecret
// causes newIssuer to generate a random key and that the resulting issuer
// can sign and verify tokens.
func TestNewIssuerEphemeralSecret(t *testing.T) {
	iss, err := newIssuer("", 15*time.Minute, time.Hour)
	if err != nil {
		t.Fatalf("newIssuer with empty secret: %v", err)
	}
	defer iss.Close()

	access, _, _, err := iss.Issue("zoe", "zoe@example.com", "viewer")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	cl, err := iss.VerifyAccess(access)
	if err != nil {
		t.Fatalf("VerifyAccess: %v", err)
	}
	if cl.Subject != "zoe" {
		t.Errorf("Subject = %q, want zoe", cl.Subject)
	}
}

// ---------------------------------------------------------------------------
// Group 10: HandleRefresh edge cases — missing body / empty refresh_token
// ---------------------------------------------------------------------------

// TestHandleRefreshMissingTokenField verifies that a request body that
// omits refresh_token returns 400.
func TestHandleRefreshMissingTokenField(t *testing.T) {
	e := newTestEngine(t, "jwt", "key")
	app := newAuthApp(e)

	status, body := fiberJSON(t, app, http.MethodPost, "/refresh", "key", `{}`)
	if status != http.StatusBadRequest {
		t.Fatalf("missing refresh_token: status = %d body = %v", status, body)
	}
}

// TestHandleRefreshAPIKeyModeReturns404 verifies that /refresh in apikey
// mode returns 404 (JWT issuer not enabled).
func TestHandleRefreshAPIKeyModeReturns404(t *testing.T) {
	e := newTestEngine(t, "apikey", "key")
	app := newAuthApp(e)

	status, _ := fiberJSON(t, app, http.MethodPost, "/refresh", "key", `{"refresh_token":"anything"}`)
	if status != http.StatusNotFound {
		t.Fatalf("apikey mode /refresh: status = %d, want 404", status)
	}
}

// ---------------------------------------------------------------------------
// Group 11: OIDCValidator via fake http.RoundTripper
// ---------------------------------------------------------------------------

// fakeRoundTripper returns canned responses for OIDC discovery + JWKS
// endpoints so we can test OIDCValidator without any real network calls.
type fakeRoundTripper struct {
	responses map[string]fakeResponse
}

type fakeResponse struct {
	status int
	body   string
}

func (f *fakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.URL.String()
	r, ok := f.responses[key]
	if !ok {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(bytes.NewBufferString(`not found`)),
			Header:     make(http.Header),
		}, nil
	}
	return &http.Response{
		StatusCode: r.status,
		Body:       io.NopCloser(bytes.NewBufferString(r.body)),
		Header:     make(http.Header),
	}, nil
}

// buildOIDCValidator constructs an OIDCValidator with a fake transport.
// It wires the discovery doc to point at jwksURL and returns canned JWKS JSON.
func buildOIDCValidator(t *testing.T, issuer, audience, jwksURL, jwksJSON string) (*OIDCValidator, error) {
	t.Helper()
	discoveryURL := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	discoveryBody := fmt.Sprintf(`{"jwks_uri":%q}`, jwksURL)

	transport := &fakeRoundTripper{
		responses: map[string]fakeResponse{
			discoveryURL: {http.StatusOK, discoveryBody},
			jwksURL:      {http.StatusOK, jwksJSON},
		},
	}

	v := &OIDCValidator{
		issuer:   issuer,
		audience: audience,
		client:   &http.Client{Transport: transport},
		keys:     make(map[string]any),
		quit:     make(chan struct{}),
	}
	if err := v.discover(); err != nil {
		return nil, err
	}
	return v, nil
}

// TestOIDCValidatorDiscoverAndFetchJWKS checks that buildOIDCValidator
// successfully loads an RSA key from a canned JWKS response.
func TestOIDCValidatorDiscoverAndFetchJWKS(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	pub := &privKey.PublicKey
	nEncoded := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	eEncoded := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())

	jwksJSON := fmt.Sprintf(`{"keys":[{"kty":"RSA","kid":"key1","use":"sig","n":%q,"e":%q}]}`,
		nEncoded, eEncoded)

	v, err := buildOIDCValidator(t, "https://issuer.example.com", "my-audience",
		"https://issuer.example.com/jwks", jwksJSON)
	if err != nil {
		t.Fatalf("buildOIDCValidator: %v", err)
	}
	defer v.close()

	// Verify the key was stored under kid "key1".
	v.mu.RLock()
	_, ok := v.keys["key1"]
	v.mu.RUnlock()
	if !ok {
		t.Error("expected key1 in JWKS cache, not found")
	}
}

// TestOIDCValidatorSkipsEncryptionKeys verifies that keys with use="enc"
// are not loaded into the signing-key cache.
func TestOIDCValidatorSkipsEncryptionKeys(t *testing.T) {
	privKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	pub := &privKey.PublicKey
	nEncoded := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	eEncoded := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())

	// use="enc" — should be skipped; use="sig" — should be kept.
	jwksJSON := fmt.Sprintf(`{"keys":[
		{"kty":"RSA","kid":"enc-key","use":"enc","n":%q,"e":%q},
		{"kty":"RSA","kid":"sig-key","use":"sig","n":%q,"e":%q}
	]}`, nEncoded, eEncoded, nEncoded, eEncoded)

	v, err := buildOIDCValidator(t, "https://issuer.example.com", "",
		"https://issuer.example.com/jwks", jwksJSON)
	if err != nil {
		t.Fatalf("buildOIDCValidator: %v", err)
	}
	defer v.close()

	v.mu.RLock()
	_, hasEnc := v.keys["enc-key"]
	_, hasSig := v.keys["sig-key"]
	v.mu.RUnlock()

	if hasEnc {
		t.Error("enc-key should be skipped (use=enc)")
	}
	if !hasSig {
		t.Error("sig-key should be loaded (use=sig)")
	}
}

// TestOIDCValidatorFetchJWKSHTTPError verifies that a non-200 JWKS response
// causes fetchJWKS to return an error.
func TestOIDCValidatorFetchJWKSHTTPError(t *testing.T) {
	transport := &fakeRoundTripper{
		responses: map[string]fakeResponse{
			"https://issuer.example.com/.well-known/openid-configuration": {
				http.StatusOK, `{"jwks_uri":"https://issuer.example.com/jwks"}`,
			},
			"https://issuer.example.com/jwks": {http.StatusInternalServerError, `error`},
		},
	}
	v := &OIDCValidator{
		issuer:   "https://issuer.example.com",
		audience: "",
		client:   &http.Client{Transport: transport},
		keys:     make(map[string]any),
		quit:     make(chan struct{}),
	}
	if err := v.discover(); err == nil {
		t.Error("expected error for non-200 JWKS response, got nil")
	}
}

// TestOIDCValidatorDiscoveryHTTPError verifies that a non-200 discovery
// document causes discover() to return an error.
func TestOIDCValidatorDiscoveryHTTPError(t *testing.T) {
	transport := &fakeRoundTripper{
		responses: map[string]fakeResponse{
			"https://issuer.example.com/.well-known/openid-configuration": {
				http.StatusForbidden, `forbidden`,
			},
		},
	}
	v := &OIDCValidator{
		issuer:   "https://issuer.example.com",
		audience: "",
		client:   &http.Client{Transport: transport},
		keys:     make(map[string]any),
		quit:     make(chan struct{}),
	}
	if err := v.discover(); err == nil {
		t.Error("expected error for non-200 discovery response, got nil")
	}
}

// TestOIDCValidatorDiscoveryMissingJWKSURI verifies that a discovery
// document missing jwks_uri returns an error.
func TestOIDCValidatorDiscoveryMissingJWKSURI(t *testing.T) {
	transport := &fakeRoundTripper{
		responses: map[string]fakeResponse{
			"https://issuer.example.com/.well-known/openid-configuration": {
				http.StatusOK, `{"issuer":"https://issuer.example.com"}`,
			},
		},
	}
	v := &OIDCValidator{
		issuer:   "https://issuer.example.com",
		audience: "",
		client:   &http.Client{Transport: transport},
		keys:     make(map[string]any),
		quit:     make(chan struct{}),
	}
	if err := v.discover(); err == nil {
		t.Error("expected error for missing jwks_uri, got nil")
	}
}

// TestOIDCValidatorECKeyLoaded verifies that EC keys (P-384) in the JWKS
// are parsed and loaded into the key cache.
func TestOIDCValidatorECKeyLoaded(t *testing.T) {
	privKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate EC key: %v", err)
	}
	pub := &privKey.PublicKey
	xEncoded := base64.RawURLEncoding.EncodeToString(pub.X.Bytes())
	yEncoded := base64.RawURLEncoding.EncodeToString(pub.Y.Bytes())

	jwksJSON := fmt.Sprintf(`{"keys":[{"kty":"EC","kid":"ec1","use":"sig","crv":"P-384","x":%q,"y":%q}]}`,
		xEncoded, yEncoded)

	v, err := buildOIDCValidator(t, "https://issuer.example.com", "",
		"https://issuer.example.com/jwks", jwksJSON)
	if err != nil {
		t.Fatalf("buildOIDCValidator: %v", err)
	}
	defer v.close()

	v.mu.RLock()
	_, ok := v.keys["ec1"]
	v.mu.RUnlock()
	if !ok {
		t.Error("expected ec1 EC key in JWKS cache")
	}
}

// TestOIDCValidatorMalformedRSAKeySkipped verifies that a malformed RSA
// key (bad base64 in n) is silently skipped during JWKS loading rather
// than causing a hard error.
func TestOIDCValidatorMalformedRSAKeySkipped(t *testing.T) {
	// good key alongside a bad one — bad one should be skipped, good loaded.
	privKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	pub := &privKey.PublicKey
	nEncoded := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	eEncoded := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())

	jwksJSON := fmt.Sprintf(`{"keys":[
		{"kty":"RSA","kid":"bad","use":"sig","n":"!!!not-base64!!!","e":"AQAB"},
		{"kty":"RSA","kid":"good","use":"sig","n":%q,"e":%q}
	]}`, nEncoded, eEncoded)

	v, err := buildOIDCValidator(t, "https://issuer.example.com", "",
		"https://issuer.example.com/jwks", jwksJSON)
	if err != nil {
		t.Fatalf("buildOIDCValidator: %v", err)
	}
	defer v.close()

	v.mu.RLock()
	_, hasBad := v.keys["bad"]
	_, hasGood := v.keys["good"]
	v.mu.RUnlock()

	if hasBad {
		t.Error("malformed RSA key should be skipped")
	}
	if !hasGood {
		t.Error("valid RSA key alongside malformed one should still be loaded")
	}
}

// TestOIDCValidatorKeyFuncUnknownKID verifies that presenting a token
// signed with an unknown kid returns an error from Validate (the keyFunc
// triggers a re-fetch attempt and ultimately returns "unknown kid").
func TestOIDCValidatorKeyFuncUnknownKID(t *testing.T) {
	privKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	pub := &privKey.PublicKey
	nEncoded := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	eEncoded := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())

	// JWKS contains key "known-kid", but we'll sign a token with "unknown-kid".
	jwksJSON := fmt.Sprintf(`{"keys":[{"kty":"RSA","kid":"known-kid","use":"sig","n":%q,"e":%q}]}`,
		nEncoded, eEncoded)

	v, err := buildOIDCValidator(t, "https://issuer.example.com", "",
		"https://issuer.example.com/jwks", jwksJSON)
	if err != nil {
		t.Fatalf("buildOIDCValidator: %v", err)
	}
	defer v.close()

	// Build a real JWT signed with privKey, but tag it with kid="unknown-kid".
	cl := &Claims{}
	cl.Subject = "testuser"
	cl.Issuer = "https://issuer.example.com"
	cl.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, cl)
	tok.Header["kid"] = "unknown-kid"
	signed, err := tok.SignedString(privKey)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	_, err = v.Validate(signed)
	if err == nil {
		t.Error("expected error for unknown kid, got nil")
	}
}

// TestOIDCValidatorFetchJWKSInvalidJSON verifies that malformed JWKS JSON
// causes fetchJWKS to return an error.
func TestOIDCValidatorFetchJWKSInvalidJSON(t *testing.T) {
	transport := &fakeRoundTripper{
		responses: map[string]fakeResponse{
			"https://issuer.example.com/.well-known/openid-configuration": {
				http.StatusOK, `{"jwks_uri":"https://issuer.example.com/jwks"}`,
			},
			"https://issuer.example.com/jwks": {http.StatusOK, `not-json`},
		},
	}
	v := &OIDCValidator{
		issuer:   "https://issuer.example.com",
		audience: "",
		client:   &http.Client{Transport: transport},
		keys:     make(map[string]any),
		quit:     make(chan struct{}),
	}
	if err := v.discover(); err == nil {
		t.Error("expected error for invalid JWKS JSON, got nil")
	}
}

// TestOIDCValidatorClose verifies that close() is idempotent (calling it
// twice does not panic).
func TestOIDCValidatorClose(t *testing.T) {
	privKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	pub := &privKey.PublicKey
	nEncoded := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	eEncoded := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	jwksJSON := fmt.Sprintf(`{"keys":[{"kty":"RSA","kid":"k1","use":"sig","n":%q,"e":%q}]}`,
		nEncoded, eEncoded)

	v, err := buildOIDCValidator(t, "https://issuer.example.com", "",
		"https://issuer.example.com/jwks", jwksJSON)
	if err != nil {
		t.Fatalf("buildOIDCValidator: %v", err)
	}

	// Should not panic on repeated close.
	v.close()
	v.close()
}

// ---------------------------------------------------------------------------
// Group 12: refreshStore expired-token path
// ---------------------------------------------------------------------------

// TestRefreshStoreExpiredToken verifies that get() rejects an entry whose
// expiresAt is in the past without panicking.
func TestRefreshStoreExpiredToken(t *testing.T) {
	s := newRefreshStore()
	defer s.close()

	// Manually insert an already-expired entry.
	tok := "expiredtoken"
	s.mu.Lock()
	s.tokens[tok] = refreshEntry{
		subject:   "ghost",
		email:     "",
		role:      "viewer",
		expiresAt: time.Now().Add(-time.Hour),
	}
	s.mu.Unlock()

	_, _, _, ok := s.get(tok)
	if ok {
		t.Error("expected expired token to be rejected, got ok=true")
	}
}

// TestRefreshStoreSingleUse verifies that a valid token can only be used once.
func TestRefreshStoreSingleUse(t *testing.T) {
	s := newRefreshStore()
	defer s.close()

	tok := s.put("alice", "alice@example.com", "admin", time.Now().Add(time.Hour))
	sub, email, role, ok := s.get(tok)
	if !ok {
		t.Fatal("first get should succeed")
	}
	if sub != "alice" || email != "alice@example.com" || role != "admin" {
		t.Errorf("unexpected values: sub=%q email=%q role=%q", sub, email, role)
	}

	// Second get must fail — token was rotated on first use.
	_, _, _, ok = s.get(tok)
	if ok {
		t.Error("second get should fail (single-use rotation)")
	}
}

// ---------------------------------------------------------------------------
// Group 13: ParseECPublicKey additional curves
// ---------------------------------------------------------------------------

// TestParseECPublicKeyP384Roundtrip verifies P-384 curve round-trip.
func TestParseECPublicKeyP384Roundtrip(t *testing.T) {
	privKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate P-384 key: %v", err)
	}
	pub := &privKey.PublicKey
	xEnc := base64.RawURLEncoding.EncodeToString(pub.X.Bytes())
	yEnc := base64.RawURLEncoding.EncodeToString(pub.Y.Bytes())

	parsed, err := parseECPublicKey("P-384", xEnc, yEnc)
	if err != nil {
		t.Fatalf("parseECPublicKey P-384: %v", err)
	}
	if parsed.Curve != elliptic.P384() {
		t.Error("curve is not P-384")
	}
	if parsed.X.Cmp(pub.X) != 0 || parsed.Y.Cmp(pub.Y) != 0 {
		t.Error("coordinate mismatch")
	}
}

// TestParseECPublicKeyP521Roundtrip verifies P-521 curve round-trip.
func TestParseECPublicKeyP521Roundtrip(t *testing.T) {
	privKey, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	if err != nil {
		t.Fatalf("generate P-521 key: %v", err)
	}
	pub := &privKey.PublicKey
	xEnc := base64.RawURLEncoding.EncodeToString(pub.X.Bytes())
	yEnc := base64.RawURLEncoding.EncodeToString(pub.Y.Bytes())

	parsed, err := parseECPublicKey("P-521", xEnc, yEnc)
	if err != nil {
		t.Fatalf("parseECPublicKey P-521: %v", err)
	}
	if parsed.Curve != elliptic.P521() {
		t.Error("curve is not P-521")
	}
}
