// workspace_deletion_routes_test.go — the three gates, the recovery window,
// and the ordering that makes the window worth having.
//
// The gates themselves are unit-tested in workspace_deletion_test.go. These
// prove the ROUTE applies them, that the store rather than a stale read
// decides, and that a purge which does not cover everything cannot be
// described as a finished deletion.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/internal/workspacepurge"
)

// fakeLifecycle is an in-memory WorkspaceLifecycle with the same refusals the
// Postgres one makes, so the route's behaviour is exercised against the
// contract rather than against a permissive stub.
type fakeLifecycle struct {
	mu           sync.Mutex
	record       tenancy.WorkspaceRecord
	now          func() time.Time
	claimedUntil time.Time
	claims       int
}

func newFakeLifecycle(name string) *fakeLifecycle {
	return &fakeLifecycle{
		record: tenancy.WorkspaceRecord{
			ID: "ws_team", OrganizationID: "org_team", Name: name, Status: tenancy.WorkspaceActive,
		},
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (f *fakeLifecycle) Workspace(context.Context, string) (tenancy.WorkspaceRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.record, nil
}

func (f *fakeLifecycle) BeginDeletion(_ context.Context, _ tenancy.Mutation, _ string, recoverUntil time.Time) (tenancy.WorkspaceRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.record.Status != tenancy.WorkspaceActive {
		return tenancy.WorkspaceRecord{}, tenancy.ErrWorkspaceNotActive
	}
	f.record.Status = tenancy.WorkspaceDeleting
	f.record.RequestedAt = f.now()
	f.record.RecoverUntil = recoverUntil
	return f.record, nil
}

func (f *fakeLifecycle) CancelDeletion(context.Context, tenancy.Mutation, string) (tenancy.WorkspaceRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.record.Status != tenancy.WorkspaceDeleting {
		return tenancy.WorkspaceRecord{}, tenancy.ErrWorkspaceNotDeleting
	}
	if !f.record.Recoverable(f.now()) {
		return tenancy.WorkspaceRecord{}, tenancy.ErrRecoveryWindowClosed
	}
	f.record.Status = tenancy.WorkspaceActive
	f.record.RequestedAt = time.Time{}
	f.record.RecoverUntil = time.Time{}
	return f.record, nil
}

func (f *fakeLifecycle) CompleteDeletion(context.Context, tenancy.Mutation, string) (tenancy.WorkspaceRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.record.Status != tenancy.WorkspaceDeleting {
		return tenancy.WorkspaceRecord{}, tenancy.ErrWorkspaceNotDeleting
	}
	f.record.Status = tenancy.WorkspaceDeleted
	return f.record, nil
}

// due and claims model the two-instance case: DueForPurge lists what is ready,
// ClaimPurge is exclusive and expiring.
func (f *fakeLifecycle) DueForPurge(_ context.Context, now time.Time, limit int) ([]tenancy.WorkspaceRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.record.PurgeDue(now) {
		return nil, nil
	}
	if f.claimedUntil.After(now) {
		return nil, nil
	}
	return []tenancy.WorkspaceRecord{f.record}, nil
}

func (f *fakeLifecycle) ClaimPurge(_ context.Context, _ tenancy.Mutation, _ string, leaseUntil time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	if !f.record.PurgeDue(now) || f.claimedUntil.After(now) {
		return false, nil
	}
	f.claimedUntil = leaseUntil
	f.claims++
	return true, nil
}

func (f *fakeLifecycle) status() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.record.Status
}

func deletionApp(t *testing.T, role *string, authTime *time.Time, lifecycle tenancy.WorkspaceLifecycle) (*Server, *fiber.App) {
	t.Helper()
	srv := newTestGateway(t, "secret")
	srv.config().Deployment.Mode = config.DeploymentModeTeam
	srv.SetWorkspaceLifecycle(lifecycle)

	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		srv.SetTenantResolver(&recordingMembershipResolver{membership: tenancy.Membership{
			OrganizationID: "org_team", WorkspaceID: "ws_team", MembershipID: "mem_team", Role: *role,
		}})
		auth.SetClaims(c, &auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_alice"},
			Role:             *role, Kind: "access", PrincipalKind: "user",
			CredentialID: "cred_alice", AuthTime: authTime.Unix(),
		})
		c.Locals("request_id", "req-deletion")
		return c.Next()
	})
	app.Use(srv.workspaceContextMW())
	srv.registerWorkspaceDeletionRoutes(app)
	return srv, app
}

func deletionRequest(t *testing.T, app *fiber.App, method, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, "/workspace/deletion", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	decoded := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}

