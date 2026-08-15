package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
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
