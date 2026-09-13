package credentials

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/httptestutil"
	"go.uber.org/zap"
)

// newTestVault creates a temporary SQLiteVault backed by a PassthroughKMS with
// a fixed 32-byte all-zero key. The test's TempDir is used for the DB file so
// the file is cleaned up automatically.
func newTestVault(t *testing.T) *SQLiteVault {
	t.Helper()
	kms, err := NewPassthroughKMS(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewPassthroughKMS: %v", err)
	}
	path := t.TempDir() + "/creds.db"
	v, err := NewSQLiteVault(path, kms)
	if err != nil {
		t.Fatalf("NewSQLiteVault: %v", err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
}

// ---------------------------------------------------------------------------
// NewSQLiteVault
// ---------------------------------------------------------------------------

func TestNewSQLiteVaultCreatesVault(t *testing.T) {
	v := newTestVault(t)
	if v == nil {
		t.Fatal("expected non-nil vault")
	}
}

func TestNewSQLiteVaultUsesOwnerOnlyPermissions(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/creds.db"
	kms, _ := NewPassthroughKMS(bytes.Repeat([]byte{1}, 32))
	v, err := NewSQLiteVault(path, kms)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("vault mode = %o, want 600", got)
	}
}

func TestNewSQLiteVaultInvalidPath(t *testing.T) {
	kms, _ := NewPassthroughKMS(make([]byte, 32))
	// Use a path inside a non-existent directory.
	_, err := NewSQLiteVault("/nonexistent/dir/that/does/not/exist/creds.db", kms)
	if err == nil {
		t.Fatal("expected error for invalid path, got nil")
	}
}

// ---------------------------------------------------------------------------
// Set + Get round-trip
// ---------------------------------------------------------------------------

func TestSetGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	want := []byte("super-secret-api-key")
	if err := v.Set(ctx, "agent-a", "api_key", want); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := v.Get(ctx, "agent-a", "api_key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Get returned %q, want %q", got, want)
	}
}

func TestSetGetEmptyValue(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	if err := v.Set(ctx, "agent-a", "empty_key", []byte{}); err != nil {
		t.Fatalf("Set empty value: %v", err)
	}
	got, err := v.Get(ctx, "agent-a", "empty_key")
	if err != nil {
		t.Fatalf("Get empty value: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %q", got)
	}
}

