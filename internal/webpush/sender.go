package webpush

import (
	"bytes"
	"crypto/ecdsa"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Sender delivers encrypted Web Push messages authenticated with VAPID.
type Sender struct {
	publicB64 string
	priv      *ecdsa.PrivateKey
	subject   string
	client    *http.Client
}

// NewSender builds a Sender from a VAPID keypair (base64url) and a subject
// (a mailto: or https: contact URI required by push services).
func NewSender(publicB64, privateB64, subject string) (*Sender, error) {
	priv, err := parsePrivate(privateB64)
	if err != nil {
		return nil, err
	}
	return &Sender{
		publicB64: publicB64,
		priv:      priv,
		subject:   subject,
		client:    &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// PublicKey returns the base64url applicationServerKey for browsers to subscribe.
func (s *Sender) PublicKey() string { return s.publicB64 }

// Send encrypts payload for sub and POSTs it to the push service. ttl is the
// message time-to-live in seconds. A 404/410 response means the subscription is
// gone and the caller should delete it (surfaced as ErrGone).
func (s *Sender) Send(sub Subscription, payload []byte, ttl int) error {
	if ttl <= 0 {
		ttl = 86400
	}
	body, err := encrypt(sub, payload, 4096)
	if err != nil {
		return err
	}
	auth, err := vapidAuthHeader(s.priv, s.publicB64, s.subject, sub.Endpoint)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(ttl))

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return ErrGone
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("webpush: push service returned %d: %s", resp.StatusCode, string(msg))
	}
	return nil
}

// ErrGone signals the subscription no longer exists and should be removed.
var ErrGone = fmt.Errorf("webpush: subscription gone")
