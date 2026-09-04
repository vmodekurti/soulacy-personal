// Package consent implements the per-case consent decision for Studio "Custom
// Python" nodes (docs/STUDIO_PYTHON_TOOLS.md §13). It is the single source of
// truth for two questions:
//
//   - Authorize: at RUN TIME, may this python node execute? Fail-closed — any
//     code beyond the ReadOnly guardrails needs a consent stamp that matches the
//     exact code (by content hash), covers the capabilities the code requires,
//     and (for system-class code) is permitted by the operator ceiling.
//   - ApplyGrants: at SAVE TIME, stamp each beyond-guardrail node with the
//     user's grant, refusing to save if any such node is ungranted.
//
// Keeping the logic here (pure Go, no cgo) makes the security decision unit-
// testable and reused identically by the gateway save path and the engine
// runtime path.
package consent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/soulacy/soulacy/internal/studio/codeclass"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// Valid scopes for a grant.
const (
	ScopeRun          = "run"           // this run only (re-prompt next time)
	ScopeWorkflow     = "workflow"      // this workflow until the code is edited
	ScopeUntilRevoked = "until_revoked" // until the user revokes it
)

// HashCode returns the content binding for a code blob: first 12 hex chars of
// sha256. A grant is tied to this; editing the code changes it and voids consent.
func HashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])[:12]
}

// Grant is one user approval supplied at save time (parsed from the save
// request), keyed by the node it authorizes.
type Grant struct {
	NodeID       string
	Hash         string   // must match the node's current code hash
	Capabilities []string // capabilities the user approved
	Scope        string
	GrantedBy    string
}

// Authorize decides whether a python node may execute right now. Returns nil
// when allowed; otherwise an error explaining the refusal. Fail-closed:
//
//   - ReadOnly code (no system/network, not dynamic) is always allowed.
//   - Beyond-guardrail code requires node.Consent to be present, its Hash to
//     match the current code, and its Capabilities to cover everything the code
//     needs. Missing/stale/insufficient → refused.
//   - system-class code additionally requires the operator ceiling
//     (allow_system_tools) to be on.
func Authorize(node sdkr.FlowNode, allowSystem bool) error {
	cls := codeclass.Classify(node.Code)
	if !cls.Beyond() {
		return nil // inside the guardrails
	}
	c := node.Consent
	if c == nil {
		return fmt.Errorf("consent: node %q runs beyond guardrails (%v) but has no consent grant", node.ID, beyondLabel(cls))
	}
	if c.Hash != HashCode(node.Code) {
		return fmt.Errorf("consent: node %q code changed since it was consented — re-consent required", node.ID)
	}
	for _, need := range cls.Requires {
		if !contains(c.Capabilities, need) {
			return fmt.Errorf("consent: node %q needs %q capability but consent did not grant it", node.ID, need)
		}
	}
	if contains(cls.Requires, codeclass.CapSystem) && !allowSystem {
		return fmt.Errorf("consent: node %q needs host execution but the server ceiling is off (runtime.allow_system_tools=false)", node.ID)
	}
	return nil
}

// ApplyGrants stamps each beyond-guardrail python node in nodes with the
// matching grant, and returns an error if any such node lacks a valid grant
// (so the save is refused). Nodes inside the guardrails are left untouched (and
// any grant for them is ignored). Mutates nodes in place. Deterministic.
func ApplyGrants(nodes []sdkr.FlowNode, grants []Grant) error {
	byNode := make(map[string]Grant, len(grants))
	for _, g := range grants {
		byNode[g.NodeID] = g
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range nodes {
		n := &nodes[i]
		if n.Code == "" {
			continue
		}
		cls := codeclass.Classify(n.Code)
		if !cls.Beyond() {
			n.Consent = nil // readonly: never carries a stamp
			continue
		}
		g, ok := byNode[n.ID]
		if !ok {
			return fmt.Errorf("consent: node %q runs beyond guardrails (%v) and requires consent to save", n.ID, beyondLabel(cls))
		}
		if g.Hash != HashCode(n.Code) {
			return fmt.Errorf("consent: grant for node %q is for different code (hash mismatch)", n.ID)
		}
		for _, need := range cls.Requires {
			if !contains(g.Capabilities, need) {
				return fmt.Errorf("consent: grant for node %q does not cover required capability %q", n.ID, need)
			}
		}
		scope := g.Scope
		if scope == "" {
			scope = ScopeWorkflow
		}
		n.Consent = &sdkr.FlowConsent{
			Hash:         g.Hash,
			Capabilities: append([]string(nil), g.Capabilities...),
			Scope:        scope,
			GrantedAt:    now,
			GrantedBy:    g.GrantedBy,
		}
	}
	return nil
}

func beyondLabel(c codeclass.Result) string {
	parts := append([]string(nil), c.Requires...)
	if c.Dynamic {
		parts = append(parts, "dynamic")
	}
	return fmt.Sprintf("%v", parts)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
