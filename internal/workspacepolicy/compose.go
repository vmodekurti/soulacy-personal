package workspacepolicy

import (
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/quota"
)

// compose.go — folding stored per-workspace limits into the operator's policy.
//
// THIS FUNCTION IS THE SECURITY BOUNDARY, which is why it is the only way a
// stored number becomes an effective one. A workspace owner can write any
// value they like; `Compose` takes the smaller positive of theirs and the
// deployment's, so writing a large number gets them exactly what the operator
// already allowed.
//
// Enforcing it here rather than validating on write is deliberate. A write-time
// check protects the endpoints that remember to call it, and this store will
// grow more of them — an import, a template, a migration, a support tool. A
// composition rule protects every one of them, including the ones not written
// yet, because none of them can produce an effective ceiling without going
// through this.

// Compose returns a quota.Policy carrying the operator's configured entries
// with each workspace's stored entry tightened onto it.
//
// `configured` may be nil, which is what a deployment that configured nothing
// has. In that case a stored entry is the only ceiling at its level — which is
// correct and not a loosening: no configured limit means no configured
// ceiling, so anything a workspace sets on itself is strictly more
// restrictive than what it had.
func Compose(configured *quota.Policy, stored []Policy) *quota.Policy {
	entries := map[quota.Scope]quota.Limit{}
	if configured != nil {
		for scope, limit := range configured.Entries() {
			entries[scope] = limit
		}
	}
	for _, policy := range stored {
		workspaceID := strings.TrimSpace(policy.WorkspaceID)
		if workspaceID == "" {
			continue
		}
		scope := quota.Scope{Level: quota.LevelWorkspace, ID: workspaceID}
		entries[scope] = tightenLimit(entries[scope], policy.Limit())
	}
	if len(entries) == 0 {
		// nil rather than an empty policy: the governor short-circuits on nil,
		// so a deployment with nothing configured anywhere pays for no lookup.
		return nil
	}
	return quota.NewPolicy(entries)
}

// tightenLimit keeps the smaller POSITIVE value of each field.
//
// Zero means unlimited on both sides, so it must never win a comparison — the
// naive `min` would let an unset field in either input erase a real ceiling in
// the other, which is the loosening this whole file exists to prevent.
func tightenLimit(base, override quota.Limit) quota.Limit {
	return quota.Limit{
		DailyMicros:   tighter(base.DailyMicros, override.DailyMicros),
		MonthlyMicros: tighter(base.MonthlyMicros, override.MonthlyMicros),
		DailyTokens:   tighter(base.DailyTokens, override.DailyTokens),
		Concurrency:   int(tighter(int64(base.Concurrency), int64(override.Concurrency))),
	}
}

func tighter(a, b int64) int64 {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case b < a:
		return b
	default:
		return a
	}
}

// Retention is one workspace's effective retention windows.
type Retention struct {
	ConversationHistory time.Duration
	ActionEvents        time.Duration
	AuditLogs           time.Duration
}

// EffectiveRetention folds a stored policy onto the deployment's windows.
//
// SHORTER WINS, which is the same direction as the budgets and for the same
// reason: a workspace may hold its own data for less time than the deployment
// does, never more. Letting a tenant lengthen retention would let them impose
// storage cost — and, in a jurisdiction with a deletion obligation, legal
// exposure — on an operator who set a shorter window deliberately.
//
// An unparseable or non-positive stored window is IGNORED rather than treated
// as zero. Zero reads downstream as "retention disabled", so a corrupt value
// would silently turn pruning off for that tenant, which is the exact inverse
// of what somebody setting a retention policy wants.
func EffectiveRetention(deployment Retention, stored Policy) Retention {
	return Retention{
		ConversationHistory: shorter(deployment.ConversationHistory, parseWindow(stored.ConversationHistory)),
		ActionEvents:        shorter(deployment.ActionEvents, parseWindow(stored.ActionEvents)),
		AuditLogs:           shorter(deployment.AuditLogs, parseWindow(stored.AuditLogs)),
	}
}

func parseWindow(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

func shorter(deployment, workspace time.Duration) time.Duration {
	switch {
	case workspace <= 0:
		return deployment
	case deployment <= 0:
		return workspace
	case workspace < deployment:
		return workspace
	default:
		return deployment
	}
}
