package mobile

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fcmFixture runs a fake Google token endpoint and a fake FCM send endpoint.
// The service-account key is generated per test; no real credential exists.
type fcmFixture struct {
	t          *testing.T
	key        *rsa.PrivateKey
	tokenSrv   *httptest.Server
	sendSrv    *httptest.Server
	creds      []byte
	mu         sync.Mutex
	tokenCalls int
	sendCalls  int
	bodies     []map[string]any
	paths      []string
	sendStatus int
	sendBody   string
}

func newFCMFixture(t *testing.T) *fcmFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fcmFixture{t: t, key: key, sendStatus: http.StatusOK, sendBody: `{"name":"projects/p/messages/1"}`}
	f.tokenSrv = httptest.NewServer(http.HandlerFunc(f.handleToken))
	t.Cleanup(f.tokenSrv.Close)
	f.sendSrv = httptest.NewServer(http.HandlerFunc(f.handleSend))
	t.Cleanup(f.sendSrv.Close)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	f.creds, _ = json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "soulacy-test",
		"private_key_id": "test-key-id",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "push@soulacy-test.iam.gserviceaccount.com",
		"token_uri":      f.tokenSrv.URL + "/token",
	})
	return f
}

func (f *fcmFixture) handleToken(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.tokenCalls++
	n := f.tokenCalls
	f.mu.Unlock()
	if err := r.ParseForm(); err != nil {
		f.t.Errorf("parse token form: %v", err)
	}
	if got := r.PostForm.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		f.t.Errorf("grant_type = %q", got)
	}
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(r.PostForm.Get("assertion"), claims, func(tok *jwt.Token) (any, error) {
		return &f.key.PublicKey, nil
		// Tests drive a fake clock; the signature and claims are checked here.
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithoutClaimsValidation())
	if err != nil || !parsed.Valid {
		f.t.Errorf("assertion did not verify: %v", err)
	} else {
		if parsed.Header["kid"] != "test-key-id" {
			f.t.Errorf("kid = %v", parsed.Header["kid"])
		}
		if claims["iss"] != "push@soulacy-test.iam.gserviceaccount.com" ||
			claims["scope"] != "https://www.googleapis.com/auth/firebase.messaging" ||
			claims["aud"] != f.tokenSrv.URL+"/token" {
			f.t.Errorf("assertion claims = %v", claims)
		}
		iat, _ := claims["iat"].(float64)
		exp, _ := claims["exp"].(float64)
		if exp-iat != 3600 {
			f.t.Errorf("assertion lifetime = %v, want 3600", exp-iat)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "access-" + string(rune('0'+n)), "expires_in": 3600, "token_type": "Bearer",
	})
}

func (f *fcmFixture) handleSend(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("decode send body: %v", err)
	}
	f.mu.Lock()
	f.sendCalls++
	f.bodies = append(f.bodies, body)
	f.paths = append(f.paths, r.URL.Path+" "+r.Header.Get("Authorization"))
	status, resp := f.sendStatus, f.sendBody
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(resp))
}

