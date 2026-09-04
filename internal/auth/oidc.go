package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// OIDCValidator validates JWTs issued by a third-party OIDC provider
// (Google, Auth0, Okta, or any standards-compliant IDP).
//
// On construction it fetches the provider's discovery document
// (<issuer>/.well-known/openid-configuration) to locate the JWKS URI, then
// downloads and caches the public keys. The cache is refreshed every hour by a
// background goroutine; it is also refreshed on-demand when an unknown key ID
// is encountered (handles key rotation without a service restart).
//
// Supported key types: RSA (RS256/RS384/RS512) and EC (ES256/ES384/ES512).
// Keys are parsed manually from the JWKS JSON to avoid adding a heavy JWK
// library dependency — the math is straightforward for both key types.
type OIDCValidator struct {
	issuer   string
	audience string
	client   *http.Client

	mu        sync.RWMutex
	keys      map[string]any // kid → *rsa.PublicKey or *ecdsa.PublicKey
	fetchedAt time.Time
	jwksURI   string

	quit chan struct{}
	once sync.Once
}

const jwksTTL = time.Hour

func newOIDCValidator(issuer, audience string) (*OIDCValidator, error) {
	v := &OIDCValidator{
		issuer:   issuer,
		audience: audience,
		client:   &http.Client{Timeout: 10 * time.Second},
		keys:     make(map[string]any),
		quit:     make(chan struct{}),
	}
	// Fetch discovery doc synchronously — we want to fail fast at startup if
	// the issuer URL is misconfigured.
	if err := v.discover(); err != nil {
		return nil, err
	}
	go v.refreshLoop()
	return v, nil
}

// discover fetches <issuer>/.well-known/openid-configuration and extracts
// the jwks_uri, then calls fetchJWKS.
func (v *OIDCValidator) discover() error {
	url := strings.TrimRight(v.issuer, "/") + "/.well-known/openid-configuration"
	resp, err := v.client.Get(url)
	if err != nil {
		return fmt.Errorf("oidc discovery GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oidc discovery %s: HTTP %d", url, resp.StatusCode)
	}

	var doc struct {
		JWKsURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("oidc discovery decode: %w", err)
	}
	if doc.JWKsURI == "" {
		return fmt.Errorf("oidc discovery: missing jwks_uri in %s", url)
	}
	v.jwksURI = doc.JWKsURI
	return v.fetchJWKS()
}

// fetchJWKS downloads and parses the provider's JSON Web Key Set.
// Thread-safe: replaces the key cache atomically under a write lock.
func (v *OIDCValidator) fetchJWKS() error {
	resp, err := v.client.Get(v.jwksURI)
	if err != nil {
		return fmt.Errorf("jwks GET %s: %w", v.jwksURI, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks %s: HTTP %d", v.jwksURI, resp.StatusCode)
	}

	var ks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			// RSA fields
			N string `json:"n"`
			E string `json:"e"`
			// EC fields
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ks); err != nil {
		return fmt.Errorf("jwks decode: %w", err)
	}

	keys := make(map[string]any, len(ks.Keys))
	for _, k := range ks.Keys {
		// Skip non-signing keys (e.g. encryption keys).
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		switch k.Kty {
		case "RSA":
			pub, err := parseRSAPublicKey(k.N, k.E)
			if err != nil {
				continue // skip malformed key
			}
			keys[k.Kid] = pub
		case "EC":
			pub, err := parseECPublicKey(k.Crv, k.X, k.Y)
			if err != nil {
				continue
			}
			keys[k.Kid] = pub
		}
	}

	v.mu.Lock()
	v.keys = keys
	v.fetchedAt = time.Now()
	v.mu.Unlock()
	return nil
}

// Validate parses and validates a JWT against the cached JWKS.
// It checks the issuer, audience, and expiry claims in addition to the signature.
func (v *OIDCValidator) Validate(tokenStr string) (*Claims, error) {
	cl := &Claims{}
	opts := []jwt.ParserOption{
		jwt.WithIssuer(v.issuer),
		jwt.WithExpirationRequired(),
	}
	if v.audience != "" {
		opts = append(opts, jwt.WithAudience(v.audience))
	}

	_, err := jwt.ParseWithClaims(tokenStr, cl, v.keyFunc, opts...)
	if err != nil {
		return nil, err
	}
	return cl, nil
}

// keyFunc is the jwt.Keyfunc that resolves the signing key from the JWKS cache.
func (v *OIDCValidator) keyFunc(t *jwt.Token) (any, error) {
	kid, _ := t.Header["kid"].(string)

	v.mu.RLock()
	key, ok := v.keys[kid]
	v.mu.RUnlock()

	if !ok {
		// Key not in cache — try a fresh JWKS fetch (handles key rotation).
		if fetchErr := v.fetchJWKS(); fetchErr == nil {
			v.mu.RLock()
			key, ok = v.keys[kid]
			v.mu.RUnlock()
		}
		if !ok {
			return nil, fmt.Errorf("oidc: unknown kid %q", kid)
		}
	}

	switch t.Method.(type) {
	case *jwt.SigningMethodRSA:
		rsaKey, ok := key.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("oidc: key %q is not RSA", kid)
		}
		return rsaKey, nil
	case *jwt.SigningMethodECDSA:
		ecKey, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("oidc: key %q is not ECDSA", kid)
		}
		return ecKey, nil
	default:
		return nil, fmt.Errorf("oidc: unsupported signing method %q", t.Header["alg"])
	}
}

// close stops the background refresh goroutine. Idempotent.
func (v *OIDCValidator) close() {
	v.once.Do(func() { close(v.quit) })
}

func (v *OIDCValidator) refreshLoop() {
	t := time.NewTicker(jwksTTL)
	defer t.Stop()
	for {
		select {
		case <-v.quit:
			return
		case <-t.C:
			_ = v.fetchJWKS() // errors are transient; next tick will retry
		}
	}
}

// ---------------------------------------------------------------------------
// JWKS key parsers — RSA and ECDSA
// ---------------------------------------------------------------------------

// parseRSAPublicKey constructs an *rsa.PublicKey from the base64url-encoded
// modulus (n) and exponent (e) fields of a JWKS RSA key entry.
func parseRSAPublicKey(n, e string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(n)
	if err != nil {
		return nil, fmt.Errorf("rsa: decode n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(e)
	if err != nil {
		return nil, fmt.Errorf("rsa: decode e: %w", err)
	}
	eInt := int(new(big.Int).SetBytes(eBytes).Int64())
	if eInt == 0 {
		return nil, fmt.Errorf("rsa: exponent is zero")
	}
	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: eInt,
	}, nil
}

// parseECPublicKey constructs an *ecdsa.PublicKey from the curve name and
// base64url-encoded X, Y coordinate fields of a JWKS EC key entry.
func parseECPublicKey(crv, x, y string) (*ecdsa.PublicKey, error) {
	var curve elliptic.Curve
	switch crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, fmt.Errorf("ec: unsupported curve %q", crv)
	}
	xBytes, err := base64.RawURLEncoding.DecodeString(x)
	if err != nil {
		return nil, fmt.Errorf("ec: decode x: %w", err)
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(y)
	if err != nil {
		return nil, fmt.Errorf("ec: decode y: %w", err)
	}
	return &ecdsa.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}, nil
}
