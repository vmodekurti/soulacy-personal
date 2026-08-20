package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/apiversion"
	"github.com/soulacy/soulacy/internal/requestctx"
)

func idempotentApp(t *testing.T, workspaceID string, handler fiber.Handler) (*fiber.App, *Server) {
	t.Helper()
	s := &Server{log: zap.NewNop(), idempotency: newIdempotencyStore()}
	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		identity, err := requestctx.New(requestctx.Input{
			Subject: "usr_a", OrganizationID: "org_a", WorkspaceID: workspaceID,
			MembershipID: "mem_a", Role: "owner", RequestID: "req_" + workspaceID,
		})
		if err != nil {
			t.Fatal(err)
		}
		c.Locals(workspaceIdentityLocal, identity)
		c.Locals("request_id", "req_"+workspaceID)
		return c.Next()
	})
	app.Use(s.idempotencyMW())
	app.Post("/things", handler)
	app.Get("/things", handler)
	return app, s
}

func postWithKey(t *testing.T, app *fiber.App, key, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/things", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// The whole point: a client that retries after a network failure must not
// create a second resource.
func TestRetryWithTheSameKeyReplaysInsteadOfReexecuting(t *testing.T) {
	var calls atomic.Int32
	app, _ := idempotentApp(t, "ws_a", func(c *fiber.Ctx) error {
		n := calls.Add(1)
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"id": "thing_" + strconv.Itoa(int(n))})
	})

	first := postWithKey(t, app, "key-1", `{"name":"a"}`)
	defer first.Body.Close()
	second := postWithKey(t, app, "key-1", `{"name":"a"}`)
	defer second.Body.Close()

	if calls.Load() != 1 {
		t.Fatalf("handler ran %d times; a retried mutation must execute once", calls.Load())
	}
	if second.StatusCode != first.StatusCode {
		t.Fatalf("replay status %d != original %d", second.StatusCode, first.StatusCode)
	}
	if second.Header.Get("Idempotency-Replayed") != "true" {
		t.Error("replayed response is not marked as a replay")
	}
	var firstBody, secondBody map[string]any
	_ = json.NewDecoder(first.Body).Decode(&firstBody)
	_ = json.NewDecoder(second.Body).Decode(&secondBody)
	if firstBody["id"] != secondBody["id"] {
		t.Fatalf("replay returned a different resource: %v vs %v", firstBody, secondBody)
	}
}

// Reusing a key for a different payload is a client bug in either direction:
// replaying hides the second request, executing breaks the key's promise.
func TestReusingAKeyWithADifferentBodyIsRefused(t *testing.T) {
	app, _ := idempotentApp(t, "ws_a", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"ok": true})
	})
	first := postWithKey(t, app, "key-1", `{"name":"a"}`)
	defer first.Body.Close()
	second := postWithKey(t, app, "key-1", `{"name":"DIFFERENT"}`)
	defer second.Body.Close()

	if second.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d, want 409", second.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(second.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != apiversion.CodeIdempotencyReus {
		t.Fatalf("untyped conflict: %v", body)
	}
	if strings.TrimSpace(toString(body["remedy"])) == "" {
		t.Error("conflict carries no remedy")
	}
}

// Keys are namespaced by workspace, so one tenant cannot replay — or block —
// another tenant's mutation by guessing a key.
func TestIdempotencyKeysAreNamespacedByWorkspace(t *testing.T) {
	store := newIdempotencyStore()
	a := store.key("ws_a", http.MethodPost, "/things", "shared-key")
	b := store.key("ws_b", http.MethodPost, "/things", "shared-key")
	if a == b {
		t.Fatal("the same client key in two workspaces collides")
	}
	route := store.key("ws_a", http.MethodPost, "/other", "shared-key")
	if a == route {
		t.Fatal("the same client key on two routes collides")
	}
	method := store.key("ws_a", http.MethodDelete, "/things", "shared-key")
	if a == method {
		t.Fatal("the same client key on two methods collides")
	}
}