func (f *fcmFixture) client(t *testing.T, projectOverride string) *fcmClient {
	t.Helper()
	c, err := newFCMClient(f.creds, projectOverride, f.sendSrv.URL, f.sendSrv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFCMSenderSendsDataOnlyMessageAndCachesAccessToken(t *testing.T) {
	f := newFCMFixture(t)
	c := f.client(t, "")
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	ctx := context.Background()

	n := relayNotification{DeviceToken: "fcm:Token_AbC-123", BundleID: BundleAndroid, DeliveryID: "d-1",
		Title: "Approval needed", Body: "An agent wants to run a tool.", DeepLink: "soulacy://approval/call-1",
		Category: CategoryApproval, ThreadID: "approvals", TimeSensitive: true,
		Data: map[string]string{"call_id": "call-1", "agent_id": "genie", "title": "spoofed", "google.x": "dropped"}}
	if err := c.push(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := c.push(ctx, n); err != nil {
		t.Fatal(err)
	}
	if f.tokenCalls != 1 || f.sendCalls != 2 {
		t.Fatalf("token calls = %d, send calls = %d; want the access token cached", f.tokenCalls, f.sendCalls)
	}
	if want := "/v1/projects/soulacy-test/messages:send Bearer access-1"; f.paths[0] != want {
		t.Fatalf("send request = %q, want %q", f.paths[0], want)
	}
	msg, _ := f.bodies[0]["message"].(map[string]any)
	if msg == nil || len(f.bodies[0]) != 1 {
		t.Fatalf("body = %#v, want a single message object", f.bodies[0])
	}
	if _, has := msg["notification"]; has {
		t.Fatal("message carries a notification block; FCM pushes must be data-only")
	}
	if msg["token"] != "fcm:Token_AbC-123" {
		t.Fatalf("token = %v; FCM tokens must not be reformatted", msg["token"])
	}
	if android, _ := msg["android"].(map[string]any); android["priority"] != "high" {
		t.Fatalf("android = %#v, want priority high", msg["android"])
	}
	wantData := map[string]any{
		"category": CategoryApproval, "title": "Approval needed", "body": "An agent wants to run a tool.",
		"deep_link": "soulacy://approval/call-1", "thread_id": "approvals", "time_sensitive": "true",
		"delivery_id": "d-1", "call_id": "call-1", "agent_id": "genie",
	}
	if !reflect.DeepEqual(msg["data"], wantData) {
		t.Fatalf("data = %#v\nwant %#v", msg["data"], wantData)
	}

	// Shortly before expiry the token is refreshed.
	now = now.Add(56 * time.Minute)
	if err := c.push(ctx, relayNotification{DeviceToken: "t", Category: CategoryDelivery}); err != nil {
		t.Fatal(err)
	}
	if f.tokenCalls != 2 || !strings.HasSuffix(f.paths[2], "Bearer access-2") {
		t.Fatalf("token calls = %d, last request %q; want a refreshed access token", f.tokenCalls, f.paths[2])
	}
	data, _ := f.bodies[2]["message"].(map[string]any)["data"].(map[string]any)
	if _, has := data["delivery_id"]; has || data["time_sensitive"] != "false" {
		t.Fatalf("data without delivery = %#v", data)
	}
}

func TestFCMSenderProjectOverrideAndUnregistered(t *testing.T) {
	f := newFCMFixture(t)
	file := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(file, f.creds, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOULACY_MOBILE_FCM_CREDENTIALS_FILE", file)
	t.Setenv("SOULACY_MOBILE_FCM_PROJECT_ID", "override-project")
	c, err := newFCMClientFromEnvironment()
	if err != nil || c == nil {
		t.Fatalf("client = %v, err = %v", c, err)
	}
	if c.projectID != "override-project" || c.sendBase != fcmDefaultSendBase {
		t.Fatalf("project = %q, base = %q", c.projectID, c.sendBase)
	}
	c.sendBase, c.client = f.sendSrv.URL, f.sendSrv.Client()

	f.sendStatus = http.StatusBadRequest
	f.sendBody = `{"error":{"code":400,"status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`
	if err := c.push(context.Background(), relayNotification{DeviceToken: "gone"}); !errors.Is(err, ErrFCMUnregistered) {
		t.Fatalf("UNREGISTERED error = %v", err)
	}
	if !strings.HasPrefix(f.paths[0], "/v1/projects/override-project/messages:send ") {
		t.Fatalf("send path = %q", f.paths[0])
	}
	f.sendStatus, f.sendBody = http.StatusNotFound, `{"error":{"code":404,"status":"NOT_FOUND"}}`
	if err := c.push(context.Background(), relayNotification{DeviceToken: "gone"}); !errors.Is(err, ErrFCMUnregistered) {
		t.Fatalf("404 error = %v", err)
	}
	f.sendStatus, f.sendBody = http.StatusInternalServerError, `{"error":{"code":500,"status":"INTERNAL"}}`
	if err := c.push(context.Background(), relayNotification{DeviceToken: "t"}); err == nil || errors.Is(err, ErrFCMUnregistered) {
		t.Fatalf("500 error = %v, want a transient failure", err)
	}
}

func TestAdapterRoutesAndroidThroughFCMAndClearsGoneTokens(t *testing.T) {
	var relayed []relayNotification
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n relayNotification
		_ = json.NewDecoder(r.Body).Decode(&n)
		relayed = append(relayed, n)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer relay.Close()
	t.Setenv("SOULACY_MOBILE_PUSH_RELAY_URL", relay.URL)

	store, err := Open(filepath.Join(t.TempDir(), "mobile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.UpsertDevice(ctx, "personal", "admin", Device{ID: "iphone", PushToken: strings.Repeat("ab", 32),
		NotificationsEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDevice(ctx, "personal", "admin", Device{ID: "pixel", Platform: PlatformAndroid,
		PushToken: "fcm-token", NotificationsEnabled: true}); err != nil {
		t.Fatal(err)
	}
	f := newFCMFixture(t)
	a := New(store, nil)
	if a.CanPush() != true {
		t.Fatal("CanPush with relay")
	}
	a.fcm = f.client(t, "")

	n := Notification{Title: "Approval needed", Body: "generic", Category: CategoryApproval,
		DeepLink: "soulacy://approval/c1", Data: map[string]string{"call_id": "c1"}, TimeSensitive: true}
	if err := a.Notify(ctx, "personal", "user:admin", n); err != nil {
		t.Fatal(err)
	}
	if len(relayed) != 1 || relayed[0].DeviceToken != strings.Repeat("ab", 32) || relayed[0].BundleID != BundleIOS {
		t.Fatalf("iOS relay notifications = %#v", relayed)
	}
	if f.sendCalls != 1 {
		t.Fatalf("FCM sends = %d, want 1", f.sendCalls)
	}
	msg, _ := f.bodies[0]["message"].(map[string]any)
	if msg["token"] != "fcm-token" {
		t.Fatalf("FCM message = %#v", msg)
	}

	f.sendStatus, f.sendBody = http.StatusNotFound, `{"error":{"code":404,"status":"NOT_FOUND","details":[{"errorCode":"UNREGISTERED"}]}}`
	if err := a.Notify(ctx, "personal", "device:pixel", n); !errors.Is(err, ErrFCMUnregistered) {
		t.Fatalf("Notify error = %v", err)
	}
	devices, err := store.TargetDevices(ctx, "personal", "")
	if err != nil || len(devices) != 1 || devices[0].ID != "iphone" {
		t.Fatalf("devices after UNREGISTERED = %#v, err = %v; want the Android token cleared", devices, err)
	}
}

func TestCanPushWithOnlyFCM(t *testing.T) {
	t.Setenv("SOULACY_MOBILE_PUSH_RELAY_URL", "")
	f := newFCMFixture(t)
	file := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(file, f.creds, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOULACY_MOBILE_FCM_CREDENTIALS_FILE", file)
	a := New(nil, nil)
	if !a.CanPush() || a.fcm == nil || a.pushConfigErr != nil {
		t.Fatalf("CanPush = %v, fcm = %v, err = %v", a.CanPush(), a.fcm, a.pushConfigErr)
	}
}
