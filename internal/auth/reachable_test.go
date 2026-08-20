package auth

import (
	"testing"

	"go.uber.org/zap"
)

// The one configuration where Effective and Reachable disagree, and the reason
// Reachable exists: jwt mode with an ephemeral issuer and nothing anybody can
// authenticate WITH. The engine is armed and permanently unusable.
func TestJWTWithNoCredentialSourceIsArmedButUnreachable(t *testing.T) {
	e, err := New(Config{Mode: "jwt"}, "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)

	if !e.Effective() {
		t.Fatal("an issuer exists, so the middleware is armed; Effective should say so")
	}
	if e.Reachable() {
		t.Fatal("no static key, no OIDC and no managed keys: the token exchange refuses an empty " +
			"expected secret, so nothing can ever obtain a token this engine accepts")
	}
	// The claim above, checked rather than asserted: an empty api_key must not
	// be accepted as matching an empty configured key.
	if secretEqual("", "") {
		t.Fatal("an empty expected secret matched — every caller would be an administrator")
	}
}

func TestEveryRealCredentialSourceCountsAsReachable(t *testing.T) {
	staticKeyed, err := New(Config{Mode: "apikey"}, "secret", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(staticKeyed.Close)
	if !staticKeyed.Reachable() {
		t.Error("a static API key is a credential a caller can present")
	}

	none, err := New(Config{Mode: "apikey"}, "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(none.Close)
	if none.Reachable() || none.Effective() {
		t.Error("apikey mode with no key has nothing to verify and nothing to present")
	}

	if (*Engine)(nil).Reachable() {
		t.Error("a nil engine is not a credential source")
	}
}
