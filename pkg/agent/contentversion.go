package agent

import (
	"crypto/sha256"
	"encoding/hex"

	"gopkg.in/yaml.v3"
)

// contentversion.go — the immutable identity of a definition's content.
//
// WHY THIS IS NOT `Definition.Version`. That field is a string the *author*
// types into SOUL.yaml. It is documentation: usually empty, frequently stale,
// never updated by an edit, and entirely under the control of whoever wrote
// the file. Anything that pins "which definition did this" to it is recording
// a value that does not change when the definition does — which is the one
// thing a version has to do.
//
// Two places relied on exactly that. `runs.Run.AgentVersion` says in its own
// comment that it "pins what actually ran" so that "an agent edited later must
// not change what this run means", and was filled from the authored string.
// `internal/gateway`'s ETag derived a token the same way this does, separately,
// so a definition had two identities that were equal only by coincidence.
//
// DERIVED, NOT ALLOCATED. A counter would need coordination between gateway
// replicas; a hash of the content needs none. Any process computes the same
// token for the same definition, and different content cannot collide into the
// same token. SourcePath and LoadedAt are `json:"-"`, so where a definition
// happens to live and when this process read it are correctly not part of its
// identity — otherwise two replicas would disagree about identical files.

// ContentVersion returns a stable, content-derived version token for the
// definition, or "" if it cannot be serialised.
//
// It is deliberately the same derivation the HTTP ETag uses — see
// internal/gateway.resourceETag, which delegates here — so that "the version I
// am editing", "the version that was deployed" and "the version this run
// executed" are one string rather than three that have to be correlated.
//
// HASHED OVER YAML, NOT JSON, AND THAT IS THE LOAD-BEARING PART. YAML is the
// storage format, so this is the identity of the file rather than of one
// in-memory representation of it — and a JSON hash is not stable across the
// round trip through that file. A nil slice encodes as `null` in JSON and as
// `[]` in YAML, and parsing `[]` back yields an empty non-nil slice, so:
//
//   - an agent created through the API (nil slices, straight from a JSON body)
//     and the same agent after a gateway restart (empty slices, parsed from
//     its own SOUL.yaml) had different tokens for identical content; and
//   - every client's held version was invalidated by a restart, producing a
//     conflict against an editor who does not exist.
//
// Both failures are invisible until two people are editing, which is exactly
// the situation the token exists for. `yaml.Marshal` renders nil and empty
// slices identically, emits struct fields in declaration order, and sorts map
// keys, so it is both canonical and deterministic here.
func (d *Definition) ContentVersion() string {
	if d == nil {
		return ""
	}
	encoded, err := yaml.Marshal(d)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:16])
}