// All three gates, at the route. Each defends against something the others
// cannot: authentication cannot tell you meant THIS workspace, the name cannot
// tell you meant it at all, and the window cannot tell it is still you.
func TestDeletionRequiresRecentAuthTheExactNameAndAWindow(t *testing.T) {
	role := tenancy.RoleOwner
	fresh := time.Now()
	lifecycle := newFakeLifecycle("Acme Production")
	_, app := deletionApp(t, &role, &fresh, lifecycle)

	// Not an owner.
	role = tenancy.RoleAdmin
	if status, _ := deletionRequest(t, app, http.MethodPost,
		`{"confirm_workspace_name":"Acme Production"}`); status != http.StatusForbidden {
		t.Errorf("an admin was allowed to delete the workspace: %d", status)
	}
	role = tenancy.RoleOwner

	// Stale session.
	stale := time.Now().Add(-2 * time.Hour)
	freshAgain := fresh
	fresh = stale
	if status, _ := deletionRequest(t, app, http.MethodPost,
		`{"confirm_workspace_name":"Acme Production"}`); status != http.StatusUnauthorized {
		t.Errorf("a stale session was allowed to delete the workspace: %d", status)
	}
	fresh = freshAgain

	// Wrong name — and the refusal must NOT echo the right one, or the gate is
	// a formality the next request satisfies by copying.
	status, body := deletionRequest(t, app, http.MethodPost, `{"confirm_workspace_name":"acme production"}`)
	if status != http.StatusBadRequest {
		t.Errorf("a near-miss name was accepted: %d", status)
	}
	if rendered, _ := json.Marshal(body); strings.Contains(string(rendered), "Acme Production") {
		t.Errorf("the refusal echoed the expected workspace name: %s", rendered)
	}

	// Too short a window.
	if status, _ := deletionRequest(t, app, http.MethodPost,
		`{"confirm_workspace_name":"Acme Production","recovery_window":"1h"}`); status != http.StatusBadRequest {
		t.Errorf("a sub-minimum recovery window was accepted: %d", status)
	}

	if lifecycle.status() != tenancy.WorkspaceActive {
		t.Fatalf("a refused deletion changed the workspace status to %q", lifecycle.status())
	}

	// And the request that satisfies every gate.
	status, body = deletionRequest(t, app, http.MethodPost,
		`{"confirm_workspace_name":"Acme Production","reason":"customer offboarding"}`)
	if status != http.StatusAccepted {
		t.Fatalf("a valid deletion request = %d: %v", status, body)
	}
	if lifecycle.status() != tenancy.WorkspaceDeleting {
		t.Fatalf("workspace status after a valid request = %q", lifecycle.status())
	}
}

// Requesting a deletion must NOT delete anything. The workspace stops
// accepting writes and a deadline is recorded; that is the whole point of a
// recovery window, and a route that purged here would have none.
func TestRequestingADeletionStopsWritesWithoutDestroyingAnything(t *testing.T) {
	role := tenancy.RoleOwner
	fresh := time.Now()
	lifecycle := newFakeLifecycle("Acme")
	_, app := deletionApp(t, &role, &fresh, lifecycle)

	status, body := deletionRequest(t, app, http.MethodPost, `{"confirm_workspace_name":"Acme"}`)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d: %v", status, body)
	}
	deletion, _ := body["deletion"].(map[string]any)
	if got, _ := deletion["status"].(string); got != tenancy.WorkspaceDeleting {
		t.Fatalf("the report says status %q, not deleting", got)
	}
	// The report enumerates the resource classes, projected from the same
	// catalog the export uses — so the two cannot disagree about what a
	// workspace contains.
	resources, _ := deletion["resources"].([]any)
	if len(resources) == 0 {
		t.Fatal("the deletion report names no resource classes")
	}
	if _, ok := deletion["recoverable_until"]; !ok {
		t.Fatal("the report carries no recovery deadline")
	}

	// A second request is refused by the STORE, not by the stale read above:
	// a gate checked against a value fetched a moment ago is one two
	// concurrent owners both pass.
	if status, _ := deletionRequest(t, app, http.MethodPost, `{"confirm_workspace_name":"Acme"}`); status != http.StatusConflict {
		t.Fatalf("a second deletion request = %d, want 409", status)
	}
}

