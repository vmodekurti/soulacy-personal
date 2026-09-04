package webpush

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"
)

// GenerateVAPIDKeys creates a new VAPID keypair, returning the public and
// private keys as base64url (unpadded) strings. The public key is the
// uncompressed EC point (65 bytes) browsers expect as applicationServerKey; the
// private key is the 32-byte scalar.
func GenerateVAPIDKeys() (publicB64, privateB64 string, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	ecdhPriv, err := priv.ECDH()
	if err != nil {
		return "", "", err
	}
	pub := ecdhPriv.PublicKey().Bytes() // uncompressed, 65 bytes
	d := priv.D.FillBytes(make([]byte, 32))
	return b64Encode(pub), b64Encode(d), nil
}

// parsePrivate reconstructs an ecdsa.PrivateKey from a base64url 32-byte scalar.
func parsePrivate(privateB64 string) (*ecdsa.PrivateKey, error) {
	d, err := b64Decode(privateB64)
	if err != nil {
		return nil, fmt.Errorf("webpush: bad vapid private key: %w", err)
	}
	curve := elliptic.P256()
	priv := new(ecdsa.PrivateKey)
	priv.Curve = curve
	priv.D = new(big.Int).SetBytes(d)
	priv.X, priv.Y = curve.ScalarBaseMult(d)
	if priv.X == nil {
		return nil, fmt.Errorf("webpush: invalid vapid private key")
	}
	return priv, nil
}

// vapidAuthHeader builds the "Authorization: vapid t=<jwt>, k=<pub>" value for a
// push endpoint per RFC 8292. aud is the endpoint's scheme://host origin.
func vapidAuthHeader(priv *ecdsa.PrivateKey, publicB64, subject, endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("webpush: bad endpoint: %w", err)
	}
	aud := u.Scheme + "://" + u.Host
	if subject == "" {
		subject = "mailto:admin@localhost"
	}

	header := b64Encode([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{
		"aud": aud,
		"exp": time.Now().Add(12 * time.Hour).Unix(),
		"sub": subject,
	})
	payload := b64Encode(claims)
	signingInput := header + "." + payload

	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[0:32])
	s.FillBytes(sig[32:64])
	jwt := signingInput + "." + b64Encode(sig)

	return "vapid t=" + jwt + ", k=" + publicB64, nil
}

func b64Encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func b64Decode(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	return base64.RawURLEncoding.DecodeString(s)
}
