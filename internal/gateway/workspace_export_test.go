// workspace_export_test.go — MU-032 criterion 2 at the HTTP boundary.
//
// The package tests prove the archive is honest about what it contains. These
// prove the four things only the route can get wrong: who may ask, whose
// exports they see, that a client hanging up does not abandon the work, and
// that "not ready", "expired" and "no such export" are three different answers.
package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/internal/workspaceexport"
)

// exportApp mounts the export routes behind the real workspace middleware.
// role is a pointer so a test can change who is asking between requests, which
// is what makes "an admin is not an owner" testable at all.
func exportApp(t *testing.T, role *string, workspaceID string) (*Server, *fiber.App) {
	t.Helper()
	t.Setenv("SOULACY_WORKSPACE", t.TempDir())
	srv := newTestGateway(t, "secret")
	srv.config().Deployment.Mode = config.DeploymentModeTeam
	srv.SetTenantResolver(&recordingMembershipResolver{membership: tenancy.Membership{
		OrganizationID: "org_team", WorkspaceID: workspaceID, MembershipID: "mem_team", Role: *role,
	}})

	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		srv.SetTenantResolver(&recordingMembershipResolver{membership: tenancy.Membership{
			OrganizationID: "org_team", WorkspaceID: workspaceID, MembershipID: "mem_team", Role: *role,
		}})
		auth.SetClaims(c, &auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_alice"},
			Role:             *role, Kind: "access", PrincipalKind: "user",
			CredentialID: "cred_alice",
			// Fresh, so requireRecentAuth is satisfied and the test is about
			// the export gate rather than about step-up (which recentauth_test
			// covers on its own).
			AuthTime: time.Now().Unix(),
		})
		c.Locals("request_id", "req-export")
		return c.Next()
	})
	app.Use(srv.workspaceContextMW())
	srv.registerWorkspaceExportRoutes(app)
	return srv, app
}

