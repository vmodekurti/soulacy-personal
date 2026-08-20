// membership_audit_page_test.go — the two things only the ROUTE can get wrong
// about a paginated audit trail: what it does with a cursor it did not issue,
// and whether the next cursor reaches the client at all.
//
// The cursor codec is unit-tested in internal/tenancy, and the SQL needs
// Postgres. What is testable here — and worth testing, because it is where an
// investigator is misled — is the failure answer.
package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/tenancy"
)

// pagingMembers implements only what the audit route uses; every other
// MemberManager method is present to satisfy the interface and panics if
// reached, so a route that starts calling one is a loud failure rather than a
// silent zero value.
type pagingMembers struct {
	page tenancy.AuditPage
	err  error
	// lastCursor records what the route passed through, so the test can prove
	// the query parameter reaches the store rather than being dropped.
	lastCursor string
	lastLimit  int
}

func (m *pagingMembers) ListMembershipAuditPage(_ context.Context, _ string, limit int, cursor string) (tenancy.AuditPage, error) {
	m.lastCursor, m.lastLimit = cursor, limit
	return m.page, m.err
}

func (m *pagingMembers) ListMembershipAudit(context.Context, string, int) ([]tenancy.MembershipAudit, error) {
	panic("the audit route must use the paginated read")
}
func (m *pagingMembers) CreateInvitation(context.Context, tenancy.Mutation, string, string, string, string, string, time.Time) (tenancy.Invitation, error) {
	panic("unused")
}
func (m *pagingMembers) ListInvitations(context.Context, string) ([]tenancy.Invitation, error) {
	panic("unused")
}
func (m *pagingMembers) AcceptInvitation(context.Context, tenancy.Mutation, string, string) (tenancy.StoredMembership, error) {
	panic("unused")
}
func (m *pagingMembers) ListMembers(context.Context, string) ([]tenancy.StoredMembership, error) {
	panic("unused")
}
func (m *pagingMembers) SetMembershipRole(context.Context, tenancy.Mutation, string, string, string, string) (tenancy.StoredMembership, error) {
	panic("unused")
}
func (m *pagingMembers) SetMembershipStatusInWorkspace(context.Context, tenancy.Mutation, string, string, string, string) (tenancy.StoredMembership, error) {
	panic("unused")
}
func (m *pagingMembers) CanRefreshUser(context.Context, string) bool { panic("unused") }
func (m *pagingMembers) PrimaryMembership(context.Context, string) (tenancy.StoredMembership, bool) {
	panic("unused")
}

func auditPageApp(t *testing.T, members *pagingMembers) *fiber.App {
	t.Helper()
	srv := newTestGateway(t, "secret")
	srv.config().Deployment.Mode = config.DeploymentModeTeam
	srv.SetTenantResolver(&recordingMembershipResolver{membership: tenancy.Membership{
		OrganizationID: "org_team", WorkspaceID: "ws_team", MembershipID: "mem_team", Role: tenancy.RoleOwner,
	}})
	srv.tenantMembers = members

	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_alice"},
			Role:             tenancy.RoleOwner, Kind: "access", PrincipalKind: "user",
			CredentialID: "cred_alice", AuthTime: time.Now().Unix(),
		})
		c.Locals("request_id", "req-audit")
		return c.Next()
	})
	app.Use(srv.workspaceContextMW())
	app.Get("/workspace/membership-audit", srv.handleWorkspaceMembershipAudit)
	return app
}

func auditPageRequest(t *testing.T, app *fiber.App, query string) (int, map[string]any) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/workspace/membership-audit"+query, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

// THE FAILURE THAT MISLEADS. A cursor this server did not issue must be a 400,
// not an empty page: answered with no results, a client bug and a tampering
// attempt both look exactly like the end of the trail, and an investigator
// concludes it stopped there.
func TestACursorTheServerDidNotIssueIsRefusedNotAnsweredEmpty(t *testing.T) {
	members := &pagingMembers{err: tenancy.ErrInvalidAuditCursor}
	app := auditPageApp(t, members)

	status, body := auditPageRequest(t, app, "?cursor=forged")
	if status != http.StatusBadRequest {
		t.Fatalf("a forged cursor = %d, want 400: %v", status, body)
	}
	if events, present := body["events"]; present && events != nil {
		t.Fatalf("a refused page still returned an events field: %v", events)
	}
}

// The cursor reaches the store and the next cursor reaches the client. Without
// both halves the route paginates once and then loops on page one forever.
func TestTheCursorTravelsBothWays(t *testing.T) {
	members := &pagingMembers{page: tenancy.AuditPage{
		Entries:    []tenancy.MembershipAudit{{ID: "aud_1", Action: "membership.role"}},
		NextCursor: "cursor-for-page-two",
	}}
	app := auditPageApp(t, members)

	status, body := auditPageRequest(t, app, "?cursor=cursor-for-page-one&limit=25")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %v", status, body)
	}
	if members.lastCursor != "cursor-for-page-one" {
		t.Fatalf("the store received cursor %q — the query parameter was dropped", members.lastCursor)
	}
	if members.lastLimit != 25 {
		t.Fatalf("the store received limit %d, want 25", members.lastLimit)
	}
	if got, _ := body["next_cursor"].(string); got != "cursor-for-page-two" {
		t.Fatalf("next_cursor = %q — the client cannot ask for page two", got)
	}
	if events, _ := body["events"].([]any); len(events) != 1 {
		t.Fatalf("events = %v", body["events"])
	}
}

// A last page carries an EMPTY cursor rather than omitting the field.
//
// Present-and-empty, not absent, and the difference matters to a client
// written against two server versions: an omitted field is indistinguishable
// from a server too old to paginate, so a client would have to guess whether
// "no cursor" means "no more pages" or "no pagination here". Always emitting
// it makes `while (next_cursor)` correct against every version.
func TestTheLastPageCarriesAnEmptyCursorRatherThanOmittingIt(t *testing.T) {
	members := &pagingMembers{page: tenancy.AuditPage{
		Entries: []tenancy.MembershipAudit{{ID: "aud_1"}},
	}}
	app := auditPageApp(t, members)

	status, body := auditPageRequest(t, app, "")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	cursor, present := body["next_cursor"]
	if !present {
		t.Fatal("next_cursor is absent, so a client cannot tell an exhausted trail from a server that does not paginate")
	}
	if got, _ := cursor.(string); got != "" {
		t.Fatalf("the last page carried a cursor: %q", got)
	}
}
