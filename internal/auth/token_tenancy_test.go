// token_tenancy_test.go — the isolation contract for locally-issued tokens.
// internal/ownership/catalog.go names this file as the isolation evidence for
// internal/auth/jwt.go.
//
// An access token is a claim about who is calling *and where*. Until this
// change the issuer minted only (subject, email, role): every locally-issued
// token carried no workspace, authenticated perfectly, and then resolved to the
// personal workspace everywhere downstream. A signed-in member of ws_a acting
// with personal's authority is not an authentication failure — it is a valid
// credential for the wrong tenant, which is worse, because nothing looks wrong.
package auth

import (
	"context"
	"testing"
	"time"
)

func tenancyIssuer(t *testing.T) *Issuer {
	t.Helper()
	iss, err := newIssuer("test-secret-value", 15*time.Minute, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(iss.Close)
	return iss
}

// The workspace a token was issued for survives signing and verification.
func TestAnIssuedTokenCarriesItsWorkspace(t *testing.T) {
	iss := tenancyIssuer(t)
	access, _, _, err := iss.IssueFor(TokenIdentity{
		Subject: "usr_a", Email: "a@example.com", Role: "developer",
		OrganizationID: "org_a", WorkspaceID: "ws_a", MembershipID: "mem_a",
	})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := iss.VerifyAccess(access)
	if err != nil {
		t.Fatal(err)
	}
	if claims.WorkspaceID != "ws_a" || claims.OrganizationID != "org_a" || claims.MembershipID != "mem_a" {
		t.Fatalf("tenancy lost between issue and verify: %+v", claims)
	}
	if claims.Role != "developer" || claims.Subject != "usr_a" {
		t.Fatalf("identity lost: %+v", claims)
	}
}

// A refresh re-reads the membership rather than replaying the one minted when
// the member signed in. A refresh family lives for days; a member moved to
// another workspace, or demoted, must not keep acting under the old authority.
func TestARefreshAdoptsTheCurrentMembershipRatherThanTheOldOne(t *testing.T) {
	iss := tenancyIssuer(t)
	_, refresh, _, err := iss.IssueFor(TokenIdentity{
		Subject: "usr_a", Email: "a@example.com", Role: "admin",
		OrganizationID: "org_a", WorkspaceID: "ws_a", MembershipID: "mem_a",
	})
	if err != nil {
		t.Fatal(err)
	}

	access, _, _, err := iss.RefreshAuthorized(refresh, func(subject string) (TokenIdentity, bool) {
		return TokenIdentity{
			Subject: "attacker-attempt", Role: "viewer",
			OrganizationID: "org_a", WorkspaceID: "ws_b", MembershipID: "mem_b",
		}, true
	})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := iss.VerifyAccess(access)
	if err != nil {
		t.Fatal(err)
	}
	if claims.WorkspaceID != "ws_b" || claims.Role != "viewer" || claims.MembershipID != "mem_b" {
		t.Fatalf("the refresh replayed the old authority: %+v", claims)
	}
	// The subject belongs to the family and is not the resolver's to change: a
	// resolver that could rewrite it would turn refresh into impersonation.
	if claims.Subject != "usr_a" {
		t.Fatalf("the refresh changed the subject to %q", claims.Subject)
	}
	// The email is preserved when the resolver does not supply one, so a
	// re-issued token does not lose the display identity.
	if claims.Email != "a@example.com" {
		t.Fatalf("email lost across refresh: %q", claims.Email)
	}
}

// With no resolver — a personal deployment — the stored tenancy is reused, so
// a refresh keeps issuing exactly the token those installations always had.
func TestARefreshWithoutAResolverKeepsTheStoredIdentity(t *testing.T) {
	iss := tenancyIssuer(t)
	_, refresh, _, err := iss.IssueFor(TokenIdentity{Subject: "usr_a", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	access, _, _, err := iss.Refresh(refresh)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := iss.VerifyAccess(access)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "usr_a" || claims.Role != "admin" || claims.WorkspaceID != "" {
		t.Fatalf("personal refresh changed shape: %+v", claims)
	}
}

// A subject the resolver refuses gets no token at all. Issuing an un-tenanted
// one instead would hand somebody with no active membership the personal
// workspace — the deployment's own.
func TestAnUnresolvableSubjectIsRefusedRatherThanGivenPersonal(t *testing.T) {
	engine := &Engine{}
	engine.SetTokenIdentityResolver(func(ctx context.Context, subject string) (TokenIdentity, bool) {
		if subject == "member" {
			return TokenIdentity{Subject: subject, Role: "developer", WorkspaceID: "ws_a", MembershipID: "mem_a"}, true
		}
		return TokenIdentity{}, false
	})

	got, ok := engine.tokenIdentityFor(context.Background(), TokenIdentity{Subject: "member", Email: "m@example.com", Role: "viewer"})
	if !ok {
		t.Fatal("a member with an active membership was refused")
	}
	if got.WorkspaceID != "ws_a" || got.Role != "developer" {
		t.Fatalf("the resolver's tenancy was not adopted: %+v", got)
	}
	if got.Email != "m@example.com" {
		t.Fatalf("email was dropped when the resolver did not supply one: %+v", got)
	}
	if _, ok := engine.tokenIdentityFor(context.Background(), TokenIdentity{Subject: "outsider"}); ok {
		t.Fatal("a subject with no active membership was given a token")
	}

	// No resolver at all is a personal deployment, which keeps working.
	personal := &Engine{}
	if got, ok := personal.tokenIdentityFor(context.Background(), TokenIdentity{Subject: "solo", Role: "admin"}); !ok || got.WorkspaceID != "" {
		t.Fatalf("personal issuance changed: %+v ok=%v", got, ok)
	}
}
