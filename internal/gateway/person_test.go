package gateway

import (
	"encoding/json"
	"net/http"
	"path/filepath"
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
