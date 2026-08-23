package entitlements

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"
)

type memoryStore struct {
	values map[string]Entitlement
	seen   map[string]bool
	writes int
}

func (m *memoryStore) Get(_ context.Context, workspace string) (Entitlement, error) {
	e, ok := m.values[workspace]
	if !ok {
		return Entitlement{}, ErrNotFound
	}
	return e, nil
}
func (m *memoryStore) ApplyEvent(_ context.Context, id string, e Entitlement, _ string) (bool, error) {
	if m.seen == nil {
		m.seen = map[string]bool{}
	}
	if m.values == nil {
		m.values = map[string]Entitlement{}
	}
	if m.seen[id] {
		return false, nil
	}
	m.seen[id] = true
	m.values[e.WorkspaceID] = e
	m.writes++
	return true, nil
}
func TestStripeFailureRevokesRuns(t *testing.T) {
	m := &memoryStore{}
	now := time.Now().UTC()
	body := []byte(`{"id":"evt_1","type":"invoice.payment_failed","data":{"object":{"id":"sub_1","customer":"cus_1","metadata":{"workspace_id":"ws_1"}}}}`)
	payload := fmt.Sprintf("%d.%s", now.Unix(), body)
	mac := hmac.New(sha256.New, []byte("whsec"))
	mac.Write([]byte(payload))
	sig := fmt.Sprintf("t=%d,v1=%s", now.Unix(), hex.EncodeToString(mac.Sum(nil)))
	if err := (StripeWebhook{Secret: "whsec", Store: m}).Handle(context.Background(), body, sig, now); err != nil {
		t.Fatal(err)
	}
	ok, _, _ := New(m).Allowed(context.Background(), "ws_1", CapabilityRuns)
	if ok {
		t.Fatal("past-due workspace allowed")
	}
}

func signedStripe(t *testing.T, secret string, now time.Time, body []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", now.Unix(), body)
	return fmt.Sprintf("t=%d,v1=%s", now.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

func TestStripePaidIsActiveAndReplayIsIdempotent(t *testing.T) {
	m := &memoryStore{}
	now := time.Now().UTC()
	body := []byte(`{"id":"evt_paid","type":"invoice.paid","data":{"object":{"id":"in_1","subscription":"sub_1","customer":"cus_1","status":"paid","metadata":{"workspace_id":"ws_1"}}}}`)
	sig := signedStripe(t, "whsec", now, body)
	h := StripeWebhook{Secret: "whsec", Store: m}
	if err := h.Handle(context.Background(), body, sig, now); err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(context.Background(), body, sig, now); err != nil {
		t.Fatal(err)
	}
	e, err := m.Get(context.Background(), "ws_1")
	if err != nil || e.Status != StatusActive || e.SubscriptionID != "sub_1" || m.writes != 1 {
		t.Fatalf("entitlement=%#v writes=%d err=%v", e, m.writes, err)
	}
	if _, err := m.Get(context.Background(), "ws_2"); !errors.Is(err, ErrNotFound) {
		t.Fatal("one workspace's billing state leaked into another")
	}
}

func TestUnknownStripeEventCannotGrantAccess(t *testing.T) {
	m := &memoryStore{}
	now := time.Now().UTC()
	body := []byte(`{"id":"evt_unknown","type":"customer.updated","data":{"object":{"metadata":{"workspace_id":"ws_1"}}}}`)
	if err := (StripeWebhook{Secret: "whsec", Store: m}).Handle(context.Background(), body, signedStripe(t, "whsec", now, body), now); err != nil {
		t.Fatal(err)
	}
	if m.writes != 0 {
		t.Fatal("unrecognized Stripe event changed entitlements")
	}
}

func TestMissingEntitlementPolicy(t *testing.T) {
	store := &memoryStore{}
	allowed, reason, err := New(store).Allowed(context.Background(), "ws_new", CapabilityRuns)
	if err != nil || !allowed || reason != "unmetered" {
		t.Fatalf("migration policy = (%v, %q, %v), want allowed unmetered", allowed, reason, err)
	}
	allowed, reason, err = NewWithOptions(store, ServiceOptions{Missing: MissingDenied}).Allowed(context.Background(), "ws_new", CapabilityRuns)
	if err != nil || allowed || reason != "subscription required" {
		t.Fatalf("strict policy = (%v, %q, %v), want denied subscription required", allowed, reason, err)
	}
}