func exportRequest(t *testing.T, app *fiber.App, method, path string) (int, map[string]any) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(method, path, strings.NewReader("{}")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

// waitForExport polls until the detached run finishes. The route answers 202
// by design, so a test that read the store immediately would be asserting on a
// race rather than on the export.
func waitForExport(t *testing.T, srv *Server, workspaceID, id string) *workspaceexport.Job {
	t.Helper()
	store, err := srv.exportStore()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := store.Get(workspaceID, id, time.Now())
		if err == nil && job.Status != workspaceexport.JobPending && job.Status != workspaceexport.JobRunning {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the export never reached a terminal status")
	return nil
}

func requestExport(t *testing.T, srv *Server, app *fiber.App, workspaceID string) *workspaceexport.Job {
	t.Helper()
	status, body := exportRequest(t, app, http.MethodPost, "/workspace/export")
	if status != http.StatusAccepted {
		t.Fatalf("export request = %d: %v", status, body)
	}
	export, _ := body["export"].(map[string]any)
	id, _ := export["id"].(string)
	if id == "" {
		t.Fatalf("no export id returned: %v", body)
	}
	return waitForExport(t, srv, workspaceID, id)
}

// An export is not the sum of permissions the caller already holds — it is one
// file containing every resource class, produced without the per-resource
// checks that normally stand between a role and a secret name or another
// member's conversations. So it is gated like the other workspace-lifecycle
// action, on the owner.
func TestOnlyAWorkspaceOwnerCanExportTheWorkspace(t *testing.T) {
	role := tenancy.RoleAdmin
	srv, app := exportApp(t, &role, "ws_team")

	for _, denied := range []string{tenancy.RoleAdmin, "member", "viewer"} {
		role = denied
		if status, _ := exportRequest(t, app, http.MethodPost, "/workspace/export"); status != http.StatusForbidden {
			t.Errorf("role %q was allowed to request an export: status=%d", denied, status)
		}
		if status, _ := exportRequest(t, app, http.MethodGet, "/workspace/export"); status != http.StatusForbidden {
			t.Errorf("role %q was allowed to list exports: status=%d", denied, status)
		}
	}
	// Waited on rather than fired and forgotten. The export runs on a context
	// detached from the request — which is the point of the 202 — so a test
	// that returned here would leave a goroutine writing into a directory
	// t.TempDir() is about to remove, and fail in cleanup with a message about
	// the filesystem rather than about authorization.
	role = tenancy.RoleOwner
	if job := requestExport(t, srv, app, "ws_team"); job.Status != workspaceexport.JobReady {
		t.Errorf("the owner's export did not complete: %q (%s)", job.Status, job.Error)
	}
}

// The route answers 202 and the work continues on a detached context, so the
// archive has to survive the request that asked for it. Asserted by letting
// the request complete and then observing the finished archive — the same
// thing a client that hung up would have caused.
func TestTheArchiveOutlivesTheRequestThatAskedForIt(t *testing.T) {
	role := tenancy.RoleOwner
	srv, app := exportApp(t, &role, "ws_team")
	job := requestExport(t, srv, app, "ws_team")

	if job.Status != workspaceexport.JobReady {
		t.Fatalf("export status = %q (%s)", job.Status, job.Error)
	}
	if job.Bytes <= 0 {
		t.Fatal("the finished export has no archive")
	}
	if job.Manifest == nil || len(job.Manifest.Entries) == 0 {
		t.Fatal("the finished export carries no manifest")
	}
}

// Three distinct answers, because they have three distinct remedies: wait,
// request a new one, and check the id.
func TestNotReadyExpiredAndUnknownAreThreeDifferentAnswers(t *testing.T) {
	role := tenancy.RoleOwner
	srv, app := exportApp(t, &role, "ws_team")

	if status, _ := exportRequest(t, app, http.MethodGet,
		"/workspace/export/aaaaaaaaaaaaaaaa/download"); status != http.StatusNotFound {
		t.Errorf("an unknown export id = %d, want 404", status)
	}

	store, err := srv.exportStore()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := workspaceexport.NewJob("pendingpending01", "ws_team", "Acme", "usr_alice", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(pending); err != nil {
		t.Fatal(err)
	}
	if status, _ := exportRequest(t, app, http.MethodGet,
		"/workspace/export/pendingpending01/download"); status != http.StatusConflict {
		t.Errorf("an unfinished export = %d, want 409", status)
	}

	done := requestExport(t, srv, app, "ws_team")
	done.ExpiresAt = time.Now().Add(-time.Hour)
	if err := store.Save(done); err != nil {
		t.Fatal(err)
	}
	if status, _ := exportRequest(t, app, http.MethodGet,
		"/workspace/export/"+done.ID+"/download"); status != http.StatusGone {
		t.Errorf("an expired export = %d, want 410", status)
	}
}

// The download streams the file, and Fiber writes the body AFTER the handler
// returns. A deferred Close in the handler makes every download fail partway
// through with "file already closed" — during the stream, after the 200 has
// been written, so the access log records a success. This asserts the bytes
// actually arrive.
func TestTheDownloadDeliversTheWholeArchive(t *testing.T) {
	role := tenancy.RoleOwner
	srv, app := exportApp(t, &role, "ws_team")
	job := requestExport(t, srv, app, "ws_team")

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/workspace/export/"+job.ID+"/download", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("the archive stream failed partway: %v", err)
	}
	if int64(len(body)) != job.Bytes {
		t.Fatalf("downloaded %d bytes, the archive is %d", len(body), job.Bytes)
	}
	if got := resp.Header.Get("X-Soulacy-Export-Checksum"); got != job.Manifest.Checksum {
		t.Fatalf("checksum header = %q, want %q", got, job.Manifest.Checksum)
	}
}

// The sources close over values read while the request was alive, never over
// the *fiber.Ctx — Fiber recycles that into a pool the moment the handler
// returns, so a closure holding one would read another tenant's request.
func TestTheExportSourcesDoNotHoldTheRequestContext(t *testing.T) {
	srv := newTestGateway(t, "secret")
	sources := srv.exportSources("ws_team", nil)
	if len(sources) == 0 {
		t.Fatal("no sources registered; this test would prove nothing")
	}
	if err := workspaceexport.ValidateSources(sources); err != nil {
		t.Fatalf("the registered sources disagree with the ownership catalog: %v", err)
	}
	// Runnable with no request in sight, which is the property: every value
	// they need was captured while one was alive.
	for _, source := range sources {
		if _, err := source.Emit(context.Background(), io.Discard); err != nil {
			t.Errorf("source %q cannot run without a request: %v", source.Resource, err)
		}
	}
}

// Exports are namespaced by PATH, so a second workspace does not see the
// first's archives — and could not have, even if the handler forgot a filter.
func TestOneWorkspaceCannotListOrDownloadAnothersExport(t *testing.T) {
	role := tenancy.RoleOwner
	srv, app := exportApp(t, &role, "ws_team")
	job := requestExport(t, srv, app, "ws_team")

	// Same server, same store, a different verified workspace.
	srv.SetTenantResolver(&recordingMembershipResolver{membership: tenancy.Membership{
		OrganizationID: "org_team", WorkspaceID: "ws_other", MembershipID: "mem_other", Role: tenancy.RoleOwner,
	}})
	other := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	other.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_eve"},
			Role:             tenancy.RoleOwner, Kind: "access", PrincipalKind: "user",
			CredentialID: "cred_eve", AuthTime: time.Now().Unix(),
		})
		c.Locals("request_id", "req-eve")
		return c.Next()
	})
	other.Use(srv.workspaceContextMW())
	srv.registerWorkspaceExportRoutes(other)

	status, body := exportRequest(t, other, http.MethodGet, "/workspace/export")
	if status != http.StatusOK {
		t.Fatalf("list status = %d", status)
	}
	if exports, _ := body["exports"].([]any); len(exports) != 0 {
		t.Fatalf("another workspace lists %d of this one's exports", len(exports))
	}
	if status, _ := exportRequest(t, other, http.MethodGet, "/workspace/export/"+job.ID); status != http.StatusNotFound {
		t.Errorf("another workspace read this export's status: %d", status)
	}
	if status, _ := exportRequest(t, other, http.MethodGet,
		"/workspace/export/"+job.ID+"/download"); status != http.StatusNotFound {
		t.Errorf("another workspace downloaded this export: %d", status)
	}
}
