// recentauth_test.go — the issuer-level property MU-030 criterion 5 rests on.
//
// It lives here rather than in internal/gateway because the trap is inside the
// issuer: auth_time has to survive token rotation, and only this package can
// drive rotation directly.
package auth

import (
	"testing"
	"time"
)

// recentEnough mirrors the gateway's window check. Duplicated deliberately and
// kept trivial: importing the gateway from here would be a cycle, and the
// property under test is "does auth_time move", not "what is the window".
func recentEnough(claims *Claims, now time.Time) bool {
	if claims == nil || claims.AuthTime <= 0 {
		return false
	}
	return now.UTC().Sub(time.Unix(claims.AuthTime, 0).UTC()) <= 10*time.Minute
}

// THE TRAP. `iat` is the obvious field to reach for and it is wrong: an access
// token rotates silently every fifteen minutes for as long as a browser is
// open, so an iat-freshness check passes forever on a session nobody has
// touched — which is exactly the abandoned session step-up exists to catch. A
// check that cannot fail is worse than no check, because every review
// afterwards reads it as protection.
func TestASilentTokenRotationDoesNotCountAsProvingWhoYouAre(t *testing.T) {
	issuer, err := newIssuer("test-secret", time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	signedInAt := time.Now().UTC().Add(-2 * time.Hour)
	_, refresh, _, err := issuer.IssueFor(TokenIdentity{Subject: "usr_alice", Role: "owner", AuthTime: signedInAt})
	if err != nil {
		t.Fatal(err)
	}

	// Eight silent rotations — a browser left open for two hours.
	//
	// Driven through RefreshAuthorized with a REAL reauthorizer, which is the
	// production path (auth.Engine.HandleRefresh always passes one). The first
	// version of this test called Refresh(), whose reauthorizer is nil, so the
	// branch that must preserve auth_time was never executed and the test
	// passed with that line deleted. Caught by mutation-testing it.
	//
	// The resolver returns a ZERO AuthTime on purpose: it re-reads membership
	// and has no idea when the human last authenticated, so a zero here is
	// exactly what production hands back. If the issuer took it, the step-up
	// clock would silently reset every fifteen minutes.
	reauthorize := func(subject string) (TokenIdentity, bool) {
		return TokenIdentity{Subject: subject, Role: "owner"}, true
	}
	access := ""
	for i := 0; i < 8; i++ {
		access, refresh, _, err = issuer.RefreshAuthorized(refresh, reauthorize)
		if err != nil {
			t.Fatalf("rotation %d: %v", i, err)
		}
	}
	claims, err := issuer.VerifyAccess(access)
	if err != nil {
		t.Fatal(err)
	}

	// The token is freshly minted...
	if claims.IssuedAt == nil || time.Since(claims.IssuedAt.Time) > time.Minute {
		t.Fatalf("iat is not fresh, so this test is not exercising the trap: %v", claims.IssuedAt)
	}
	// ...and the human has not proved anything in two hours.
	if recentEnough(claims, time.Now()) {
		t.Fatal("a session rotated for two hours without a human touching it still counts as recently authenticated")
	}
	if claims.AuthTime != signedInAt.Unix() {
		t.Fatalf("auth_time = %d, want the original sign-in %d — rotation moved it", claims.AuthTime, signedInAt.Unix())
	}
}

// The other half of that property: re-authenticating actually does move it.
func TestReauthenticatingMovesTheClockAndStartsANewFamily(t *testing.T) {
	issuer, err := newIssuer("test-secret", time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, oldRefresh, _, err := issuer.IssueFor(TokenIdentity{
		Subject: "usr_alice", Role: "owner", AuthTime: time.Now().UTC().Add(-2 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	access, _, _, err := issuer.Reauthenticate(TokenIdentity{Subject: "usr_alice", Role: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := issuer.VerifyAccess(access)
	if err != nil {
		t.Fatal(err)
	}
	if !recentEnough(claims, time.Now()) {
		t.Fatal("re-authenticating did not make the session recently authenticated")
	}

	// A NEW family. Step-up is only meaningful if the elevated authority is
	// bound to the credential the human just proved — otherwise a refresh
	// token stolen BEFORE the step-up inherits the elevation on its next
	// rotation, which is the attacker step-up is meant to exclude.
	rotated, _, _, err := issuer.Refresh(oldRefresh)
	if err != nil {
		t.Fatalf("the pre-elevation refresh token should still work for an ordinary session: %v", err)
	}
	stale, err := issuer.VerifyAccess(rotated)
	if err != nil {
		t.Fatal(err)
	}
	if recentEnough(stale, time.Now()) {
		t.Fatal("a refresh token from before the step-up inherited the elevation")
	}
}
