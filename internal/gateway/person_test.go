package gateway

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/person"
)

// personApp mounts the person routes behind fixed claims, like householdApp.
func personApp(t *testing.T, s *Server, role, subject string) *fiber.App {
	t.Helper()
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Request().Header.Set("Authorization", "Bearer test")
		auth.SetClaims(c, &auth.Claims{Role: role, RegisteredClaims: jwt.RegisteredClaims{Subject: subject}})
		return c.Next()
	})
	api := app.Group("/api/v1")
	api.Get("/person/model/sync", s.handlePersonSync)
	api.Get("/person/model", s.handlePersonModel)
	api.Put("/person/model/entries", s.handlePersonPut)
	api.Delete("/person/model/entries/:section/:key", s.handlePersonDelete)
	api.Delete("/person/model", s.handlePersonPurge)
	api.Get("/person/senses", s.handlePersonSenses)
	api.Put("/person/senses/:sense", s.handlePersonSetSense)
	api.Post("/person/observations", s.handlePersonObservations)
	return app
}

func personGateway(t *testing.T) *Server {
	t.Helper()
	s, _ := newTestGatewayWithLLM(t, "secret")
	store, err := person.OpenSQLite(filepath.Join(t.TempDir(), "person.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.SetPersonModel(store)
	return s
}

func TestPersonModelRoundTripsAndRendersProse(t *testing.T) {
	s := personGateway(t)
	kai := personApp(t, s, "operator", "kai")

	code, body := doJSON(t, kai, http.MethodPut, "/api/v1/person/model/entries",
		`{"section":"identity","key":"home","summary":"Home is 14 Oak Street"}`)
	if code != http.StatusOK || body["applied"] != true {
		t.Fatalf("put: %d %+v", code, body)
	}
	entry, _ := body["entry"].(map[string]any)
	if entry["source"] != person.SourceManual {
		t.Fatalf("a person typing into the UI is a manual source: %+v", entry)
	}

	code, body = doJSON(t, kai, http.MethodGet, "/api/v1/person/model", "")
	if code != http.StatusOK {
		t.Fatalf("get: %d %+v", code, body)
	}
	summary, _ := body["summary"].(string)
	if summary == "" || summary[0] == '{' {
		t.Fatalf("the model must come back as prose too: %q", summary)
	}
	if entries, _ := body["entries"].([]any); len(entries) != 1 {
		t.Fatalf("entries: %+v", body["entries"])
	}
}

func TestPersonModelRefusesToOverwriteWhatThePersonSaid(t *testing.T) {
	s := personGateway(t)
	kai := personApp(t, s, "operator", "kai")

	if code, _ := doJSON(t, kai, http.MethodPut, "/api/v1/person/model/entries",
		`{"section":"identity","key":"home","summary":"Home is 14 Oak Street"}`); code != http.StatusOK {
		t.Fatalf("manual put: %d", code)
	}
	// An observer reporting a different home must be told it lost, with the
	// entry that stands — 409, not a silent success.
	code, body := doJSON(t, kai, http.MethodPut, "/api/v1/person/model/entries",
		`{"section":"identity","key":"home","summary":"Home is near Elm Street","source":"sense:location","confidence":0.4}`)
	if code != http.StatusConflict || body["applied"] != false {
		t.Fatalf("a sense must not overwrite a manual entry: %d %+v", code, body)
	}
	entry, _ := body["entry"].(map[string]any)
	if entry["summary"] != "Home is 14 Oak Street" {
		t.Fatalf("the conflict response must carry what stands: %+v", entry)
	}
}

func TestPersonModelIsPrivateToEachHouseholdMember(t *testing.T) {
	s := personGateway(t)
	kai := personApp(t, s, "operator", "kai")
	priya := personApp(t, s, "operator", "priya-s")

	if code, _ := doJSON(t, kai, http.MethodPut, "/api/v1/person/model/entries",
		`{"section":"preferences","key":"seats","summary":"Never a middle seat"}`); code != http.StatusOK {
		t.Fatal("kai's put failed")
	}
	code, body := doJSON(t, priya, http.MethodGet, "/api/v1/person/model", "")
	if code != http.StatusOK {
		t.Fatalf("priya get: %d", code)
	}
	if entries, _ := body["entries"].([]any); len(entries) != 0 {
		t.Fatalf("one member must not see another's model: %+v", entries)
	}
	// An operator cannot ask for someone else's model by name either.
	if code, _ := doJSON(t, priya, http.MethodGet, "/api/v1/person/model?owner=kai", ""); code != http.StatusForbidden {
		t.Fatalf("?owner= must be admin-only, got %d", code)
	}
	// An admin may.
	admin := personApp(t, s, "admin", "admin")
	code, body = doJSON(t, admin, http.MethodGet, "/api/v1/person/model?owner=kai", "")
	if code != http.StatusOK || body["owner"] != "kai" {
		t.Fatalf("admin inspection: %d %+v", code, body)
	}
}

func TestPersonModelSyncAndForget(t *testing.T) {
	s := personGateway(t)
	kai := personApp(t, s, "operator", "kai")

	for _, payload := range []string{
		`{"section":"identity","key":"home","summary":"Home is 14 Oak Street"}`,
		`{"section":"preferences","key":"seats","summary":"Never a middle seat"}`,
	} {
		if code, _ := doJSON(t, kai, http.MethodPut, "/api/v1/person/model/entries", payload); code != http.StatusOK {
			t.Fatalf("put %s", payload)
		}
	}

	code, body := doJSON(t, kai, http.MethodGet, "/api/v1/person/model/sync?since=0", "")
	if code != http.StatusOK {
		t.Fatalf("sync: %d %+v", code, body)
	}
	changes, _ := body["changes"].([]any)
	if len(changes) != 2 {
		t.Fatalf("first sync should carry both entries: %+v", changes)
	}
	cursor, _ := body["next_cursor"].(float64)
	if cursor == 0 {
		t.Fatal("sync must return a cursor")
	}

	// Deleting one reaches the device as a tombstone.
	if code, _ := doJSON(t, kai, http.MethodDelete, "/api/v1/person/model/entries/preferences/seats", ""); code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
	_, body = doJSON(t, kai, http.MethodGet, "/api/v1/person/model/sync?since="+jsonNumber(cursor), "")
	changes, _ = body["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("incremental sync: %+v", changes)
	}
	change, _ := changes[0].(map[string]any)
	if change["deleted"] != true {
		t.Fatalf("deletion must be a tombstone: %+v", change)
	}

	// Forgetting needs an explicit confirmation.
	if code, _ := doJSON(t, kai, http.MethodDelete, "/api/v1/person/model", ""); code != http.StatusBadRequest {
		t.Fatalf("purge without confirm should be refused, got %d", code)
	}
	code, body = doJSON(t, kai, http.MethodDelete, "/api/v1/person/model?confirm=true", "")
	if code != http.StatusOK || body["removed"] != float64(1) {
		t.Fatalf("purge: %d %+v", code, body)
	}
}

func TestPersonModelOffSaysSo(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	kai := personApp(t, s, "operator", "kai")
	if code, _ := doJSON(t, kai, http.MethodGet, "/api/v1/person/model", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("a gateway without the model should answer 503, got %d", code)
	}
}

func jsonNumber(v float64) string {
	b, _ := json.Marshal(int64(v))
	return string(b)
}

func TestPersonSensesAreOffUntilAsked(t *testing.T) {
	s := personGateway(t)
	kai := personApp(t, s, "operator", "kai")

	code, body := doJSON(t, kai, http.MethodGet, "/api/v1/person/senses", "")
	if code != http.StatusOK {
		t.Fatalf("senses: %d %+v", code, body)
	}
	senses, _ := body["senses"].([]any)
	if len(senses) == 0 {
		t.Fatal("the switches must be listed even when all are off")
	}
	for _, raw := range senses {
		sense, _ := raw.(map[string]any)
		if sense["enabled"] != false {
			t.Fatalf("nothing is watched until asked: %+v", sense)
		}
		if purpose, _ := sense["purpose"].(string); purpose == "" {
			t.Fatalf("a consent switch with no stated purpose is not consent: %+v", sense)
		}
	}
	if code, _ := doJSON(t, kai, http.MethodPut, "/api/v1/person/senses/astrology", `{"enabled":true}`); code != http.StatusNotFound {
		t.Fatalf("unknown sense should 404, got %d", code)
	}
}

func TestPersonObservationsAreIgnoredWithoutConsentAndDigestedWithIt(t *testing.T) {
	s := personGateway(t)
	kai := personApp(t, s, "operator", "kai")
	focus := `{"observations":[{"kind":"focus","payload":{"mode":"Work"}}]}`

	// No consent: recorded nothing, concluded nothing.
	code, body := doJSON(t, kai, http.MethodPost, "/api/v1/person/observations", focus)
	if code != http.StatusOK || body["recorded"] != float64(0) || body["ignored"] != float64(1) {
		t.Fatalf("signals without consent must be ignored: %d %+v", code, body)
	}
	if code, body := doJSON(t, kai, http.MethodGet, "/api/v1/person/model", ""); code != http.StatusOK {
		t.Fatalf("model: %d %+v", code, body)
	} else if entries, _ := body["entries"].([]any); len(entries) != 0 {
		t.Fatalf("nothing should have been concluded: %+v", entries)
	}

	// Consent given: the same push is digested into the model.
	if code, _ := doJSON(t, kai, http.MethodPut, "/api/v1/person/senses/state", `{"enabled":true}`); code != http.StatusOK {
		t.Fatal("enabling the state sense failed")
	}
	code, body = doJSON(t, kai, http.MethodPost, "/api/v1/person/observations", focus)
	if code != http.StatusOK || body["recorded"] != float64(1) || body["applied"] != float64(1) {
		t.Fatalf("digest: %d %+v", code, body)
	}
	code, body = doJSON(t, kai, http.MethodGet, "/api/v1/person/model", "")
	summary, _ := body["summary"].(string)
	if code != http.StatusOK || !strings.Contains(summary, "In Work Focus") {
		t.Fatalf("the model should now know: %d %q", code, summary)
	}

	// Withdrawing consent removes what the sense concluded.
	code, body = doJSON(t, kai, http.MethodPut, "/api/v1/person/senses/state", `{"enabled":false}`)
	if code != http.StatusOK || body["forgotten"] != float64(1) {
		t.Fatalf("withdrawing consent should forget the inference: %d %+v", code, body)
	}
	code, body = doJSON(t, kai, http.MethodGet, "/api/v1/person/model", "")
	if code != http.StatusOK {
		t.Fatalf("the model is still readable after a sense is switched off: %d", code)
	}
	if entries, _ := body["entries"].([]any); len(entries) != 0 {
		t.Fatalf("the conclusion must go with the consent: %+v", entries)
	}
}

func TestPersonObservationsRejectNonsense(t *testing.T) {
	s := personGateway(t)
	kai := personApp(t, s, "operator", "kai")
	if code, _ := doJSON(t, kai, http.MethodPut, "/api/v1/person/senses/state", `{"enabled":true}`); code != http.StatusOK {
		t.Fatal("enable failed")
	}
	if code, _ := doJSON(t, kai, http.MethodPost, "/api/v1/person/observations", `{"observations":[]}`); code != http.StatusBadRequest {
		t.Fatalf("an empty batch should be refused, got %d", code)
	}
	future := `{"observations":[{"kind":"focus","at":"2099-01-01T00:00:00Z","payload":{"mode":"Work"}}]}`
	if code, _ := doJSON(t, kai, http.MethodPost, "/api/v1/person/observations", future); code != http.StatusBadRequest {
		t.Fatalf("a signal from the future should be refused, got %d", code)
	}
}

func TestPersonObservationsCannotBeAttributedToSomeoneElse(t *testing.T) {
	s := personGateway(t)
	kai := personApp(t, s, "operator", "kai")
	priya := personApp(t, s, "operator", "priya-s")
	for _, app := range []*fiber.App{kai, priya} {
		if code, _ := doJSON(t, app, http.MethodPut, "/api/v1/person/senses/state", `{"enabled":true}`); code != http.StatusOK {
			t.Fatal("enable failed")
		}
	}
	// Kai's phone claims the observation belongs to Priya. The owner comes
	// from the credential, never the body.
	spoofed := `{"observations":[{"kind":"focus","owner":"priya-s","payload":{"mode":"Work"}}]}`
	if code, _ := doJSON(t, kai, http.MethodPost, "/api/v1/person/observations", spoofed); code != http.StatusOK {
		t.Fatal("post failed")
	}
	_, body := doJSON(t, priya, http.MethodGet, "/api/v1/person/model", "")
	if entries, _ := body["entries"].([]any); len(entries) != 0 {
		t.Fatalf("one member must not write another's model: %+v", entries)
	}
	_, body = doJSON(t, kai, http.MethodGet, "/api/v1/person/model", "")
	if entries, _ := body["entries"].([]any); len(entries) != 1 {
		t.Fatalf("the signal belongs to the caller: %+v", entries)
	}
}
