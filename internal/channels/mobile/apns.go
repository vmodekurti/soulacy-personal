package mobile

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	apnsProductionURL = "https://api.push.apple.com"
	apnsSandboxURL    = "https://api.sandbox.push.apple.com"
)

// apnsClient sends the generic wake-up notification directly from a Personal
// gateway to Apple. The full agent result remains in Soulacy's durable inbox.
type apnsClient struct {
	teamID   string
	keyID    string
	key      *ecdsa.PrivateKey
	client   *http.Client
	endpoint func(environment string) string

	mu        sync.Mutex
	token     string
	tokenTime time.Time
}

func newAPNSClientFromEnvironment() (*apnsClient, error) {
	teamID := strings.TrimSpace(os.Getenv("SOULACY_MOBILE_APNS_TEAM_ID"))
	keyID := strings.TrimSpace(os.Getenv("SOULACY_MOBILE_APNS_KEY_ID"))
	keyFile := strings.TrimSpace(os.Getenv("SOULACY_MOBILE_APNS_PRIVATE_KEY_FILE"))
	if teamID == "" && keyID == "" && keyFile == "" {
		return nil, nil
	}
	if teamID == "" || keyID == "" || keyFile == "" {
		return nil, errors.New("APNs requires SOULACY_MOBILE_APNS_TEAM_ID, SOULACY_MOBILE_APNS_KEY_ID, and SOULACY_MOBILE_APNS_PRIVATE_KEY_FILE")
	}
	pem, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read APNs private key: %w", err)
	}
	key, err := jwt.ParseECPrivateKeyFromPEM(pem)
	if err != nil {
		return nil, fmt.Errorf("parse APNs private key: %w", err)
	}
	return &apnsClient{
		teamID: teamID,
		keyID:  keyID,
		key:    key,
		client: &http.Client{Timeout: 8 * time.Second},
		endpoint: func(environment string) string {
			if environment == "development" {
				return apnsSandboxURL
			}
			return apnsProductionURL
		},
	}, nil
}

func (a *apnsClient) authorizationToken(now time.Time) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && now.Sub(a.tokenTime) < 45*time.Minute {
		return a.token, nil
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": a.teamID,
		"iat": now.Unix(),
	})
	token.Header["kid"] = a.keyID
	signed, err := token.SignedString(a.key)
	if err != nil {
		return "", fmt.Errorf("sign APNs provider token: %w", err)
	}
	a.token, a.tokenTime = signed, now
	return signed, nil
}

func (a *apnsClient) push(ctx context.Context, n relayNotification) error {
	if len(n.DeviceToken) != 64 {
		return errors.New("APNs device token must be 32 bytes encoded as hexadecimal")
	}
	if subtle.ConstantTimeCompare([]byte(n.BundleID), []byte("dev.soulacy.ios")) != 1 {
		return errors.New("APNs bundle ID is not allowed")
	}
	providerToken, err := a.authorizationToken(time.Now().UTC())
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert": map[string]string{"title": n.Title, "body": n.Body},
			"sound": "default",
		},
		"delivery_id": n.DeliveryID,
		"deep_link":   n.DeepLink,
	})
	if err != nil {
		return err
	}
	base := a.endpoint(n.Environment)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/3/device/"+n.DeviceToken, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "bearer "+providerToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apns-topic", n.BundleID)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("apns-expiration", "0")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("send APNs request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var rejected struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(body, &rejected)
	if rejected.Reason == "" {
		rejected.Reason = resp.Status
	}
	return fmt.Errorf("APNs rejected notification: %s", rejected.Reason)
}