// A failure must not be cached: doing so would turn one transient 500 into a
// permanent one for the whole TTL.
func TestFailedMutationsAreNotReplayed(t *testing.T) {
	var calls atomic.Int32
	app, _ := idempotentApp(t, "ws_a", func(c *fiber.Ctx) error {
		if calls.Add(1) == 1 {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "transient"})
		}
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"ok": true})
	})
	first := postWithKey(t, app, "key-1", `{"n":1}`)
	defer first.Body.Close()
	if first.StatusCode != http.StatusInternalServerError {
		t.Fatalf("setup: status=%d", first.StatusCode)
	}
	second := postWithKey(t, app, "key-1", `{"n":1}`)
	defer second.Body.Close()
	if second.StatusCode != http.StatusCreated {
		t.Fatalf("a cached failure blocked the retry: status=%d", second.StatusCode)
	}
	if calls.Load() != 2 {
		t.Fatalf("handler ran %d times; the retry after a failure must execute", calls.Load())
	}
}

// No key means no behaviour change, which is what keeps this additive.
func TestRequestsWithoutAKeyAreUntouched(t *testing.T) {
	var calls atomic.Int32
	app, _ := idempotentApp(t, "ws_a", func(c *fiber.Ctx) error {
		calls.Add(1)
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"ok": true})
	})
	for i := 0; i < 3; i++ {
		resp := postWithKey(t, app, "", `{"n":1}`)
		resp.Body.Close()
	}
	if calls.Load() != 3 {
		t.Fatalf("handler ran %d times; unkeyed requests must not be deduplicated", calls.Load())
	}
}

func TestReadsAreNeverIntercepted(t *testing.T) {
	var calls atomic.Int32
	app, _ := idempotentApp(t, "ws_a", func(c *fiber.Ctx) error {
		calls.Add(1)
		return c.JSON(fiber.Map{"ok": true})
	})
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/things", nil)
		req.Header.Set("Idempotency-Key", "key-1")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if calls.Load() != 2 {
		t.Fatalf("a GET was deduplicated (%d calls)", calls.Load())
	}
}

func TestExpiredRecordsAreReexecuted(t *testing.T) {
	store := newIdempotencyStore()
	now := time.Now()
	store.now = func() time.Time { return now }
	key := store.key("ws_a", http.MethodPost, "/things", "key-1")

	if _, found, err := store.begin(key, "ws_test", "hash", "req-1"); err != nil || found {
		t.Fatalf("first begin: found=%v err=%v", found, err)
	}
	store.complete(key, http.StatusCreated, fiber.MIMEApplicationJSON, []byte(`{"ok":true}`))
	if _, found, err := store.begin(key, "ws_test", "hash", "req-2"); err != nil || !found {
		t.Fatalf("a completed record should replay: found=%v err=%v", found, err)
	}
	now = now.Add(idempotencyTTL + time.Minute)
	if _, found, err := store.begin(key, "ws_test", "hash", "req-3"); err != nil || found {
		t.Fatalf("an expired record should not replay: found=%v err=%v", found, err)
	}
}

func TestConcurrentDuplicateIsRefusedWhileInFlight(t *testing.T) {
	store := newIdempotencyStore()
	key := store.key("ws_a", http.MethodPost, "/things", "key-1")
	if _, _, err := store.begin(key, "ws_test", "hash", "req-1"); err != nil {
		t.Fatal(err)
	}
	_, _, err := store.begin(key, "ws_test", "hash", "req-2")
	var typed *apiversion.IncompatibleError
	if !asIncompatible(err, &typed) || typed.Code != apiversion.CodeIdempotencyBusy {
		t.Fatalf("expected idempotency_key_in_flight, got %v", err)
	}
}