func TestSetGetBinaryValue(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	want := []byte{0x00, 0xFF, 0xAB, 0xCD, 0x01, 0x02, 0x03}
	if err := v.Set(ctx, "agent-b", "binary_key", want); err != nil {
		t.Fatalf("Set binary: %v", err)
	}
	got, err := v.Get(ctx, "agent-b", "binary_key")
	if err != nil {
		t.Fatalf("Get binary: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Get binary returned %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Get for missing key → ErrNotFound
// ---------------------------------------------------------------------------

func TestGetMissingKeyReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_, err := v.Get(ctx, "no-such-agent", "no-such-key")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing key: got %v, want ErrNotFound", err)
	}
}

func TestGetMissingKeyForExistingAgent(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-x", "known_key", []byte("value"))

	_, err := v.Get(ctx, "agent-x", "unknown_key")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing key on existing agent: got %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestDeleteExistingKey(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-a", "key1", []byte("value1"))

	if err := v.Delete(ctx, "agent-a", "key1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := v.Get(ctx, "agent-a", "key1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("after Delete, Get returned %v, want ErrNotFound", err)
	}
}

func TestDeleteNonExistentKeyIsNoop(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	// Deleting a key that never existed must return nil (not an error).
	if err := v.Delete(ctx, "ghost-agent", "ghost-key"); err != nil {
		t.Errorf("Delete non-existent key: got %v, want nil", err)
	}
}

func TestDeleteDoesNotAffectOtherKeys(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-a", "key1", []byte("v1"))
	_ = v.Set(ctx, "agent-a", "key2", []byte("v2"))

	_ = v.Delete(ctx, "agent-a", "key1")

	got, err := v.Get(ctx, "agent-a", "key2")
	if err != nil {
		t.Fatalf("Get key2 after deleting key1: %v", err)
	}
	if !bytes.Equal(got, []byte("v2")) {
		t.Errorf("key2 corrupted: got %q, want %q", got, "v2")
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestListReturnsAllKeysForAgent(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	keys := []string{"alpha", "beta", "gamma"}
	for _, k := range keys {
		_ = v.Set(ctx, "agent-list", k, []byte("value-"+k))
	}

	got, err := v.List(ctx, "agent-list")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// List returns keys ORDER BY key, so sort our expectation to match.
	want := make([]string, len(keys))
	copy(want, keys)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("List returned %d keys, want %d; got %v", len(got), len(want), got)
	}
	for i, k := range got {
		if k != want[i] {
			t.Errorf("List[%d] = %q, want %q", i, k, want[i])
		}
	}
}

func TestListReturnsEmptySliceForUnknownAgent(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	got, err := v.List(ctx, "totally-unknown-agent")
	if err != nil {
		t.Fatalf("List unknown agent: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List unknown agent returned %v, want empty", got)
	}
}

func TestListAfterDeleteOmitsDeletedKey(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-del", "keep", []byte("v"))
	_ = v.Set(ctx, "agent-del", "remove", []byte("v"))
	_ = v.Delete(ctx, "agent-del", "remove")

	got, err := v.List(ctx, "agent-del")
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(got) != 1 || got[0] != "keep" {
		t.Errorf("List after delete = %v, want [keep]", got)
	}
}

// ---------------------------------------------------------------------------
// Cross-agent isolation
// ---------------------------------------------------------------------------

func TestCrossAgentIsolation(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-a", "secret", []byte("value-for-a"))
	_ = v.Set(ctx, "agent-b", "secret", []byte("value-for-b"))

	// agent-a's value must not be readable as agent-b's.
	gotA, err := v.Get(ctx, "agent-a", "secret")
	if err != nil {
		t.Fatalf("Get agent-a: %v", err)
	}
	if !bytes.Equal(gotA, []byte("value-for-a")) {
		t.Errorf("agent-a secret = %q, want value-for-a", gotA)
	}

	gotB, err := v.Get(ctx, "agent-b", "secret")
	if err != nil {
		t.Fatalf("Get agent-b: %v", err)
	}
	if !bytes.Equal(gotB, []byte("value-for-b")) {
		t.Errorf("agent-b secret = %q, want value-for-b", gotB)
	}
}

func TestCrossAgentListIsolation(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-a", "key1", []byte("v"))
	_ = v.Set(ctx, "agent-a", "key2", []byte("v"))
	_ = v.Set(ctx, "agent-b", "keyX", []byte("v"))

	listA, _ := v.List(ctx, "agent-a")
	listB, _ := v.List(ctx, "agent-b")

	if len(listA) != 2 {
		t.Errorf("agent-a list len = %d, want 2; got %v", len(listA), listA)
	}
	if len(listB) != 1 {
		t.Errorf("agent-b list len = %d, want 1; got %v", len(listB), listB)
	}
	for _, k := range listA {
		for _, kb := range listB {
			if k == kb {
				t.Errorf("cross-agent key leak: %q appears in both lists", k)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Multiple values for same agent
// ---------------------------------------------------------------------------

func TestMultipleValuesForSameAgent(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	entries := map[string][]byte{
		"db_pass": []byte("pg-secret"),
		"api_key": []byte("sk-12345"),
		"token":   []byte("bearer-abc"),
	}

	for k, val := range entries {
		if err := v.Set(ctx, "multi-agent", k, val); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	for k, want := range entries {
		got, err := v.Get(ctx, "multi-agent", k)
		if err != nil {
			t.Fatalf("Get %q: %v", k, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("Get %q = %q, want %q", k, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Overwrite (upsert) — Set twice on same key, Get returns latest value
// ---------------------------------------------------------------------------

func TestSetOverwriteReturnsLatestValue(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-a", "key", []byte("original"))
	_ = v.Set(ctx, "agent-a", "key", []byte("updated"))

	got, err := v.Get(ctx, "agent-a", "key")
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}
	if !bytes.Equal(got, []byte("updated")) {
		t.Errorf("Get after overwrite = %q, want updated", got)
	}
}

func TestSetOverwriteDoesNotDuplicateInList(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-a", "key", []byte("v1"))
	_ = v.Set(ctx, "agent-a", "key", []byte("v2"))

	keys, err := v.List(ctx, "agent-a")
	if err != nil {
		t.Fatalf("List after overwrite: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("List after overwrite: got %v, want exactly one key", keys)
	}
}

// ---------------------------------------------------------------------------
// WriteBlob / ReadBlob delegates
// ---------------------------------------------------------------------------

func TestWriteBlobReadBlobRoundTrip(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	data := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	if err := v.WriteBlob(ctx, "agent-a", "blob_key", data); err != nil {
		t.Fatalf("WriteBlob: %v", err)
	}

	got, err := v.ReadBlob(ctx, "agent-a", "blob_key")
	if err != nil {
		t.Fatalf("ReadBlob: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("ReadBlob = %v, want %v", got, data)
	}
}

func TestReadBlobMissingReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_, err := v.ReadBlob(ctx, "agent-a", "missing_blob")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadBlob missing: got %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestCloseIsIdempotentish(t *testing.T) {
	kms, _ := NewPassthroughKMS(make([]byte, 32))
	v, err := NewSQLiteVault(t.TempDir()+"/creds.db", kms)
	if err != nil {
		t.Fatalf("NewSQLiteVault: %v", err)
	}
	if err := v.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Versioning / Rotation (rotation.go coverage)
// ---------------------------------------------------------------------------

func TestRotateIncreasesVersionNumber(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-r", "rotated_key", []byte("secret"))

	v1, err := v.Rotate(ctx, "agent-r", "rotated_key")
	if err != nil {
		t.Fatalf("Rotate v1: %v", err)
	}
	v2, err := v.Rotate(ctx, "agent-r", "rotated_key")
	if err != nil {
		t.Fatalf("Rotate v2: %v", err)
	}
	if v2 <= v1 {
		t.Errorf("Rotate v2 (%d) should be > v1 (%d)", v2, v1)
	}
}

func TestRotatePreservesPlaintext(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	want := []byte("preserved-value")
	_ = v.Set(ctx, "agent-r", "my_key", want)
	_, _ = v.Rotate(ctx, "agent-r", "my_key")

	got, err := v.Get(ctx, "agent-r", "my_key")
	if err != nil {
		t.Fatalf("Get after Rotate: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Get after Rotate = %q, want %q", got, want)
	}
}

func TestRotateMissingKeyReturnsError(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_, err := v.Rotate(ctx, "agent-r", "no_such_key")
	if err == nil {
		t.Fatal("Rotate non-existent key: expected error, got nil")
	}
}

func TestListVersionsReturnsVersions(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-r", "versioned", []byte("v"))
	_, _ = v.Rotate(ctx, "agent-r", "versioned")
	_, _ = v.Rotate(ctx, "agent-r", "versioned")

	versions, err := v.ListVersions(ctx, "agent-r", "versioned")
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) < 2 {
		t.Errorf("ListVersions: got %d versions, want >= 2", len(versions))
	}
}

func TestListVersionsEmptyForUnknownKey(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	versions, err := v.ListVersions(ctx, "agent-r", "no_such_key")
	if err != nil {
		t.Fatalf("ListVersions unknown key: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("ListVersions unknown key: got %v, want empty", versions)
	}
}

func TestDeleteVersionRemovesInactiveVersion(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-r", "dv_key", []byte("data"))
	v1, _ := v.Rotate(ctx, "agent-r", "dv_key")
	_, _ = v.Rotate(ctx, "agent-r", "dv_key") // v1 is now inactive

	err := v.DeleteVersion(ctx, "agent-r", "dv_key", v1)
	if err != nil {
		t.Fatalf("DeleteVersion inactive: %v", err)
	}
}

func TestDeleteVersionActiveReturnsError(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-r", "active_key", []byte("data"))
	ver, _ := v.Rotate(ctx, "agent-r", "active_key")

	// ver is the current active version — deleting it must fail.
	err := v.DeleteVersion(ctx, "agent-r", "active_key", ver)
	if err == nil {
		t.Fatal("DeleteVersion active: expected error, got nil")
	}
}

func TestDeleteVersionNonExistentReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-r", "some_key", []byte("data"))
	_, _ = v.Rotate(ctx, "agent-r", "some_key")

	err := v.DeleteVersion(ctx, "agent-r", "some_key", 9999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteVersion non-existent: got %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// DefaultConfig
// ---------------------------------------------------------------------------

func TestDefaultConfigKMSProvider(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.KMSProvider != "local" {
		t.Errorf("DefaultConfig.KMSProvider = %q, want local", cfg.KMSProvider)
	}
}

// ---------------------------------------------------------------------------
// API handler tests (fiber.App.Test — no httptest.NewServer)
// ---------------------------------------------------------------------------

// newAPIApp wires a fresh vault into a minimal Fiber app with the four
// credential routes and returns both.
func newAPIApp(t *testing.T) (*fiber.App, *SQLiteVault) {
	t.Helper()
	v := newTestVault(t)
	log := zap.NewNop()
	api := NewAPI(v, log)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/credentials/:agentID", api.HandleSet)
	app.Get("/credentials/:agentID", api.HandleList)
	app.Get("/credentials/:agentID/:key", api.HandleGet)
	app.Delete("/credentials/:agentID/:key", api.HandleDelete)
	return app, v
}

func TestAPIHandleSetAndGet(t *testing.T) {
	app, _ := newAPIApp(t)

	// POST to set a credential.
	body := `{"key":"api_key","value":"` + base64.StdEncoding.EncodeToString([]byte("my-secret")) + `"}`
	req, _ := http.NewRequest(http.MethodPost, "/credentials/agent-a", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(httptestutil.WithHost(req))
	if err != nil {
		t.Fatalf("POST /credentials/agent-a: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("POST status = %d, want 204", resp.StatusCode)
	}

	// GET the credential back.
	req2, _ := http.NewRequest(http.MethodGet, "/credentials/agent-a/api_key", nil)
	resp2, err := app.Test(httptestutil.WithHost(req2))
	if err != nil {
		t.Fatalf("GET /credentials/agent-a/api_key: %v", err)
	}
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("GET status = %d, want 200", resp2.StatusCode)
	}

	var got map[string]string
	if err := json.NewDecoder(resp2.Body).Decode(&got); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(got["value"])
	if err != nil {
		t.Fatalf("decode value: %v", err)
	}
	if string(decoded) != "my-secret" {
		t.Errorf("GET value = %q, want my-secret", string(decoded))
	}
}

func TestAPIHandleSetMissingKey(t *testing.T) {
	app, _ := newAPIApp(t)
	body := `{"key":"","value":"` + base64.StdEncoding.EncodeToString([]byte("v")) + `"}`
	req, _ := http.NewRequest(http.MethodPost, "/credentials/agent-a", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(httptestutil.WithHost(req))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST empty key status = %d, want 400", resp.StatusCode)
	}
}

func TestAPIHandleSetInvalidBase64(t *testing.T) {
	app, _ := newAPIApp(t)
	body := `{"key":"k","value":"not-valid-base64!!!"}`
	req, _ := http.NewRequest(http.MethodPost, "/credentials/agent-a", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(httptestutil.WithHost(req))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST invalid base64 status = %d, want 400", resp.StatusCode)
	}
}

func TestAPIHandleSetInvalidBody(t *testing.T) {
	app, _ := newAPIApp(t)
	req, _ := http.NewRequest(http.MethodPost, "/credentials/agent-a", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(httptestutil.WithHost(req))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST invalid body status = %d, want 400", resp.StatusCode)
	}
}

func TestAPIHandleList(t *testing.T) {
	app, v := newAPIApp(t)
	ctx := context.Background()
	_ = v.Set(ctx, "agent-l", "k1", []byte("v"))
	_ = v.Set(ctx, "agent-l", "k2", []byte("v"))

	req, _ := http.NewRequest(http.MethodGet, "/credentials/agent-l", nil)
	resp, err := app.Test(httptestutil.WithHost(req))
	if err != nil {
		t.Fatalf("GET /credentials/agent-l: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("List status = %d, want 200", resp.StatusCode)
	}
	var got map[string][]string
	json.NewDecoder(resp.Body).Decode(&got)
	if len(got["keys"]) != 2 {
		t.Errorf("List keys = %v, want 2 items", got["keys"])
	}
}

func TestAPIHandleListEmpty(t *testing.T) {
	app, _ := newAPIApp(t)
	req, _ := http.NewRequest(http.MethodGet, "/credentials/unknown-agent", nil)
	resp, _ := app.Test(httptestutil.WithHost(req))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("List empty status = %d, want 200", resp.StatusCode)
	}
	var got map[string][]string
	json.NewDecoder(resp.Body).Decode(&got)
	// Should return an empty array, not null.
	if got["keys"] == nil {
		t.Error("List empty: keys field is null, want []")
	}
}

func TestAPIHandleGetNotFound(t *testing.T) {
	app, _ := newAPIApp(t)
	req, _ := http.NewRequest(http.MethodGet, "/credentials/agent-a/missing_key", nil)
	resp, _ := app.Test(httptestutil.WithHost(req))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET missing key status = %d, want 404", resp.StatusCode)
	}
}

func TestAPIHandleDelete(t *testing.T) {
	app, v := newAPIApp(t)
	ctx := context.Background()
	_ = v.Set(ctx, "agent-d", "del_key", []byte("value"))

	req, _ := http.NewRequest(http.MethodDelete, "/credentials/agent-d/del_key", nil)
	resp, err := app.Test(httptestutil.WithHost(req))
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE status = %d, want 204", resp.StatusCode)
	}

	// Verify deleted.
	_, err = v.Get(ctx, "agent-d", "del_key")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("after DELETE, Get returned %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// LazyAPI tests
// ---------------------------------------------------------------------------

// mockVaultProvider implements VaultProvider for testing LazyAPI.
type mockVaultProvider struct {
	v Vault
}

func (m *mockVaultProvider) CredentialVault() Vault { return m.v }

func newLazyAPIApp(t *testing.T, v Vault) *fiber.App {
	t.Helper()
	log := zap.NewNop()
	provider := &mockVaultProvider{v: v}
	lazy := NewLazyAPI(provider, log)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/credentials/:agentID", lazy.HandleSet)
	app.Get("/credentials/:agentID", lazy.HandleList)
	app.Get("/credentials/:agentID/:key", lazy.HandleGet)
	app.Delete("/credentials/:agentID/:key", lazy.HandleDelete)
	return app
}

func TestLazyAPIHandleSetAndGet(t *testing.T) {
	v := newTestVault(t)
	app := newLazyAPIApp(t, v)

	body := `{"key":"lazy_key","value":"` + base64.StdEncoding.EncodeToString([]byte("lazy-val")) + `"}`
	req, _ := http.NewRequest(http.MethodPost, "/credentials/agent-lazy", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(httptestutil.WithHost(req))
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("LazyAPI POST status = %d, want 204", resp.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodGet, "/credentials/agent-lazy/lazy_key", nil)
	resp2, _ := app.Test(httptestutil.WithHost(req2))
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("LazyAPI GET status = %d, want 200", resp2.StatusCode)
	}
}

func TestLazyAPIHandleListAndDelete(t *testing.T) {
	v := newTestVault(t)
	app := newLazyAPIApp(t, v)
	ctx := context.Background()
	_ = v.Set(ctx, "agent-lz", "k", []byte("v"))

	req, _ := http.NewRequest(http.MethodGet, "/credentials/agent-lz", nil)
	resp, _ := app.Test(httptestutil.WithHost(req))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("LazyAPI List status = %d, want 200", resp.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodDelete, "/credentials/agent-lz/k", nil)
	resp2, _ := app.Test(httptestutil.WithHost(req2))
	if resp2.StatusCode != http.StatusNoContent {
		t.Errorf("LazyAPI Delete status = %d, want 204", resp2.StatusCode)
	}
}

func TestLazyAPIVaultNilReturnsServiceUnavailable(t *testing.T) {
	log := zap.NewNop()
	provider := &mockVaultProvider{v: nil}
	lazy := NewLazyAPI(provider, log)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/credentials/:agentID", lazy.HandleSet)
	app.Get("/credentials/:agentID", lazy.HandleList)
	app.Get("/credentials/:agentID/:key", lazy.HandleGet)
	app.Delete("/credentials/:agentID/:key", lazy.HandleDelete)

	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodPost, "/credentials/agent-a"},
		{http.MethodGet, "/credentials/agent-a"},
		{http.MethodGet, "/credentials/agent-a/key"},
		{http.MethodDelete, "/credentials/agent-a/key"},
	} {
		req, _ := http.NewRequest(tc.method, tc.path, strings.NewReader(`{"key":"k","value":""}`))
		req.Header.Set("Content-Type", "application/json")
		resp, _ := app.Test(httptestutil.WithHost(req))
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503", tc.method, tc.path, resp.StatusCode)
		}
	}
}

func TestNewAPIAndNewLazyAPINotNil(t *testing.T) {
	v := newTestVault(t)
	log := zap.NewNop()
	api := NewAPI(v, log)
	if api == nil {
		t.Error("NewAPI returned nil")
	}
	provider := &mockVaultProvider{v: v}
	lazy := NewLazyAPI(provider, log)
	if lazy == nil {
		t.Error("NewLazyAPI returned nil")
	}
}
