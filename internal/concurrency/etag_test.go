// etag_test.go — MU-028 criteria 1 and 2: mutable resources expose a version,
// and a stale update is refused with current metadata rather than applied.
package concurrency

import (
	"errors"
	"strings"
	"testing"
)

// The failure this prevents: two members open the same agent, one saves, the
// other saves from a form rendered before that — and the first change is gone
// with no error anywhere.
func TestAStaleUpdateIsRefused(t *testing.T) {
	original := ETag([]byte("agent: v1"))
	current := ETag([]byte("agent: v2 — somebody else saved"))

	stale := Precondition{Value: original, RequireMatch: true}
	if err := stale.Check(current); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale — the second save silently overwrote the first", err)
	}
	// The member who is up to date still writes.
	fresh := Precondition{Value: current, RequireMatch: true}
	if err := fresh.Check(current); err != nil {
		t.Fatalf("a current version was refused: %v", err)
	}
}

// A counter needs coordination: two replicas both incrementing "version 4"
// produce two different "version 5"s for different content. A content hash
// needs none — any replica computes the same token for the same bytes.
func TestTheTokenIsDerivedFromContentNotACounter(t *testing.T) {
	content := []byte("agent: unchanged")
	if ETag(content) != ETag(content) {
		t.Fatal("the same content produced two tokens; replicas would disagree")
	}
	if ETag(content) == ETag([]byte("agent: edited")) {
		t.Fatal("different content produced the same token")
	}
	// A no-op save is therefore not a conflict for anybody to resolve.
	same := Precondition{Value: ETag(content), RequireMatch: true}
	if err := same.Check(ETag(content)); err != nil {
		t.Fatalf("saving an unedited form was treated as a conflict: %v", err)
	}
}

// THE decision that makes the mechanism worth having. If a missing
// precondition is allowed through, concurrency control is opt-in — and the
// client that forgets is exactly the one that overwrites silently every time.
func TestAMissingPreconditionIsRefusedWhereItIsRequired(t *testing.T) {
	current := ETag([]byte("agent: v1"))
	missing := Precondition{RequireMatch: true}
	err := missing.Check(current)
	if !errors.Is(err, ErrPreconditionRequired) {
		t.Fatalf("err = %v, want ErrPreconditionRequired", err)
	}
	// Distinct from stale, because the remedies differ: "reload and reconcile"
	// versus "your client is not participating in concurrency control".
	if errors.Is(err, ErrStale) {
		t.Fatal("a missing precondition is indistinguishable from a stale one")
	}
	// A single-tenant install has nobody to conflict with and keeps working
	// unchanged (invariant 7).
	personal := Precondition{RequireMatch: false}
	if err := personal.Check(current); err != nil {
		t.Fatalf("a personal deployment was refused: %v", err)
	}
}

// A token that "nearly matches" is precisely the stale write this catches.
func TestMatchingIsExactRatherThanApproximate(t *testing.T) {
	current := ETag([]byte("agent: v1"))
	inner := strings.Trim(current, `"`)
	for _, supplied := range []string{
		inner[:8],              // truncated
		inner + "00",           // extended
		strings.ToUpper(inner), // case-shifted
		"",                     // empty is handled separately, never a match
	} {
		if Match(supplied, current) {
			t.Errorf("%q matched %q", supplied, current)
		}
	}
	// Quoting and weak-validator prefixes vary by client and proxy; the token
	// inside is what identifies the content.
	for _, supplied := range []string{current, inner, `W/` + current, `w/` + current, "  " + current + "  "} {
		if !Match(supplied, current) {
			t.Errorf("%q did not match %q despite naming the same content", supplied, current)
		}
	}
}

// An empty current version must never match anything, or a resource with no
// version becomes writable by any precondition.
func TestAnEmptyCurrentVersionMatchesNothing(t *testing.T) {
	if Match("", "") {
		t.Fatal("two empty versions matched")
	}
	if Match(`"abc"`, "") {
		t.Fatal("a supplied version matched an absent one")
	}
	// The wildcard means "it must exist", so it fails against nothing.
	if err := (Precondition{Value: Wildcard, RequireMatch: true}).Check(""); !errors.Is(err, ErrStale) {
		t.Fatalf("wildcard against a missing resource returned %v", err)
	}
	if err := (Precondition{Value: Wildcard, RequireMatch: true}).Check(ETag([]byte("x"))); err != nil {
		t.Fatalf("wildcard against an existing resource returned %v", err)
	}
}

// A bare 409 forces a refetch and races: between the 409 and the refetch the
// resource can change again. And "somebody else changed this" without saying
// who is unactionable in a team, where the remedy is usually a conversation.
func TestTheConflictCarriesEnoughToActOn(t *testing.T) {
	conflict := NewConflict("agent support-bot", `"old"`, `"new"`, "usr_dana", "2026-08-16T12:00:00Z")

	if conflict.CurrentVersion != `"new"` {
		t.Fatal("the conflict does not carry the current version; the client must refetch and race")
	}
	if conflict.SuppliedVersion != `"old"` {
		t.Fatal("the conflict does not echo what was sent; a client with several edits cannot tell which lost")
	}
	if conflict.LastModifiedBy != "usr_dana" {
		t.Fatal("the conflict does not name the other actor")
	}
	if !strings.Contains(conflict.Message, "usr_dana") {
		t.Fatalf("the message does not name who: %q", conflict.Message)
	}
	if !strings.Contains(conflict.Message, "Reload") || !strings.Contains(conflict.Message, "overwrite") {
		t.Fatalf("the message does not offer the choices criterion 3 asks the GUI to present: %q", conflict.Message)
	}
	// With no known actor it still reads sensibly rather than naming nobody.
	anonymous := NewConflict("agent support-bot", "", `"new"`, "", "")
	if strings.Contains(anonymous.Message, "by ") {
		t.Fatalf("an unattributed conflict claims an actor: %q", anonymous.Message)
	}
}
