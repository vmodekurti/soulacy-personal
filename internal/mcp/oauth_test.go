package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOAuthDiscoveryRegistrationAndExchange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := "http://" + r.Host
		switch r.URL.Path {
		case "/resource":
			_ = json.NewEncoder(w).Encode(ProtectedResourceMetadata{Resource: origin + "/mcp", AuthorizationServers: []string{origin}, ScopesSupported: []string{"read", "install"}})
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(AuthorizationServerMetadata{Issuer: origin, AuthorizationEndpoint: origin + "/authorize", TokenEndpoint: origin + "/token", RegistrationEndpoint: origin + "/register", CodeChallengeMethodsSupported: []string{"S256"}})
		case "/register":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"token_endpoint_auth_method":"none"`) || !strings.Contains(string(body), `"redirect_uris":["http://localhost/callback"]`) {
				t.Fatalf("unexpected registration: %s", body)
			}
			_ = json.NewEncoder(w).Encode(OAuthClientRegistration{ClientID: "soulacy-client"})
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code_verifier") != "verifier" || r.Form.Get("resource") != origin+"/mcp" {
				t.Fatalf("unexpected token request: %v", r.Form)
			}
			_ = json.NewEncoder(w).Encode(oauthWireToken{AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer", ExpiresIn: 3600})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	resource, err := FetchProtectedResourceMetadata(ctx, server.Client(), server.URL+"/resource")
	if err != nil {
		t.Fatal(err)
	}
	auth, err := FetchAuthorizationServerMetadata(ctx, server.Client(), resource.AuthorizationServers[0])
	if err != nil {
		t.Fatal(err)
	}
	registration, err := RegisterOAuthClient(ctx, server.Client(), auth.RegistrationEndpoint, "http://localhost/callback", "Soulacy Personal")
	if err != nil || registration.ClientID != "soulacy-client" {
		t.Fatalf("registration = %+v, %v", registration, err)
	}
	authorize, err := AuthorizationURL(auth, registration.ClientID, "http://localhost/callback", "state", "verifier", resource.Resource, []string{"read", "install"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(authorize)
	if parsed.Query().Get("code_challenge_method") != "S256" || parsed.Query().Get("state") != "state" || parsed.Query().Get("scope") != "read install" || parsed.Query().Get("resource") != resource.Resource {
		t.Fatalf("authorization query = %v", parsed.Query())
	}
	tokens, err := ExchangeAuthorizationCode(ctx, server.Client(), auth.TokenEndpoint, "code", "verifier", registration.ClientID, "http://localhost/callback", resource.Resource)
	if err != nil || tokens.AccessToken != "access" || tokens.RefreshToken != "refresh" || tokens.ExpiresAt.Before(time.Now().Add(59*time.Minute)) {
		t.Fatalf("tokens = %+v, %v", tokens, err)
	}
}

func TestOAuthDiscoveryRejectsIssuerMismatchAndMissingPKCE(t *testing.T) {
	for _, test := range []struct {
		name   string
		issuer string
		pkce   []string
		want   string
	}{
		{name: "issuer mismatch", issuer: "https://attacker.example", pkce: []string{"S256"}, want: "issuer does not match"},
		{name: "missing PKCE", pkce: []string{"plain"}, want: "PKCE S256"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				localIssuer := "http://" + request.Host
				issuer := test.issuer
				if issuer == "" {
					issuer = localIssuer
				}
				_ = json.NewEncoder(w).Encode(AuthorizationServerMetadata{
					Issuer: issuer, AuthorizationEndpoint: localIssuer + "/authorize", TokenEndpoint: localIssuer + "/token",
					RegistrationEndpoint: localIssuer + "/register", CodeChallengeMethodsSupported: test.pkce,
				})
			}))
			defer server.Close()
			_, err := FetchAuthorizationServerMetadata(context.Background(), server.Client(), server.URL)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAuthorizationCodeTransportRefreshesAndPersists(t *testing.T) {
	var mcpCalls atomic.Int32
	var persisted string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_ = r.ParseForm()
			if r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("client_id") != "client" {
				t.Fatalf("unexpected refresh form: %v", r.Form)
			}
			_ = json.NewEncoder(w).Encode(oauthWireToken{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 3600})
			return
		}
		mcpCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer new-access" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": map[string]any{"tools": []any{}}})
	}))
	defer server.Close()

	expired, _ := json.Marshal(OAuthTokenSet{AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Minute)})
	tx := newHTTP(ServerConfig{URL: server.URL + "/mcp", Auth: AuthConfig{Type: "oauth_authorization_code", SecretRef: "oauth", TokenURL: server.URL + "/token", ClientID: "client", Resource: server.URL + "/mcp"}},
		func(context.Context, string) (string, error) { return string(expired), nil },
		func(_ context.Context, _ string, value string) error { persisted = value; return nil })
	result, err := tx.request(context.Background(), "tools/list", map[string]any{})
	if err != nil || !strings.Contains(string(result), `"tools"`) || mcpCalls.Load() != 1 {
		t.Fatalf("request = %s, calls=%d, err=%v", result, mcpCalls.Load(), err)
	}
	var saved OAuthTokenSet
	if json.Unmarshal([]byte(persisted), &saved) != nil || saved.AccessToken != "new-access" || saved.RefreshToken != "new-refresh" {
		t.Fatalf("persisted token = %q", persisted)
	}
}

func TestAuthorizationCodeTransportRefusesAnonymousUse(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	tx := newHTTP(ServerConfig{URL: server.URL, Auth: AuthConfig{Type: "oauth_authorization_code", SecretRef: "oauth", TokenURL: server.URL + "/token", ClientID: "client"}},
		func(context.Context, string) (string, error) { return "", context.Canceled }, nil)
	_, err := tx.request(context.Background(), "tools/list", nil)
	if err == nil || !strings.Contains(err.Error(), "sign-in is required") || calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}