// A handler that never produced a response must release its reservation,
// otherwise one crashed request poisons that key for a day.
func TestAbandonedReservationsAreReleased(t *testing.T) {
	store := newIdempotencyStore()
	key := store.key("ws_a", http.MethodPost, "/things", "key-1")
	if _, _, err := store.begin(key, "ws_test", "hash", "req-1"); err != nil {
		t.Fatal(err)
	}
	store.abandon(key)
	if _, found, err := store.begin(key, "ws_test", "hash", "req-2"); err != nil || found {
		t.Fatalf("key stayed locked after abandonment: found=%v err=%v", found, err)
	}
}

// ---------------------------------------------------------------------------
// Optimistic concurrency
// ---------------------------------------------------------------------------

func etagApp(t *testing.T, current func() any) *fiber.App {
	t.Helper()
	s := &Server{log: zap.NewNop()}
	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Get("/thing", func(c *fiber.Ctx) error {
		value := current()
		c.Set(fiber.HeaderETag, resourceETag(value))
		return c.JSON(value)
	})
	app.Put("/thing", func(c *fiber.Ctx) error {
		if rejected, err := s.checkIfMatch(c, current()); rejected {
			return err
		}
		return c.JSON(fiber.Map{"applied": true})
	})
	return app
}

func TestStaleWriteIsRejectedWithATypedConflict(t *testing.T) {
	state := map[string]any{"name": "original"}
	app := etagApp(t, func() any { return state })

	read, err := app.Test(httptest.NewRequest(http.MethodGet, "/thing", nil))
	if err != nil {
		t.Fatal(err)
	}
	read.Body.Close()
	etag := read.Header.Get(fiber.HeaderETag)
	if etag == "" {
		t.Fatal("read did not return an ETag; conditional writes are impossible without one")
	}

	// Someone else edits the resource between our read and our write.
	state = map[string]any{"name": "changed by someone else"}

	req := httptest.NewRequest(http.MethodPut, "/thing", strings.NewReader(`{"name":"mine"}`))
	req.Header.Set(fiber.HeaderIfMatch, etag)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d, want 409 — a stale write was applied", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != apiversion.CodeStaleWrite {
		t.Fatalf("untyped conflict: %v", body)
	}
	if toString(body["etag"]) == "" || strings.TrimSpace(toString(body["remedy"])) == "" {
		t.Fatalf("conflict must carry the current ETag and a remedy: %v", body)
	}
}

func TestFreshWriteAndWildcardAndWeakValidatorsAreAccepted(t *testing.T) {
	state := map[string]any{"name": "original"}
	app := etagApp(t, func() any { return state })
	etag := resourceETag(state)

	for name, header := range map[string]string{
		"exact":    etag,
		"wildcard": "*",
		"weak":     "W/" + etag,
		"list":     `"other", ` + etag,
	} {
		req := httptest.NewRequest(http.MethodPut, "/thing", strings.NewReader(`{}`))
		req.Header.Set(fiber.HeaderIfMatch, header)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s validator: status=%d, want 200", name, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// Omitting If-Match must keep working, or every existing client breaks the
// day this ships.
func TestWritesWithoutIfMatchStillApply(t *testing.T) {
	state := map[string]any{"name": "original"}
	app := etagApp(t, func() any { return state })
	resp, err := app.Test(httptest.NewRequest(http.MethodPut, "/thing", strings.NewReader(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
}

func TestETagChangesWithContentAndIsStableWithout(t *testing.T) {
	a := resourceETag(map[string]any{"name": "x", "n": 1})
	again := resourceETag(map[string]any{"name": "x", "n": 1})
	b := resourceETag(map[string]any{"name": "x", "n": 2})
	if a != again {
		t.Fatal("ETag is unstable for identical content")
	}
	if a == b {
		t.Fatal("ETag did not change when content changed")
	}
	if !strings.HasPrefix(a, `"`) || !strings.HasSuffix(a, `"`) {
		t.Fatalf("ETag %s is not a quoted validator", a)
	}
}

func toString(value any) string {
	s, _ := value.(string)
	return s
}
