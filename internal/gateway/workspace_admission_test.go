// workspace_admission_test.go — MU-032 criterion 4: "new runs and writes stop
// when deletion begins."
//
// The check lives in workspaceContextMW rather than in each handler, because
// that is the one place every workspace request passes through and the only
// place that already holds the verified membership. A per-handler check is a
// rule, and the handlers that forget a rule are exactly the ones that write
// into a workspace somebody is in the middle of deleting.
package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/tenancy"
)

func admissionApp(t *testing.T, workspaceStatus string) *fiber.App {
	t.Helper()
	srv := withCfg(&Server{
		log: zap.NewNop(),
	}, &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeTeam}})
	srv.SetTenantResolver(&recordingMembershipResolver{membership: tenancy.Membership{
		OrganizationID: "org_a", WorkspaceID: "ws_a", MembershipID: "mem_a",
		Role: "owner", WorkspaceStatus: workspaceStatus,
	}})
	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_alice"},
			Role:             "owner", Kind: "access", PrincipalKind: "user",
		})
		c.Locals("request_id", "req-admission")
		return c.Next()
	})
	app.Use(srv.workspaceContextMW())
	handler := func(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) }
	app.Get("/thing", handler)
	app.Post("/thing", handler)
	app.Put("/thing", handler)
	app.Patch("/thing", handler)
	app.Delete("/thing", handler)
	return app
}

func admissionStatus(t *testing.T, app *fiber.App, method string) int {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(method, "/thing", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestAnActiveWorkspaceAcceptsEverything(t *testing.T) {
	app := admissionApp(t, tenancy.WorkspaceActive)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if got := admissionStatus(t, app, method); got != http.StatusNoContent {
			t.Errorf("%s on an active workspace = %d", method, got)
		}
	}
}

// The column postdates existing rows. Treating an unset status as "being
// deleted" would take a deployment offline on upgrade — which is the failure
// mode a fail-closed default has here, and why this one field is not.
func TestAnEmptyStatusIsActive(t *testing.T) {
	app := admissionApp(t, "")
	if got := admissionStatus(t, app, http.MethodPost); got != http.StatusNoContent {
		t.Fatalf("an unset workspace status refused a write with %d", got)
	}
}

// THE CRITERION. Writes and new runs stop; reads do not, because the recovery
// window is worthless if nobody can look at what is about to be destroyed.
func TestDeletingStopsWritesAndLeavesReadsOpen(t *testing.T) {
	app := admissionApp(t, tenancy.WorkspaceDeleting)
	if got := admissionStatus(t, app, http.MethodGet); got != http.StatusNoContent {
		t.Fatalf("a read during deletion was refused with %d — the recovery window is unusable", got)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if got := admissionStatus(t, app, method); got != http.StatusConflict {
			t.Errorf("%s during deletion = %d, want 409", method, got)
		}
	}
}

// A suspension a member cannot see is indistinguishable from having been
// removed from the workspace, and those have different remedies.
func TestSuspensionStopsWritesAndLeavesReadsOpen(t *testing.T) {
	app := admissionApp(t, tenancy.WorkspaceSuspended)
	if got := admissionStatus(t, app, http.MethodGet); got != http.StatusNoContent {
		t.Fatalf("a read on a suspended workspace = %d", got)
	}
	if got := admissionStatus(t, app, http.MethodPost); got != http.StatusConflict {
		t.Fatalf("a write on a suspended workspace = %d, want 409", got)
	}
}

func TestADeletedWorkspaceIsGoneForReadsToo(t *testing.T) {
	app := admissionApp(t, tenancy.WorkspaceDeleted)
	if got := admissionStatus(t, app, http.MethodGet); got != http.StatusGone {
		t.Fatalf("a read on a deleted workspace = %d, want 410", got)
	}
	if got := admissionStatus(t, app, http.MethodPost); got != http.StatusGone {
		t.Fatalf("a write on a deleted workspace = %d, want 410", got)
	}
}

// A status this build does not recognise is overwhelmingly likely to be one a
// NEWER build introduced to stop writes. Guessing "active" would have an old
// replica writing into a workspace a new one is deleting — the exact split
// -brain a rolling upgrade produces.
func TestAnUnrecognisedStatusRefusesWrites(t *testing.T) {
	app := admissionApp(t, "quarantined-pending-legal-hold")
	if got := admissionStatus(t, app, http.MethodPost); got != http.StatusConflict {
		t.Fatalf("an unknown workspace status allowed a write: %d", got)
	}
	// But it stays readable: only an explicit `deleted` means gone, and an old
	// build should not black out a workspace it merely does not understand.
	if got := admissionStatus(t, app, http.MethodGet); got != http.StatusNoContent {
		t.Fatalf("an unknown workspace status blocked a read: %d", got)
	}
}

// The predicate, at the level the rest of the system will use it.
func TestTheLifecyclePredicates(t *testing.T) {
	for status, writable := range map[string]bool{
		"":                              true,
		tenancy.WorkspaceActive:         true,
		"ACTIVE":                        true,
		tenancy.WorkspaceSuspended:      false,
		tenancy.WorkspaceDeleting:       false,
		tenancy.WorkspaceDeleted:        false,
		"something-a-newer-build-added": false,
	} {
		if tenancy.WorkspaceAcceptsWrites(status) != writable {
			t.Errorf("WorkspaceAcceptsWrites(%q) = %v, want %v", status, !writable, writable)
		}
	}
	if tenancy.WorkspaceIsReadable(tenancy.WorkspaceDeleted) {
		t.Error("a deleted workspace is readable")
	}
	if !tenancy.WorkspaceIsReadable(tenancy.WorkspaceDeleting) {
		t.Error("a workspace being deleted is not readable, so its recovery window cannot be used")
	}
}
