package mobile

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestAPNsPushUsesProviderTokenAndGenericPayload(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/device/"+strings.Repeat("ab", 32) {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("apns-topic") != "dev.soulacy.ios" || r.Header.Get("apns-push-type") != "alert" {
			t.Errorf("APNs headers = %#v", r.Header)
		}
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "bearer ")
		parsed, err := jwt.Parse(raw, func(token *jwt.Token) (any, error) { return &key.PublicKey, nil })
		if err != nil || !parsed.Valid || parsed.Header["kid"] != "KEY123" {
			t.Errorf("provider token is invalid: token=%v err=%v", parsed, err)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &apnsClient{
		teamID: "TEAM123", keyID: "KEY123", key: key,
		client: server.Client(), endpoint: func(string) string { return server.URL },
	}
	err = client.push(context.Background(), relayNotification{
		DeviceToken: strings.Repeat("ab", 32), Environment: "production", BundleID: "dev.soulacy.ios",
		DeliveryID: "delivery-1", Title: "Result from agent", Body: "An agent result is ready to review.",
		DeepLink: "soulacy://delivery/delivery-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["delivery_id"] != "delivery-1" || body["deep_link"] != "soulacy://delivery/delivery-1" {
		t.Fatalf("payload routing fields = %#v", body)
	}
	aps, ok := body["aps"].(map[string]any)
	if !ok || aps["content-available"] != float64(1) {
		t.Fatalf("push must wake the app to refresh its durable inbox: %#v", body["aps"])
	}
	if strings.Contains(string(mustJSON(t, body)), "private result") {
		t.Fatal("push payload exposed durable result content")
	}
}

func TestAPNsProviderTokenIsCached(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	client := &apnsClient{teamID: "TEAM", keyID: "KEY", key: key}
	now := time.Now().UTC()
	first, err := client.authorizationToken(now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.authorizationToken(now.Add(10 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("provider token was not cached")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
