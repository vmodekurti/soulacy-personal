package requestctx

import (
	"context"
	"testing"
)

func TestIdentityCopiesAuthorityInputs(t *testing.T) {
	scopes := []string{"chat"}
	id, err := New(Input{Subject: " user ", OrganizationID: "org", WorkspaceID: "ws", MembershipID: "mem", Role: "OWNER", Scopes: scopes, RequestID: "req"})
	if err != nil {
		t.Fatal(err)
	}
	scopes[0] = "admin"
	returned := id.Scopes()
	returned[0] = "config"
	if got := id.Scopes()[0]; got != "chat" {
		t.Fatalf("scope mutated through caller: %q", got)
	}
	if id.Subject() != "user" || id.Role() != "owner" {
		t.Fatalf("identity was not normalized: subject=%q role=%q", id.Subject(), id.Role())
	}
	if got, ok := From(With(context.Background(), id)); !ok || got.WorkspaceID() != "ws" {
		t.Fatalf("context round trip failed: %#v %v", got, ok)
	}
}

func TestIdentityRejectsIncompleteAuthority(t *testing.T) {
	if _, err := New(Input{Subject: "user"}); err == nil {
		t.Fatal("expected incomplete identity to be rejected")
	}
}
