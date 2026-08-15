package tenancy

import (
	"context"
	"errors"
	"testing"
)

func TestPersonalResolverVerifiesWorkspaceSelector(t *testing.T) {
	r := NewPersonalResolver(PersonalTenant{OrganizationID: "org", WorkspaceID: "ws", UserID: "usr", MembershipID: "mem"})
	m, err := r.ResolveMembership(context.Background(), "admin", "ws")
	if err != nil || m.Role != "owner" || m.UserID != "usr" {
		t.Fatalf("resolve = %#v, %v", m, err)
	}
	if _, err := r.ResolveMembership(context.Background(), "admin", "other"); !errors.Is(err, ErrMembershipNotFound) {
		t.Fatalf("wrong workspace error = %v", err)
	}
}
