package gateway

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/auth/apikeys"
)

// householdApp mounts the pairing handlers behind fixed claims, like adaptiveApp.
func householdApp(t *testing.T, s *Server, role, subject string) *fiber.App {
	t.Helper()
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{Role: role, RegisteredClaims: jwt.RegisteredClaims{Subject: subject}})
		return c.Next()
	})
	api := app.Group("/api/v1")
	api.Post("/pairing/tokens", s.handleCreatePairingToken)
	api.Post("/pairing/redeem", s.handleRedeemPairingToken)
	api.Get("/pairing/members", s.handleListHouseholdMembers)
	return app
}

func TestPairingForSomeoneElseMintsTheirIdentity(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	store, err := apikeys.NewSQLiteStore(filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	s.SetAPIKeyStore(store)
	admin := householdApp(t, s, "admin", "admin")
	operator := householdApp(t, s, "operator", "sam")

	// A non-admin cannot pair a phone for someone else.
	if code, _ := doJSON(t, operator, http.MethodPost, "/api/v1/pairing/tokens", `{"name":"Priya"}`); code != http.StatusForbidden {
		t.Fatalf("operator pairing for another person should be 403, got %d", code)
	}
	// ...but pairs their own phone as themselves.
	code, body := doJSON(t, operator, http.MethodPost, "/api/v1/pairing/tokens", "")
	if code != http.StatusOK || body["subject"] != "sam" || body["role"] != "operator" {
		t.Fatalf("own pairing token: %d %+v", code, body)
	}
	code, redeemed := doJSON(t, operator, http.MethodPost, "/api/v1/pairing/redeem", `{"code":"`+body["code"].(string)+`"}`)
	if code != http.StatusOK || redeemed["subject"] != "sam" {
		t.Fatalf("own redeem: %d %+v", code, redeemed)
	}
	if ak, err := store.Validate(t.Context(), redeemed["token"].(string)); err != nil || auth.ClaimsForAPIKey(ak).Subject != "sam" {
		t.Fatalf("sam's phone must authenticate as sam: %v %+v", err, ak)
	}

	// The admin pairs a phone for Priya as a viewer.
	code, body = doJSON(t, admin, http.MethodPost, "/api/v1/pairing/tokens", `{"name":"Priya S.","role":"viewer"}`)
	if code != http.StatusOK || body["subject"] != "priya-s" || body["display_name"] != "Priya S." || body["role"] != "viewer" {
		t.Fatalf("household token: %d %+v", code, body)
	}
	code, redeemed = doJSON(t, admin, http.MethodPost, "/api/v1/pairing/redeem", `{"code":"`+body["code"].(string)+`"}`)
	if code != http.StatusOK || redeemed["subject"] != "priya-s" || redeemed["role"] != "viewer" || redeemed["display_name"] != "Priya S." {
		t.Fatalf("household redeem: %d %+v", code, redeemed)
	}
	ak, err := store.Validate(t.Context(), redeemed["token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if cl := auth.ClaimsForAPIKey(ak); cl.Subject != "priya-s" || cl.Role != "viewer" || ak.Name != "mobile-companion (Priya S.)" {
		t.Fatalf("priya's phone identity wrong: %+v key=%+v", cl, ak)
	}

	// The owner's own phone (no name) is the owner.
	code, body = doJSON(t, admin, http.MethodPost, "/api/v1/pairing/tokens", "")
	if code != http.StatusOK || body["subject"] != "admin" {
		t.Fatalf("owner token: %d %+v", code, body)
	}
	_, redeemed = doJSON(t, admin, http.MethodPost, "/api/v1/pairing/redeem", `{"code":"`+body["code"].(string)+`"}`)
	if ak, err := store.Validate(t.Context(), redeemed["token"].(string)); err != nil || auth.ClaimsForAPIKey(ak).Subject != "admin" || ak.Name != "mobile-companion" {
		t.Fatalf("owner's phone must be admin: %v %+v", err, ak)
	}

	// Bad inputs.
	for name, payload := range map[string]string{"owner name": `{"name":"admin"}`, "bad role": `{"name":"Kai","role":"root"}`, "empty": `{"name":"!!!"}`} {
		if code, _ := doJSON(t, admin, http.MethodPost, "/api/v1/pairing/tokens", payload); code != http.StatusBadRequest {
			t.Fatalf("%s should be 400, got %d", name, code)
		}
	}

	// Members: one row per person, the owner flagged, phones counted.
	code, members := doJSON(t, admin, http.MethodGet, "/api/v1/pairing/members", "")
	if code != http.StatusOK {
		t.Fatalf("members: %d %+v", code, members)
	}
	rows, _ := members["members"].([]any)
	seen := map[string]map[string]any{}
	for _, r := range rows {
		m := r.(map[string]any)
		seen[m["subject"].(string)] = m
	}
	if len(seen) != 3 || seen["priya-s"]["display_name"] != "Priya S." || seen["priya-s"]["role"] != "viewer" || seen["admin"]["owner"] != true || seen["sam"]["phones"] != float64(1) {
		t.Fatalf("members malformed: %+v", rows)
	}
	if code, _ := doJSON(t, operator, http.MethodGet, "/api/v1/pairing/members", ""); code != http.StatusForbidden {
		t.Fatalf("members must be admin only, got %d", code)
	}
}

func TestLegacyCompanionKeysStillBelongToTheOwner(t *testing.T) {
	legacy := apikeys.APIKey{ID: "abc123", Name: "mobile-companion"}
	if cl := auth.ClaimsForAPIKey(legacy); cl.Subject != "admin" || cl.Role != "operator" {
		t.Fatalf("legacy companion key must map to the owner: %+v", cl)
	}
	other := apikeys.APIKey{ID: "k1", Name: "ci-deploy"}
	if cl := auth.ClaimsForAPIKey(other); cl.Subject != "k1" {
		t.Fatalf("other legacy keys keep their id as subject: %+v", cl)
	}
	bad := apikeys.APIKey{ID: "k2", Name: "x", Subject: "kai", Role: "root"}
	if cl := auth.ClaimsForAPIKey(bad); cl.Subject != "kai" || cl.Role != "operator" {
		t.Fatalf("unknown roles fall back to operator: %+v", cl)
	}
	if householdSubject("  Priya  S. ") != "priya-s" || householdSubject("!!!") != "" || householdSubject("Kai_2") != "kai-2" {
		t.Fatal("household subject slug wrong")
	}
}
