// Package concurrency is optimistic concurrency control for mutable resources
// (MU-028: "prevent stale writes during collaboration").
//
// THE FAILURE IT PREVENTS is not dramatic and that is why it needs a
// mechanism. Two members open the same agent. One saves. The other saves
// thirty seconds later from a form rendered before the first save, and the
// first member's change is gone — no error, no conflict, nothing in the UI
// that looks wrong. The only evidence is that work disappeared, which is
// usually noticed days later by the person who did it.
package concurrency

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrStale reports a write against a version that is no longer current.
var ErrStale = errors.New("concurrency: the resource changed since it was read")

// ErrPreconditionRequired reports an update that supplied no version at all.
//
// Distinct from ErrStale because the remedies differ: a stale write means
// "reload and reconcile", a missing precondition means "your client is not
// participating in concurrency control at all" — and a client that never sends
// a version is the one that will silently overwrite every time.
var ErrPreconditionRequired = errors.New("concurrency: this update requires the current version")

// ETag is a content-derived version token.
//
// CONTENT-DERIVED, NOT A COUNTER. A counter is the obvious design and it needs
// coordination: two gateway replicas both incrementing "version 4" produce two
// different "version 5"s for different content, and a client that read one
// happily overwrites the other. A hash of the stored bytes needs no
// coordination at all — any replica computes the same token for the same
// content, and different content cannot collide into the same token.
//
// It also makes a no-op write detectable: saving a form nobody edited produces
// the same ETag, so the update is not a conflict for anyone else to resolve.
func ETag(content []byte) string {
	sum := sha256.Sum256(content)
	// Weak-validator syntax deliberately avoided: this is a strong validator
	// in the HTTP sense — byte-identical content, byte-identical token.
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// Match reports whether a supplied precondition matches the current ETag.
//
// Exact comparison after normalising the quoting clients vary on. A prefix or
// case-insensitive comparison would let a truncated token pass, and a token
// that "nearly matches" is precisely the stale write this exists to catch.
func Match(supplied, current string) bool {
	return normalize(supplied) == normalize(current) && normalize(current) != ""
}

// Wildcard is the HTTP If-Match wildcard: "any current version".
const Wildcard = "*"

// Precondition is what a caller supplied.
type Precondition struct {
	// Value is the raw If-Match header or version field. Empty means none.
	Value string
	// RequireMatch, when true, refuses an update that supplies nothing.
	//
	// This is the decision that makes the mechanism worth having. If a missing
	// precondition is allowed through, concurrency control is opt-in — and the
	// client that forgets is exactly the one that overwrites silently, every
	// time, which is the bug. Required in multi-user deployments; a
	// single-tenant install has nobody to conflict with and keeps working
	// unchanged (product invariant 7).
	RequireMatch bool
}

// Check evaluates a precondition against the current version.
//
// Returns nil when the write may proceed.
func (p Precondition) Check(current string) error {
	supplied := normalize(p.Value)
	if supplied == "" {
		if p.RequireMatch {
			return ErrPreconditionRequired
		}
		return nil
	}
	if supplied == Wildcard {
		// "*" means "it must exist", which for an update it does.
		if normalize(current) == "" {
			return ErrStale
		}
		return nil
	}
	if !Match(supplied, current) {
		return ErrStale
	}
	return nil
}

// Conflict is what a 409 carries.
//
// The CURRENT version and enough about the other actor to act on. A bare 409
// forces the client to refetch and re-diff, and it races: between the 409 and
// the refetch the resource can change again. Worse, "somebody else changed
// this" without saying WHO is unactionable in a team — the remedy is usually a
// conversation, and the person cannot have it.
type Conflict struct {
	Resource string `json:"resource"`
	// CurrentVersion lets the client reconcile without a second round trip.
	CurrentVersion string `json:"current_version"`
	// SuppliedVersion echoes what the caller sent, so a client with several
	// in-flight edits can tell which one lost.
	SuppliedVersion string `json:"supplied_version,omitempty"`
	// LastModifiedBy and LastModifiedAt identify the other actor. MU-028
	// criterion 5 asks audit history to identify both conflicting actors; a
	// conflict the loser never sees attributed is half that.
	LastModifiedBy string `json:"last_modified_by,omitempty"`
	LastModifiedAt string `json:"last_modified_at,omitempty"`
	Message        string `json:"message"`
}

// NewConflict builds the 409 body.
func NewConflict(resource, supplied, current, modifiedBy, modifiedAt string) Conflict {
	message := fmt.Sprintf("%s changed since you loaded it", resource)
	if strings.TrimSpace(modifiedBy) != "" {
		message = fmt.Sprintf("%s was changed by %s since you loaded it", resource, modifiedBy)
	}
	return Conflict{
		Resource:        resource,
		CurrentVersion:  current,
		SuppliedVersion: supplied,
		LastModifiedBy:  modifiedBy,
		LastModifiedAt:  modifiedAt,
		Message:         message + ". Reload to see the current version, or overwrite deliberately.",
	}
}

func normalize(tag string) string {
	tag = strings.TrimSpace(tag)
	// W/ prefixes and surrounding quotes vary by client and proxy; the token
	// inside is what identifies the content.
	tag = strings.TrimPrefix(tag, "W/")
	tag = strings.TrimPrefix(tag, "w/")
	return strings.Trim(tag, `"`)
}
