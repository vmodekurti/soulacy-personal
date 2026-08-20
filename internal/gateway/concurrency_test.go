// concurrency_test.go — MU-028 at the HTTP edge: a stale save is refused, a
// missing precondition is refused where it matters, and the 409 carries enough
// to reconcile without a second round trip.
package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/concurrency"
)

func conflictServer(t *testing.T, teamMode bool) (*Server, *fiber.App, func() any) {
	t.Helper()
	state := map[string]any{"content": "v1"}
	srv := &Server{}
	if teamMode {
		srv = teamServer(nil)
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Get("/thing/:id", func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderETag, resourceETag(state))
		return c.JSON(state)
	})
	app.Put("/thing/:id", func(c *fiber.Ctx) error {
		if rejected, err := srv.checkIfMatch(c, state); rejected {
			return err
		}
		state["content"] = "v2"
		return c.SendStatus(fiber.StatusOK)
	})
	return srv, app, func() any { return state }
}

func put(t *testing.T, app *fiber.App, ifMatch string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/thing/support-bot", strings.NewReader("{}"))
	if ifMatch != "" {
		req.Header.Set(fiber.HeaderIfMatch, ifMatch)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := make([]byte, 2048)
	n, _ := resp.Body.Read(body)
	return resp.StatusCode, string(body[:n])
}

// The failure being prevented: a save from a form rendered before somebody
// else's save silently discards their change.
func TestAStaleSaveIsRefusedWithTheCurrentVersion(t *testing.T) {
	_, app, state := conflictServer(t, true)
	current := resourceETag(state())

	status, body := put(t, app, `"a-version-from-before"`)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}
	// A bare 409 forces a refetch and races: between the conflict and the
	// refetch the resource can change again. Asserted on the current_version
	// FIELD, not on the body containing the token — the etag header field
	// carries it too, so a substring check passes even with current_version
	// emptied.
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("body is not JSON: %s", body)
	}
	if decoded["current_version"] != current {
		t.Fatalf("current_version = %v, want %q", decoded["current_version"], current)
	}
	if decoded["supplied_version"] == nil || decoded["supplied_version"] == "" {
		t.Fatalf("the 409 does not echo what was sent: %s", body)
	}
	// And the write did not land.
	if state().(map[string]any)["content"] != "v1" {
		t.Fatal("a stale save was applied")
	}

	// The caller who is up to date still writes.
	if status, _ := put(t, app, current); status != http.StatusOK {
		t.Fatalf("a current version was refused: %d", status)
	}
}

// Letting a missing precondition through makes concurrency control opt-in, and
// the client that forgets is exactly the one that overwrites silently.
func TestAMissingPreconditionIsRefusedInMultiUserMode(t *testing.T) {
	_, app, state := conflictServer(t, true)
	status, body := put(t, app, "")
	// 428, not 409: "you sent no version" and "your version is stale" have
	// different remedies, and a client told 409 will re-read and retry —
	// succeeding, and still not sending a precondition.
	if status != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, want 428: %s", status, body)
	}
	if state().(map[string]any)["content"] != "v1" {
		t.Fatal("an unconditional save was applied")
	}
}

// A personal deployment has nobody to conflict with and keeps working
// unchanged (product invariant 7).
func TestAPersonalDeploymentStillAcceptsUnconditionalSaves(t *testing.T) {
	_, app, _ := conflictServer(t, false)
	if status, body := put(t, app, ""); status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, body)
	}
}

// A comma-separated If-Match list is legal HTTP, and proxies add W/ prefixes
// without asking.
func TestAnIfMatchListAndWeakPrefixesAreHonoured(t *testing.T) {
	_, _, state := conflictServer(t, true)
	current := resourceETag(state())

	for _, header := range []string{
		`"stale-one", ` + current,
		"W/" + current,
		concurrency.Wildcard,
	} {
		_, fresh, _ := conflictServer(t, true)
		if status, body := put(t, fresh, header); status != http.StatusOK {
			t.Errorf("If-Match %q returned %d: %s", header, status, body)
		}
	}
}

// The raw YAML editor is a whole-file replace, so a stale save discards every
// change made since the editor was opened — not just the overlapping field. It
// was the one agent-mutating handler with no check at all.
func TestTheYAMLEditorRefusesAStaleSave(t *testing.T) {
	srv := newTestGateway(t, "secret")
	seedAgent(t, srv)

	read := httptest.NewRequest(http.MethodGet, "/api/v1/agents/contract-agent/yaml", nil)
	read.Header.Set("Authorization", "Bearer secret")
	resp, err := srv.app.Test(read)
	if err != nil {
		t.Fatal(err)
	}
	version := resp.Header.Get(fiber.HeaderETag)
	resp.Body.Close()
	if version == "" {
		t.Fatal("the YAML editor cannot obtain a version, so it can never send one")
	}

	write := httptest.NewRequest(http.MethodPut, "/api/v1/agents/contract-agent/yaml",
		strings.NewReader("id: contract-agent\nname: Contract Agent\n"))
	write.Header.Set("Authorization", "Bearer secret")
	write.Header.Set(fiber.HeaderIfMatch, `"a-version-from-before"`)
	stale, err := srv.app.Test(write)
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Body.Close()
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("a stale YAML save returned %d, want 409", stale.StatusCode)
	}
}
