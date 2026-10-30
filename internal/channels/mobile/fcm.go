package mobile

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	fcmDefaultSendBase = "https://fcm.googleapis.com"
	fcmDefaultTokenURI = "https://oauth2.googleapis.com/token"
	fcmScope           = "https://www.googleapis.com/auth/firebase.messaging"
	// fcmTokenSkew refreshes the OAuth access token this long before Google
	// says it expires, so a send never races the expiry.
	fcmTokenSkew = 5 * time.Minute
)

// ErrFCMUnregistered reports that FCM no longer knows a registration token:
// the app was uninstalled or the token rotated. The caller forgets the token.
var ErrFCMUnregistered = errors.New("FCM registration token is no longer registered")

// fcmClient sends data-only wake-ups to the Android app through the FCM HTTP
// v1 API, authenticating as a Google service account. Like APNs, the push is
// only a wake-up; the app fetches the durable result from the gateway.
type fcmClient struct {
	projectID    string
	clientEmail  string
	privateKeyID string
	tokenURI     string
	key          *rsa.PrivateKey
	sendBase     string
	client       *http.Client
	now          func() time.Time

	mu          sync.Mutex
	accessToken string
	expiry      time.Time
}

type fcmServiceAccount struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	TokenURI     string `json:"token_uri"`
}

// newFCMClientFromEnvironment returns nil, nil when FCM is not configured.
func newFCMClientFromEnvironment() (*fcmClient, error) {
	file := strings.TrimSpace(os.Getenv("SOULACY_MOBILE_FCM_CREDENTIALS_FILE"))
	if file == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read FCM service account: %w", err)
	}
	return newFCMClient(raw, strings.TrimSpace(os.Getenv("SOULACY_MOBILE_FCM_PROJECT_ID")), fcmDefaultSendBase,
		&http.Client{Timeout: 8 * time.Second})
}

// newFCMClient builds a sender from a service-account JSON document.
// projectOverride replaces the file's project_id when set; sendBase is the
// FCM origin (injectable for tests).
func newFCMClient(credentials []byte, projectOverride, sendBase string, client *http.Client) (*fcmClient, error) {
	var sa fcmServiceAccount
	if err := json.Unmarshal(credentials, &sa); err != nil {
		return nil, fmt.Errorf("parse FCM service account: %w", err)
	}
	if sa.Type != "" && sa.Type != "service_account" {
		return nil, fmt.Errorf("FCM credentials must be a service_account key, got %q", sa.Type)
	}
	projectID := strings.TrimSpace(projectOverride)
	if projectID == "" {
		projectID = strings.TrimSpace(sa.ProjectID)
	}
	if projectID == "" || strings.TrimSpace(sa.ClientEmail) == "" || strings.TrimSpace(sa.PrivateKey) == "" {
		return nil, errors.New("FCM service account requires project_id, client_email and private_key")
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(sa.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("parse FCM private key: %w", err)
	}
	tokenURI := strings.TrimSpace(sa.TokenURI)
	if tokenURI == "" {
		tokenURI = fcmDefaultTokenURI
	}
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	return &fcmClient{
		projectID: projectID, clientEmail: strings.TrimSpace(sa.ClientEmail), privateKeyID: strings.TrimSpace(sa.PrivateKeyID),
		tokenURI: tokenURI, key: key, sendBase: strings.TrimRight(sendBase, "/"), client: client, now: time.Now,
	}, nil
}

// token returns a cached OAuth2 access token, minting a new one from a
// signed JWT assertion when the cached one is missing or about to expire.
func (f *fcmClient) token(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now().UTC()
	if f.accessToken != "" && now.Before(f.expiry) {
		return f.accessToken, nil
	}
	claims := jwt.MapClaims{
		"iss":   f.clientEmail,
		"scope": fcmScope,
		"aud":   f.tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	assertion := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	if f.privateKeyID != "" {
		assertion.Header["kid"] = f.privateKeyID
	}
	signed, err := assertion.SignedString(f.key)
	if err != nil {
		return "", fmt.Errorf("sign FCM token assertion: %w", err)
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {signed},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request FCM access token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("FCM token endpoint returned %s", resp.Status)
	}
	var granted struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &granted); err != nil || granted.AccessToken == "" {
		return "", errors.New("FCM token endpoint returned no access token")
	}
	lifetime := time.Duration(granted.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = time.Hour
	}
	refreshAt := lifetime - fcmTokenSkew
	if refreshAt < lifetime/2 {
		refreshAt = lifetime / 2
	}
	f.accessToken, f.expiry = granted.AccessToken, now.Add(refreshAt)
	return f.accessToken, nil
}

func (f *fcmClient) invalidateToken() {
	f.mu.Lock()
	f.accessToken, f.expiry = "", time.Time{}
	f.mu.Unlock()
}

// fcmReservedDataKey reports keys FCM refuses in a data payload.
func fcmReservedDataKey(k string) bool {
	switch k {
	case "", "from", "message_type", "collapse_key", "notification":
		return true
	}
	return strings.HasPrefix(k, "google.") || strings.HasPrefix(k, "gcm.")
}

// fcmData flattens a notification into FCM's string-only data map. The core
// keys always win over Notification.Data so the app's contract stays stable.
func fcmData(n relayNotification) map[string]string {
	data := map[string]string{}
	for k, v := range n.Data {
		if !fcmReservedDataKey(k) {
			data[k] = v
		}
	}
	data["category"] = n.Category
	data["title"] = n.Title
	data["body"] = n.Body
	data["deep_link"] = n.DeepLink
	data["thread_id"] = n.ThreadID
	data["time_sensitive"] = strconv.FormatBool(n.TimeSensitive)
	if n.DeliveryID != "" {
		data["delivery_id"] = n.DeliveryID
	}
	return data
}

// push sends one data-only, high-priority message. There is deliberately no
// notification block: the app renders the alert itself after waking.
func (f *fcmClient) push(ctx context.Context, n relayNotification) error {
	if n.DeviceToken == "" {
		return errors.New("FCM registration token is empty")
	}
	if n.BundleID != "" && n.BundleID != BundleAndroid {
		return errors.New("FCM package name is not allowed")
	}
	payload, err := json.Marshal(map[string]any{
		"message": map[string]any{
			"token":   n.DeviceToken,
			"data":    fcmData(n),
			"android": map[string]any{"priority": "high"},
		},
	})
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		access, err := f.token(ctx)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			f.sendBase+"/v1/projects/"+url.PathEscape(f.projectID)+"/messages:send", bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+access)
		req.Header.Set("Content-Type", "application/json")
		resp, err := f.client.Do(req)
		if err != nil {
			return fmt.Errorf("send FCM request: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
		// A revoked or rotated access token: mint a fresh one once.
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			f.invalidateToken()
			continue
		}
		return fcmError(resp, body)
	}
}

func fcmError(resp *http.Response, body []byte) error {
	var rejected struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
			Details []struct {
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &rejected)
	if resp.StatusCode == http.StatusNotFound {
		return ErrFCMUnregistered
	}
	for _, d := range rejected.Error.Details {
		if d.ErrorCode == "UNREGISTERED" {
			return ErrFCMUnregistered
		}
	}
	reason := rejected.Error.Status
	if reason == "" {
		reason = resp.Status
	}
	return fmt.Errorf("FCM rejected notification: %s", reason)
}