// Cancelling inside the window works; outside it, the answer is 410 and not
// 409. "There was nothing to cancel" and "there was, and you have missed it"
// mean opposite things to an owner racing the deadline — one should stop
// looking, the other should call support now.
func TestCancellingInsideTheWindowWorksAndOutsideItIsADifferentAnswer(t *testing.T) {
	role := tenancy.RoleOwner
	fresh := time.Now()
	lifecycle := newFakeLifecycle("Acme")
	_, app := deletionApp(t, &role, &fresh, lifecycle)

	// Nothing to cancel yet.
	if status, _ := deletionRequest(t, app, http.MethodDelete, ""); status != http.StatusConflict {
		t.Fatalf("cancelling with no deletion in progress = %d, want 409", status)
	}

	if status, _ := deletionRequest(t, app, http.MethodPost, `{"confirm_workspace_name":"Acme"}`); status != http.StatusAccepted {
		t.Fatal("the deletion request failed")
	}
	if status, _ := deletionRequest(t, app, http.MethodDelete, ""); status != http.StatusOK {
		t.Fatalf("cancelling inside the window = %d, want 200", status)
	}
	if lifecycle.status() != tenancy.WorkspaceActive {
		t.Fatalf("status after cancellation = %q", lifecycle.status())
	}

	// Now let the window close.
	if status, _ := deletionRequest(t, app, http.MethodPost, `{"confirm_workspace_name":"Acme"}`); status != http.StatusAccepted {
		t.Fatal("the second deletion request failed")
	}
	lifecycle.mu.Lock()
	lifecycle.now = func() time.Time { return lifecycle.record.RecoverUntil.Add(time.Minute) }
	lifecycle.mu.Unlock()

	if status, _ := deletionRequest(t, app, http.MethodDelete, ""); status != http.StatusGone {
		t.Fatalf("cancelling past the window = %d, want 410", status)
	}
	if lifecycle.status() != tenancy.WorkspaceDeleting {
		t.Fatalf("a refused cancellation changed the status to %q", lifecycle.status())
	}
}

// The purge does not run until the window closes, and an INCOMPLETE purge is
// reported as incomplete rather than as a finished deletion.
func TestThePurgeWaitsForTheWindowAndAdmitsWhatItDidNotCover(t *testing.T) {
	role := tenancy.RoleOwner
	fresh := time.Now()
	lifecycle := newFakeLifecycle("Acme")
	srv, app := deletionApp(t, &role, &fresh, lifecycle)

	if status, _ := deletionRequest(t, app, http.MethodPost, `{"confirm_workspace_name":"Acme"}`); status != http.StatusAccepted {
		t.Fatal("the deletion request failed")
	}

	// Inside the window: nothing runs, and the workspace is untouched.
	report, err := srv.PurgeWorkspaceIfDue(context.Background(), "ws_team", time.Now())
	if err != nil {
		t.Fatalf("an early purge returned an error: %v", err)
	}
	if len(report.Entries) != 0 {
		t.Fatal("the purge ran before the recovery window closed")
	}
	if lifecycle.status() != tenancy.WorkspaceDeleting {
		t.Fatalf("an early purge changed the status to %q", lifecycle.status())
	}

	// Past the window: it runs, and says what it did not cover.
	lifecycle.mu.Lock()
	due := lifecycle.record.RecoverUntil.Add(time.Minute)
	lifecycle.mu.Unlock()

	report, err = srv.PurgeWorkspaceIfDue(context.Background(), "ws_team", due)
	if !errors.Is(err, workspacepurge.ErrIncomplete) {
		t.Fatalf("an incomplete purge returned %v, want ErrIncomplete", err)
	}
	if report.Complete {
		t.Fatal("a purge with uncovered classes reported complete")
	}
	if len(report.Survivors) == 0 {
		t.Fatal("an incomplete purge named no survivors")
	}
	// Marked deleted anyway, WITH the survivor list. Leaving it `deleting`
	// until coverage is total would keep a workspace the customer was told is
	// gone in a state no operator can resolve.
	if lifecycle.status() != tenancy.WorkspaceDeleted {
		t.Fatalf("status after the purge = %q, want deleted", lifecycle.status())
	}
}

// With no lifecycle store, the routes answer 503 rather than pretending. A
// deletion that reports success with nothing to record it is the worst
// possible failure of this particular endpoint.
func TestDeletionRoutesRefuseWithNoLifecycleStore(t *testing.T) {
	role := tenancy.RoleOwner
	fresh := time.Now()
	_, app := deletionApp(t, &role, &fresh, nil)

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		if status, _ := deletionRequest(t, app, method, `{"confirm_workspace_name":"Acme"}`); status != http.StatusServiceUnavailable {
			t.Errorf("%s with no lifecycle store = %d, want 503", method, status)
		}
	}
}

