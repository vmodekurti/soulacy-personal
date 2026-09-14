package mobile

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

func relayFixture(t *testing.T) (*Adapter, *Store, func() []relayNotification) {
	t.Helper()
	var mu sync.Mutex
	var got []relayNotification
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n relayNotification
		_ = json.NewDecoder(r.Body).Decode(&n)
		mu.Lock()
		got = append(got, n)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SOULACY_MOBILE_PUSH_RELAY_URL", srv.URL)
	t.Setenv("SOULACY_MOBILE_APNS_KEY_ID", "")
	store, err := Open(filepath.Join(t.TempDir(), "mobile.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	a := New(store, zap.NewNop())
	return a, store, func() []relayNotification {
		mu.Lock()
		defer mu.Unlock()
		return append([]relayNotification(nil), got...)
	}
}

func TestNotifyCarriesCategoryThreadAndDataToTargetedDevices(t *testing.T) {
	a, store, got := relayFixture(t)
	if !a.CanPush() {
		t.Fatal("relay transport should make the adapter pushable")
	}
	ctx := context.Background()
	tok := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, d := range []Device{
		{ID: "phone-ada", UserID: "ada", PushToken: tok, PushEnvironment: "production", BundleID: "dev.soulacy.ios", NotificationsEnabled: true},
		{ID: "phone-bob", UserID: "bob", PushToken: tok, PushEnvironment: "production", BundleID: "dev.soulacy.ios", NotificationsEnabled: true},
		{ID: "phone-muted", UserID: "ada", PushToken: tok, PushEnvironment: "production", BundleID: "dev.soulacy.ios", NotificationsEnabled: false},
	} {
		if err := store.UpsertDevice(ctx, "personal", d.UserID, d); err != nil {
			t.Fatal(err)
		}
	}
	err := a.Notify(ctx, "personal", "user:ada", Notification{
		Title: "Approval needed", Body: "planner wants to run shell_exec", Category: CategoryApproval,
		ThreadID: "approval-planner", DeepLink: "soulacy://approval/c1", TimeSensitive: true,
		Data: map[string]string{"call_id": "c1", "agent_id": "planner"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sent := got()
	if len(sent) != 1 {
		t.Fatalf("expected exactly ada's enabled phone, got %d pushes", len(sent))
	}
	n := sent[0]
	if n.Category != CategoryApproval || n.ThreadID != "approval-planner" || !n.TimeSensitive || n.Data["call_id"] != "c1" || n.DeepLink != "soulacy://approval/c1" {
		t.Fatalf("notification fields lost in transit: %+v", n)
	}
	if err := a.Notify(ctx, "personal", "device:phone-bob", Notification{Title: "t", Body: "b", Category: CategoryTrigger}); err != nil {
		t.Fatal(err)
	}
	if len(got()) != 2 {
		t.Fatal("device-scoped destination should reach exactly one phone")
	}
	if DefaultAdapter() != a {
		t.Fatal("New must register the default adapter")
	}
}

func TestAPNSPayloadIncludesCategoryAndInterruptionLevel(t *testing.T) {
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if r.Header.Get("apns-push-type") != "alert" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	client := &apnsClient{client: srv.Client(), endpoint: func(string) string { return srv.URL }, token: "test-token", tokenTime: time.Now()}
	err := client.push(context.Background(), relayNotification{
		DeviceToken: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", BundleID: "dev.soulacy.ios", Environment: "production",
		Title: "Approval needed", Body: "x", Category: CategoryApproval, ThreadID: "approval-a", TimeSensitive: true,
		Data: map[string]string{"call_id": "c1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	aps := payload["aps"].(map[string]any)
	if aps["category"] != CategoryApproval || aps["thread-id"] != "approval-a" || aps["interruption-level"] != "time-sensitive" || payload["call_id"] != "c1" {
		t.Fatalf("APNs payload missing fields: %+v", payload)
	}
}
