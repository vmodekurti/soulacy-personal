package mobile

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestAdapterPersistsBeforeSendingGenericPush(t *testing.T) {
	var got relayNotification
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer relay-secret" {
			t.Errorf("authorization header = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode relay request: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer relay.Close()
	t.Setenv("SOULACY_MOBILE_PUSH_RELAY_URL", relay.URL)
	t.Setenv("SOULACY_MOBILE_PUSH_RELAY_TOKEN", "relay-secret")

	store, err := Open(filepath.Join(t.TempDir(), "mobile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.UpsertDevice(ctx, "personal", "admin", Device{
		ID: "phone", PushToken: strings.Repeat("ab", 32), NotificationsEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	a := New(store, nil)
	if err := a.Start(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Send(ctx, message.Message{
		ID: "delivery-1", SessionID: "session-1", AgentID: "genie", Channel: "mobile", ThreadID: "all",
		Parts: message.Text("private result"), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	deliveries, err := store.List(ctx, "personal", "phone", "admin", 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Body != "private result" {
		t.Fatalf("persisted deliveries = %#v, err=%v", deliveries, err)
	}
	if got.DeliveryID != "delivery-1" || got.DeepLink != "soulacy://delivery/delivery-1" {
		t.Fatalf("relay notification = %#v", got)
	}
	if got.Body != "An agent result is ready to review." || strings.Contains(got.Body, "private result") {
		t.Fatalf("push body exposed result content: %q", got.Body)
	}
}

func TestAdapterRelayFailureDoesNotLoseDelivery(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer relay.Close()
	t.Setenv("SOULACY_MOBILE_PUSH_RELAY_URL", relay.URL)

	store, err := Open(filepath.Join(t.TempDir(), "mobile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.UpsertDevice(ctx, "personal", "admin", Device{
		ID: "phone", PushToken: strings.Repeat("cd", 32), NotificationsEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	a := New(store, nil)
	if err := a.Send(ctx, message.Message{ID: "kept", AgentID: "agent", ThreadID: "all", Parts: message.Text("result")}); err != nil {
		t.Fatalf("relay failure must not fail durable delivery: %v", err)
	}
	items, err := store.List(ctx, "personal", "phone", "admin", 10)
	if err != nil || len(items) != 1 || items[0].ID != "kept" {
		t.Fatalf("durable delivery missing after relay failure: %#v, err=%v", items, err)
	}
}