// A personal installation's only workspace is the installation. Refusing beats
// a no-op success, which would report a deletion that did not happen.
func TestAPersonalDeploymentRefusesToDeleteItsOnlyWorkspace(t *testing.T) {
	role := tenancy.RoleOwner
	fresh := time.Now()
	personal := tenancy.NewPersonalWorkspaceLifecycle(tenancy.PersonalTenant{
		OrganizationID: "org_personal", WorkspaceID: "ws_personal",
	})
	_, app := deletionApp(t, &role, &fresh, personal)

	status, body := deletionRequest(t, app, http.MethodPost, `{"confirm_workspace_name":"Personal"}`)
	if status != http.StatusNotImplemented {
		t.Fatalf("a personal deletion = %d, want 501: %v", status, body)
	}
	if message, _ := body["error"].(string); !strings.Contains(message, "data directory") {
		t.Fatalf("the refusal does not say what to do instead: %v", body)
	}
}

// ---------------------------------------------------------------------------
// The sweep
// ---------------------------------------------------------------------------

// Without a sweep the product has a deletion API that records an intention and
// never acts on it: the workspace sits at `deleting`, refusing writes, data
// intact, forever — with the customer told it would be gone on a date.
func TestTheSweepPurgesAWorkspaceOnceItsWindowHasClosed(t *testing.T) {
	role := tenancy.RoleOwner
	fresh := time.Now()
	lifecycle := newFakeLifecycle("Acme")
	srv, app := deletionApp(t, &role, &fresh, lifecycle)

	if status, _ := deletionRequest(t, app, http.MethodPost, `{"confirm_workspace_name":"Acme"}`); status != http.StatusAccepted {
		t.Fatal("the deletion request failed")
	}

	// Inside the window the sweep must do nothing at all.
	if n := srv.sweepDueWorkspacePurges(context.Background(), time.Now().UTC()); n != 0 {
		t.Fatalf("the sweep purged %d workspaces before the window closed", n)
	}
	if lifecycle.status() != tenancy.WorkspaceDeleting {
		t.Fatalf("an early sweep changed the status to %q", lifecycle.status())
	}

	lifecycle.mu.Lock()
	due := lifecycle.record.RecoverUntil.Add(time.Minute)
	lifecycle.now = func() time.Time { return due }
	lifecycle.mu.Unlock()

	if n := srv.sweepDueWorkspacePurges(context.Background(), due); n != 1 {
		t.Fatalf("the sweep purged %d workspaces past the window, want 1", n)
	}
	if lifecycle.status() != tenancy.WorkspaceDeleted {
		t.Fatalf("status after the sweep = %q, want deleted", lifecycle.status())
	}
}

// The claim is exclusive, so two gateway instances sweeping at once do not
// both purge the same workspace.
func TestTwoSweepsDoNotBothPurgeTheSameWorkspace(t *testing.T) {
	role := tenancy.RoleOwner
	fresh := time.Now()
	lifecycle := newFakeLifecycle("Acme")
	srv, app := deletionApp(t, &role, &fresh, lifecycle)
	if status, _ := deletionRequest(t, app, http.MethodPost, `{"confirm_workspace_name":"Acme"}`); status != http.StatusAccepted {
		t.Fatal("the deletion request failed")
	}
	lifecycle.mu.Lock()
	due := lifecycle.record.RecoverUntil.Add(time.Minute)
	lifecycle.now = func() time.Time { return due }
	lifecycle.mu.Unlock()

	first := srv.sweepDueWorkspacePurges(context.Background(), due)
	second := srv.sweepDueWorkspacePurges(context.Background(), due)
	if first+second != 1 {
		t.Fatalf("two sweeps purged %d workspaces between them, want 1", first+second)
	}
	lifecycle.mu.Lock()
	claims := lifecycle.claims
	lifecycle.mu.Unlock()
	if claims != 1 {
		t.Fatalf("the workspace was claimed %d times, want 1", claims)
	}
}

// A gateway with no lifecycle store starts no sweep goroutine and does nothing
// when driven — which is every directly constructed and embedded gateway,
// including every test in this package.
func TestAGatewayWithNoLifecycleStoreSweepsNothing(t *testing.T) {
	srv := newTestGateway(t, "secret")
	if n := srv.sweepDueWorkspacePurges(context.Background(), time.Now()); n != 0 {
		t.Fatalf("a gateway with no lifecycle store purged %d workspaces", n)
	}
	// And starting the sweep is a no-op rather than a goroutine that panics on
	// its first tick.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.StartWorkspacePurgeSweep(ctx)
}
