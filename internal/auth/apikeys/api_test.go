// api_test.go — HTTP handler tests for the apikeys API.
// Uses fiber.App.Test so no external server is needed. Each test wires a
// fresh SQLiteStore into a minimal Fiber app and exercises the handlers.
package apikeys

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// helper — build a Fiber app backed by a fresh store
// ---------------------------------------------------------------------------

func newAPIApp(t *testing.T) (*fiber.App, *SQLiteStore) {
	t.Helper()
	s := newStore(t)
	log := zap.NewNop()
	api := NewAPI(s, log)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/apikeys", api.HandleCreate)
	app.Get("/apikeys", api.HandleList)
	app.Post("/apikeys/:id/revoke", api.HandleRevoke)
	app.Post("/apikeys/validate", api.HandleValidate)
	return app, s
}

// ---------------------------------------------------------------------------
// NewAPI
// ---------------------------------------------------------------------------

func TestNewAPI_NotNil(t *testing.T) {
	s := newStore(t)
	api := NewAPI(s, zap.NewNop())
	if api == nil {
		t.Fatal("NewAPI returned nil")
	}
}

// ---------------------------------------------------------------------------
// HandleCreate
// ---------------------------------------------------------------------------

func TestHandleCreate_HappyPath(t *testing.T) {
	app, _ := newAPIApp(t)

	body := `{"name":"ci-bot","scopes":["read","write"]}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("POST /apikeys: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("Create status = %d, want 201", resp.StatusCode)
	}

	var got map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["id"] == "" {
		t.Error("Create response missing 'id'")
	}
	if got["key"] == "" {
		t.Error("Create response missing 'key' (plaintext)")
	}
	if got["name"] != "ci-bot" {
		t.Errorf("name = %v, want ci-bot", got["name"])
	}
}

func TestHandleCreate_MissingName(t *testing.T) {
	app, _ := newAPIApp(t)

	body := `{"name":"  ","scopes":[]}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Create empty name: status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleCreate_EmptyNameWhitespace(t *testing.T) {
	app, _ := newAPIApp(t)

	body := `{"name":"\t  \n"}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Create whitespace-only name: status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleCreate_InvalidBody(t *testing.T) {
	app, _ := newAPIApp(t)

	req, _ := http.NewRequest(http.MethodPost, "/apikeys", strings.NewReader("not-json{"))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Create invalid body: status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleCreate_NilScopes(t *testing.T) {
	// Scopes omitted from body — should default to nil/empty and succeed.
	app, _ := newAPIApp(t)

	body := `{"name":"no-scopes-key"}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("Create nil scopes: status = %d, want 201", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// HandleList
// ---------------------------------------------------------------------------

func TestHandleList_EmptyStore(t *testing.T) {
	app, _ := newAPIApp(t)

	req, _ := http.NewRequest(http.MethodGet, "/apikeys", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET /apikeys: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("List empty: status = %d, want 200", resp.StatusCode)
	}

	var got map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&got)
	if got["keys"] == nil {
		t.Error("List empty: 'keys' field is null, want []")
	}
}

func TestHandleList_WithKeys(t *testing.T) {
	app, s := newAPIApp(t)
	ctx := context.Background()
	s.Create(ctx, "alpha", nil)
	s.Create(ctx, "beta", nil)

	req, _ := http.NewRequest(http.MethodGet, "/apikeys", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET /apikeys: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("List: status = %d, want 200", resp.StatusCode)
	}

	var got map[string][]interface{}
	json.NewDecoder(resp.Body).Decode(&got)
	if len(got["keys"]) != 2 {
		t.Errorf("List: got %d keys, want 2", len(got["keys"]))
	}
}

func TestHandleList_IncludeRevokedQuery(t *testing.T) {
	app, s := newAPIApp(t)
	ctx := context.Background()
	_, k, _ := s.Create(ctx, "revokable", nil)
	s.Revoke(ctx, k.ID)

	// Without include_revoked — revoked key excluded.
	req, _ := http.NewRequest(http.MethodGet, "/apikeys", nil)
	resp, _ := app.Test(req)
	var got1 map[string][]interface{}
	json.NewDecoder(resp.Body).Decode(&got1)
	if len(got1["keys"]) != 0 {
		t.Errorf("List without include_revoked: got %d, want 0", len(got1["keys"]))
	}

	// With include_revoked=true — revoked key included.
	req2, _ := http.NewRequest(http.MethodGet, "/apikeys?include_revoked=true", nil)
	resp2, _ := app.Test(req2)
	var got2 map[string][]interface{}
	json.NewDecoder(resp2.Body).Decode(&got2)
	if len(got2["keys"]) != 1 {
		t.Errorf("List with include_revoked=true: got %d, want 1", len(got2["keys"]))
	}
}

// ---------------------------------------------------------------------------
// HandleRevoke
// ---------------------------------------------------------------------------

func TestHandleRevoke_HappyPath(t *testing.T) {
	app, s := newAPIApp(t)
	ctx := context.Background()
	_, key, _ := s.Create(ctx, "revoke-me", nil)

	req, _ := http.NewRequest(http.MethodPost, "/apikeys/"+key.ID+"/revoke", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("POST revoke: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Revoke status = %d, want 200", resp.StatusCode)
	}

	var got map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&got)
	if got["status"] != "revoked" {
		t.Errorf("Revoke response status = %v, want 'revoked'", got["status"])
	}
	if got["id"] != key.ID {
		t.Errorf("Revoke response id = %v, want %q", got["id"], key.ID)
	}
}

func TestHandleRevoke_NotFound(t *testing.T) {
	app, _ := newAPIApp(t)

	req, _ := http.NewRequest(http.MethodPost, "/apikeys/nonexistent-id/revoke", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Revoke missing ID: status = %d, want 404", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// HandleValidate
// ---------------------------------------------------------------------------

func TestHandleValidate_HappyPath(t *testing.T) {
	app, s := newAPIApp(t)
	ctx := context.Background()
	plaintext, key, _ := s.Create(ctx, "validator", []string{"read"})

	body := `{"key":"` + plaintext + `"}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys/validate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("POST validate: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Validate status = %d, want 200", resp.StatusCode)
	}

	var got map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&got)
	if got["id"] != key.ID {
		t.Errorf("Validate response id = %v, want %q", got["id"], key.ID)
	}
}

func TestHandleValidate_InvalidKey(t *testing.T) {
	app, _ := newAPIApp(t)

	body := `{"key":"sk_totally_wrong_key"}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys/validate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Validate bad key: status = %d, want 401", resp.StatusCode)
	}
}

func TestHandleValidate_RevokedKey(t *testing.T) {
	app, s := newAPIApp(t)
	ctx := context.Background()
	plaintext, key, _ := s.Create(ctx, "to-revoke", nil)
	s.Revoke(ctx, key.ID)

	body := `{"key":"` + plaintext + `"}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys/validate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Validate revoked key: status = %d, want 401", resp.StatusCode)
	}
}

func TestHandleValidate_MissingKey(t *testing.T) {
	app, _ := newAPIApp(t)

	body := `{"key":""}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys/validate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Validate empty key: status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleValidate_InvalidBody(t *testing.T) {
	app, _ := newAPIApp(t)

	req, _ := http.NewRequest(http.MethodPost, "/apikeys/validate", strings.NewReader("bad-json"))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Validate invalid body: status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleCreate_ResponseContainsPrefix(t *testing.T) {
	app, _ := newAPIApp(t)

	body := `{"name":"prefix-test"}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Create: status = %d", resp.StatusCode)
	}

	var got map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&got)

	prefix, _ := got["prefix"].(string)
	key, _ := got["key"].(string)
	if prefix == "" {
		t.Error("Create response missing 'prefix'")
	}
	if key == "" {
		t.Error("Create response missing 'key'")
	}
	if len(key) >= 8 && prefix != key[:8] {
		t.Errorf("prefix %q != key[:8] %q", prefix, key[:8])
	}
}
