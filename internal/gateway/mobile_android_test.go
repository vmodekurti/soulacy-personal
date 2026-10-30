package gateway

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	mobilechan "github.com/soulacy/soulacy/internal/channels/mobile"
)

func TestMobileDeviceRegistrationPlatforms(t *testing.T) {
	store, err := mobilechan.Open(filepath.Join(t.TempDir(), "mobile.db"))
	if err != nil {
		t.Fatal(err)
	}
	mobilechan.SetDefaultStore(store)
	t.Cleanup(func() {
		mobilechan.SetDefaultStore(nil)
		_ = store.Close()
	})
	s := newTestGateway(t, "secret")
	ctx := context.Background()
	fcmToken := "dGVzdA:APA91b-Mixed_Case-token"
	apnsToken := strings.Repeat("AB", 32)

	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"unknown platform", `{"id":"p","platform":"windows"}`, http.StatusBadRequest},
		{"android with iOS bundle", `{"id":"p","platform":"android","bundle_id":"dev.soulacy.ios"}`, http.StatusBadRequest},
		{"android with foreign bundle", `{"id":"p","platform":"android","bundle_id":"com.example.app"}`, http.StatusBadRequest},
		{"android token with whitespace", `{"id":"p","platform":"android","push_token":"abc def"}`, http.StatusBadRequest},
		{"android token too long", `{"id":"p","platform":"android","push_token":"` + strings.Repeat("a", 4097) + `"}`, http.StatusBadRequest},
		{"iOS with android bundle", `{"id":"p","platform":"ios","bundle_id":"dev.soulacy.android"}`, http.StatusBadRequest},
		{"iOS with FCM token", `{"id":"p","push_token":"` + fcmToken + `"}`, http.StatusBadRequest},
	} {
		if status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mobile/devices", "secret", tc.body); status != tc.want {
			t.Errorf("%s: status = %d body = %v, want %d", tc.name, status, body, tc.want)
		}
	}

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mobile/devices", "secret",
		`{"id":"pixel","name":"Pixel","platform":"android","bundle_id":"dev.soulacy.android","push_token":"`+fcmToken+
			`","push_environment":"development","live_start_token":"not-hex","notifications_enabled":true}`)
	if status != http.StatusCreated || body["registered"] != true {
		t.Fatalf("android register status = %d body = %v", status, body)
	}
	status, _ = gatewayJSON(t, s, http.MethodPost, "/api/v1/mobile/devices", "secret",
		`{"id":"pixel-no-bundle","platform":"Android","push_token":"`+strings.Repeat("x", 4096)+`","notifications_enabled":true}`)
	if status != http.StatusCreated {
		t.Fatalf("android without bundle_id status = %d", status)
	}
	status, _ = gatewayJSON(t, s, http.MethodPost, "/api/v1/mobile/devices", "secret",
		`{"id":"iphone","push_token":"`+apnsToken+`","bundle_id":"dev.soulacy.ios","notifications_enabled":true}`)
	if status != http.StatusCreated {
		t.Fatalf("iOS register status = %d", status)
	}

	devices, err := store.TargetDevices(ctx, "personal", "user:admin")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]mobilechan.Device{}
	for _, d := range devices {
		byID[d.ID] = d
	}
	pixel := byID["pixel"]
	if pixel.Platform != mobilechan.PlatformAndroid || pixel.PushToken != fcmToken ||
		pixel.BundleID != mobilechan.BundleAndroid || pixel.PushEnvironment != "" {
		t.Fatalf("android device = %#v", pixel)
	}
	if byID["pixel-no-bundle"].BundleID != mobilechan.BundleAndroid {
		t.Fatalf("android default bundle = %#v", byID["pixel-no-bundle"])
	}
	iphone := byID["iphone"]
	if iphone.Platform != mobilechan.PlatformIOS || iphone.PushToken != strings.ToLower(apnsToken) ||
		iphone.BundleID != mobilechan.BundleIOS || iphone.PushEnvironment != "production" {
		t.Fatalf("iOS device = %#v", iphone)
	}
	if live, _ := store.LiveStartDevices(ctx, "personal", "device:pixel"); len(live) != 0 {
		t.Fatalf("android live_start_token was stored: %#v", live)
	}

	status, _ = gatewayJSON(t, s, http.MethodPost, "/api/v1/mobile/activities", "secret",
		`{"device_id":"pixel","agent_id":"a","session_id":"s","token":"`+strings.Repeat("ab", 32)+`"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("android live activity registration status = %d, want 400", status)
	}
}
