package gateway

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/auth/apikeys"
)

// One paired device per person (#216): a second pairing is refused until the
// first is cleared, the status route says which phone is paired, and unpair
// makes room for a different device.
func TestOneDevicePerPerson(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	store, err := apikeys.NewSQLiteStore(filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	s.SetAPIKeyStore(store)
	admin := householdApp(t, s, "admin", "admin")
	api := admin.Group("/api/v1")
	api.Get("/pairing/status", s.handlePairingStatus)
	api.Post("/pairing/unpair", s.handleUnpair)

	code, body := doJSON(t, admin, http.MethodGet, "/api/v1/pairing/status", "")
	if code != http.StatusOK || body["paired"] != false {
		t.Fatalf("status before pairing: %d %+v", code, body)
	}

	// First phone pairs.
	code, tok := doJSON(t, admin, http.MethodPost, "/api/v1/pairing/tokens", "")
	if code != http.StatusOK {
		t.Fatalf("first token: %d %+v", code, tok)
	}
	code, redeemed := doJSON(t, admin, http.MethodPost, "/api/v1/pairing/redeem", `{"code":"`+tok["code"].(string)+`"}`)
	if code != http.StatusOK || redeemed["paired"] != true {
		t.Fatalf("first redeem: %d %+v", code, redeemed)
	}

	// A second code for the same person is refused, with the paired phone named.
	code, second := doJSON(t, admin, http.MethodPost, "/api/v1/pairing/tokens", "")
	if code != http.StatusConflict || second["paired"] == nil {
		t.Fatalf("second token should be 409 with the paired device, got %d %+v", code, second)
	}
	if msg, _ := second["error"].(string); !strings.HasPrefix(msg, "Your phone is already paired (since ") || !strings.Contains(msg, "Unpair it first") {
		t.Fatalf("refusal should name the phone and the way out, got %q", msg)
	}
	code, body = doJSON(t, admin, http.MethodGet, "/api/v1/pairing/status", "")
	if code != http.StatusOK || body["paired"] != true || body["device"].(map[string]any)["owner"] != true {
		t.Fatalf("status after pairing: %d %+v", code, body)
	}

	// Someone else's phone is unaffected by the owner's.
	code, other := doJSON(t, admin, http.MethodPost, "/api/v1/pairing/tokens", `{"name":"Priya"}`)
	if code != http.StatusOK || other["subject"] != "priya" {
		t.Fatalf("pairing another person should still work: %d %+v", code, other)
	}

	// Unpair clears the way; the old credential is dead.
	code, cleared := doJSON(t, admin, http.MethodPost, "/api/v1/pairing/unpair", "")
	if code != http.StatusOK || cleared["unpaired"] != true || cleared["revoked"] != float64(1) {
		t.Fatalf("unpair: %d %+v", code, cleared)
	}
	if _, err := store.Validate(t.Context(), redeemed["token"].(string)); err == nil {
		t.Fatal("the unpaired phone's credential must be revoked")
	}
	code, body = doJSON(t, admin, http.MethodGet, "/api/v1/pairing/status", "")
	if code != http.StatusOK || body["paired"] != false {
		t.Fatalf("status after unpair: %d %+v", code, body)
	}
	if code, _ := doJSON(t, admin, http.MethodPost, "/api/v1/pairing/tokens", ""); code != http.StatusOK {
		t.Fatalf("a new phone can pair after unpairing, got %d", code)
	}

	// A code minted before another phone paired is refused at redeem too.
	code, stale := doJSON(t, admin, http.MethodPost, "/api/v1/pairing/tokens", `{"name":"Kai"}`)
	if code != http.StatusOK {
		t.Fatalf("token for Kai: %d %+v", code, stale)
	}
	code, stale2 := doJSON(t, admin, http.MethodPost, "/api/v1/pairing/tokens", `{"name":"Kai"}`)
	if code != http.StatusOK {
		t.Fatalf("second token for Kai before any redeem: %d %+v", code, stale2)
	}
	if code, r := doJSON(t, admin, http.MethodPost, "/api/v1/pairing/redeem", `{"code":"`+stale["code"].(string)+`"}`); code != http.StatusOK {
		t.Fatalf("Kai's first redeem: %d %+v", code, r)
	}
	if code, r := doJSON(t, admin, http.MethodPost, "/api/v1/pairing/redeem", `{"code":"`+stale2["code"].(string)+`"}`); code != http.StatusConflict || r["paired"] != false {
		t.Fatalf("Kai's second phone must be refused at redeem: %d %+v", code, r)
	}
}
