package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ProtectedResourceMetadata is the RFC 9728 document published by an
// OAuth-protected MCP server.
type ProtectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// AuthorizationServerMetadata is the subset of RFC 8414 used by Soulacy's
// authorization-code + PKCE client.
type AuthorizationServerMetadata struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	RegistrationEndpoint          string   `json:"registration_endpoint"`
	RevocationEndpoint            string   `json:"revocation_endpoint"`
	ScopesSupported               []string `json:"scopes_supported"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
}

// OAuthClientRegistration is returned by RFC 7591 dynamic registration.
// Agensi uses a public client, so ClientSecret is normally empty.
type OAuthClientRegistration struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
}

// OAuthTokenSet is encrypted as one vault value. Keeping access and refresh
// state together lets refresh rotation be committed atomically.
type OAuthTokenSet struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}

type oauthWireToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
}

func fetchOAuthJSON(ctx context.Context, client *http.Client, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("OAuth metadata endpoint returned HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode OAuth metadata: %w", err)
	}
	return nil
}

func FetchProtectedResourceMetadata(ctx context.Context, client *http.Client, endpoint string) (ProtectedResourceMetadata, error) {
	var metadata ProtectedResourceMetadata
	err := fetchOAuthJSON(ctx, client, endpoint, &metadata)
	if err == nil && (metadata.Resource == "" || len(metadata.AuthorizationServers) == 0) {
		err = fmt.Errorf("OAuth protected-resource metadata is incomplete")
	}
	return metadata, err
}

func FetchAuthorizationServerMetadata(ctx context.Context, client *http.Client, issuer string) (AuthorizationServerMetadata, error) {
	if err := validateOAuthEndpoint(issuer); err != nil {
		return AuthorizationServerMetadata{}, fmt.Errorf("unsafe OAuth issuer: %w", err)
	}
	endpoint := strings.TrimRight(issuer, "/") + "/.well-known/oauth-authorization-server"
	var metadata AuthorizationServerMetadata
	err := fetchOAuthJSON(ctx, client, endpoint, &metadata)
	if err == nil && (metadata.AuthorizationEndpoint == "" || metadata.TokenEndpoint == "" || metadata.RegistrationEndpoint == "") {
		err = fmt.Errorf("OAuth authorization-server metadata is incomplete")
	}
	if err == nil && strings.TrimRight(metadata.Issuer, "/") != strings.TrimRight(issuer, "/") {
		err = fmt.Errorf("OAuth authorization-server issuer does not match discovery issuer")
	}
	if err == nil {
		for _, endpoint := range []string{metadata.AuthorizationEndpoint, metadata.TokenEndpoint, metadata.RegistrationEndpoint} {
			if endpointErr := validateOAuthEndpoint(endpoint); endpointErr != nil {
				err = fmt.Errorf("unsafe OAuth endpoint: %w", endpointErr)
				break
			}
		}
	}
	if err == nil && !containsFold(metadata.CodeChallengeMethodsSupported, "S256") {
		err = fmt.Errorf("OAuth authorization server does not advertise PKCE S256")
	}
	return metadata, err
}

func validateOAuthEndpoint(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !u.IsAbs() || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("endpoint must be an absolute URL without user information")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		host := strings.TrimSpace(u.Hostname())
		if strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("endpoint must use HTTPS (HTTP is allowed only for loopback testing)")
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func RegisterOAuthClient(ctx context.Context, client *http.Client, endpoint, redirectURI, clientName string) (OAuthClientRegistration, error) {
	payload, _ := json.Marshal(map[string]any{
		"client_name":                clientName,
		"redirect_uris":              []string{redirectURI},
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return OAuthClientRegistration{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return OAuthClientRegistration{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return OAuthClientRegistration{}, fmt.Errorf("OAuth client registration returned HTTP %d", resp.StatusCode)
	}
	var registration OAuthClientRegistration
	if err := json.Unmarshal(body, &registration); err != nil || registration.ClientID == "" {
		return OAuthClientRegistration{}, fmt.Errorf("OAuth client registration returned an invalid response")
	}
	return registration, nil
}

func PKCEChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func AuthorizationURL(metadata AuthorizationServerMetadata, clientID, redirectURI, state, verifier, resource string, scopes []string) (string, error) {
	if err := validateOAuthEndpoint(metadata.AuthorizationEndpoint); err != nil {
		return "", fmt.Errorf("unsafe OAuth authorization endpoint: %w", err)
	}
	u, err := url.Parse(metadata.AuthorizationEndpoint)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", PKCEChallenge(verifier))
	q.Set("code_challenge_method", "S256")
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	if resource != "" {
		q.Set("resource", resource)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func exchangeOAuthToken(ctx context.Context, client *http.Client, endpoint string, values url.Values) (OAuthTokenSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return OAuthTokenSet{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return OAuthTokenSet{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return OAuthTokenSet{}, fmt.Errorf("OAuth token endpoint returned HTTP %d", resp.StatusCode)
	}
	var wire oauthWireToken
	if err := json.Unmarshal(body, &wire); err != nil || wire.AccessToken == "" {
		return OAuthTokenSet{}, fmt.Errorf("OAuth token endpoint returned an invalid response")
	}
	tokens := OAuthTokenSet{AccessToken: wire.AccessToken, RefreshToken: wire.RefreshToken, TokenType: wire.TokenType, Scope: wire.Scope}
	if tokens.TokenType == "" {
		tokens.TokenType = "Bearer"
	}
	if wire.ExpiresIn > 0 {
		tokens.ExpiresAt = time.Now().UTC().Add(time.Duration(wire.ExpiresIn) * time.Second)
	}
	return tokens, nil
}

func ExchangeAuthorizationCode(ctx context.Context, client *http.Client, endpoint, code, verifier, clientID, redirectURI, resource string) (OAuthTokenSet, error) {
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
	}
	if resource != "" {
		values.Set("resource", resource)
	}
	return exchangeOAuthToken(ctx, client, endpoint, values)
}

func RefreshAuthorizationCodeToken(ctx context.Context, client *http.Client, endpoint, refreshToken, clientID, resource string) (OAuthTokenSet, error) {
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	}
	if resource != "" {
		values.Set("resource", resource)
	}
	return exchangeOAuthToken(ctx, client, endpoint, values)
}
