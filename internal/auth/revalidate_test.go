package auth

import (
	"context"
	"github.com/soulacy/soulacy/internal/auth/apikeys"
	"testing"
)

func TestRevalidateCredentialTracksRevocationScopesAndIdentity(t *testing.T) {
	e := newTestEngine(t, "jwt", "static-key")
	store := &fakeAPIKeyStore{keys: map[string]apikeys.APIKey{"sk_key": {ID: "owner", Name: "User", Scopes: []string{"agents:write"}}}}
	e.SetAPIKeyStore(store)
	ctx := context.Background()
	cl, err := e.RevalidateCredential(ctx, "sk_key")
	if err != nil || cl.Subject != "owner" || !cl.Allows("agents", "write") || cl.Allows("config", "write") {
		t.Fatal(cl, err)
	}
	store.keys["sk_key"] = apikeys.APIKey{ID: "owner", Name: "User", Scopes: []string{"agents:read"}}
	cl, err = e.RevalidateCredential(ctx, "sk_key")
	if err != nil || cl.Allows("agents", "write") {
		t.Fatal(cl, err)
	}
	delete(store.keys, "sk_key")
	if _, err = e.RevalidateCredential(ctx, "sk_key"); err == nil {
		t.Fatal("revoked credential accepted")
	}
	access, refresh, _, err := e.issuer.Issue("alice", "alice@example.test", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	cl, err = e.RevalidateCredential(ctx, access)
	if err != nil || cl.Subject != "alice" || cl.Role != "viewer" {
		t.Fatal(cl, err)
	}
	if _, err = e.RevalidateCredential(ctx, refresh); err == nil {
		t.Fatal("refresh token authorizes writes")
	}
	for _, token := range []string{"", "garbage", "static-key?secret"} {
		if _, err = e.RevalidateCredential(ctx, token); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
	cl, err = e.RevalidateCredential(ctx, "static-key")
	if err != nil || cl.Email != "admin" || cl.Role != "admin" {
		t.Fatal(cl, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = e.RevalidateCredential(cancelled, "static-key"); err == nil {
		t.Fatal("cancelled credential allowed")
	}
}
