package entitlements

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestStripeCheckoutSession(t *testing.T) {
	var got url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, _, ok := r.BasicAuth(); !ok || user != "sk_test" {
			t.Fatal("Stripe secret was not sent with HTTP Basic auth")
		}
		if r.Header.Get("Idempotency-Key") != "checkout-key" {
			t.Fatalf("idempotency key = %q", r.Header.Get("Idempotency-Key"))
		}
		body, _ := io.ReadAll(r.Body)
		got, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_1","url":"https://checkout.stripe.test/cs_1"}`))
	}))
	defer server.Close()

	client := &StripeClient{SecretKey: "sk_test", BaseURL: server.URL, Client: server.Client()}
	session, err := client.CreateCheckout(context.Background(), CheckoutRequest{
		WorkspaceID: "ws_1", Plan: "team", PriceID: "price_team", CustomerID: "cus_1",
		SuccessURL: "https://app.test/billing?success=1", CancelURL: "https://app.test/billing", IdempotencyKey: "checkout-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != "cs_1" || session.URL == "" {
		t.Fatalf("session = %#v", session)
	}
	for key, want := range map[string]string{
		"mode": "subscription", "customer": "cus_1", "line_items[0][price]": "price_team",
		"metadata[workspace_id]": "ws_1", "subscription_data[metadata][workspace_id]": "ws_1",
	} {
		if got.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, got.Get(key), want)
		}
	}
}

func TestStripePortalSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		if form.Get("customer") != "cus_1" || form.Get("return_url") != "https://app.test/settings" {
			t.Fatalf("portal form = %v", form)
		}
		_, _ = w.Write([]byte(`{"id":"bps_1","url":"https://billing.stripe.test/bps_1"}`))
	}))
	defer server.Close()
	client := &StripeClient{SecretKey: "sk_test", BaseURL: server.URL, Client: server.Client()}
	if _, err := client.CreatePortal(context.Background(), PortalRequest{CustomerID: "cus_1", ReturnURL: "https://app.test/settings"}); err != nil {
		t.Fatal(err)
	}
}
