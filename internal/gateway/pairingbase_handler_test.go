package gateway

import (
	"net/http"
	"strings"
	"testing"
)

// Endpoint-level contract for #160: the pair URL must be built from an address
// the phone can use, an explicit address is honoured, a loopback address is
// flagged rather than silently minted, and garbage is rejected. The probe stub
// (see pairingbase_stub_test.go) treats every candidate as unreachable, so
// "reachable" is false throughout and the assertions stay host-agnostic.
func TestPairingTokenBaseURL(t *testing.T) {
	srv := newTestGateway(t, "secret")

	t.Run("pair_url is built from the reported base_url", func(t *testing.T) {
		status, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/pairing/tokens", "secret", "")
		if status != http.StatusOK {
			t.Fatalf("status %d: %v", status, body)
		}
		base, _ := body["base_url"].(string)
		code, _ := body["code"].(string)
		pairURL, _ := body["pair_url"].(string)
		if base == "" || code == "" || pairURL != base+"/mobile?pair="+code {
			t.Fatalf("pair_url must be base_url + /mobile?pair=code; got base=%q code=%q pair_url=%q", base, code, pairURL)
		}
		if r, _ := body["reachable"].(bool); r {
			t.Fatalf("stubbed probe answers false; reachable must be false: %v", body)
		}
	})

	t.Run("an explicit base_url is honoured", func(t *testing.T) {
		status, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/pairing/tokens", "secret", `{"base_url":"http://my-mac.tailnet.ts.net:18789/"}`)
		if status != http.StatusOK {
			t.Fatalf("status %d: %v", status, body)
		}
		if base, _ := body["base_url"].(string); base != "http://my-mac.tailnet.ts.net:18789" {
			t.Fatalf("base_url = %q", base)
		}
		if pairURL, _ := body["pair_url"].(string); !strings.HasPrefix(pairURL, "http://my-mac.tailnet.ts.net:18789/mobile?pair=") {
			t.Fatalf("pair_url = %q", pairURL)
		}
	})

	t.Run("a loopback base_url is flagged, not silently minted", func(t *testing.T) {
		status, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/pairing/tokens", "secret", `{"base_url":"http://127.0.0.1:18789"}`)
		if status != http.StatusOK {
			t.Fatalf("status %d: %v", status, body)
		}
		if r, _ := body["reachable"].(bool); r {
			t.Fatalf("loopback must not be reachable: %v", body)
		}
		if hint, _ := body["hint"].(string); hint == "" {
			t.Fatalf("loopback must carry a hint: %v", body)
		}
	})

	t.Run("an invalid base_url is rejected", func(t *testing.T) {
		status, _ := gatewayJSON(t, srv, http.MethodPost, "/api/v1/pairing/tokens", "secret", `{"base_url":"nope"}`)
		if status != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", status)
		}
	})
}
