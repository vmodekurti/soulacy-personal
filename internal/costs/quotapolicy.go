package costs

import (
	"github.com/soulacy/soulacy/internal/quota"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// quotapolicy.go — MU-024 criterion 1, the last piece: limits configured at
// six levels reach the reservation transaction.
//
// WHAT WAS ALREADY RIGHT, AND WHAT WAS NOT. TryReserve has always carried the
// tenant predicate on every capacity query, so a configured ceiling was
// ENFORCED per workspace rather than across the deployment. What was missing
// was that the ceiling's VALUE came from one flat process-wide config —
// `runtime.costs.daily_budget_usd` and friends — applied identically to every
// tenant. An operator could not give one customer a larger budget than
// another, could not express an organization ceiling above its workspaces at
// all, and could not cap a single expensive model.
//
// quota.Policy supplies those values per subject. The transaction is unchanged;
// only the numbers it is handed differ.
//
// THE COMBINING RULE IS THE POINT. A resolved policy value never REPLACES the
// operator's flat config — it is tightened against it. Replacing would let a
// per-workspace entry raise a limit above the deployment-wide one the operator
// set, which is the "narrower scope licenses more" inversion the whole
// precedence design rejects (see docs/QUOTA_PRECEDENCE.md). Both are ceilings;
// the tighter binds.

// SetQuotaPolicy installs the multi-level limit policy.
//
// Optional: nil keeps the flat-config behaviour exactly, which is what a
// personal deployment has always had and what every existing test asserts.
func (g *Governor) SetQuotaPolicy(policy *quota.Policy) {
	g.quotaMu.Lock()
	g.quotaPolicy = policy
	g.quotaMu.Unlock()
}

// SetWorkspaceUserTokenLimits installs per-user rolling token allowances for
// individual workspaces. The map is copied so callers cannot mutate a live
// admission policy after publication.
func (g *Governor) SetWorkspaceUserTokenLimits(limits map[string]int64) {
	next := make(map[string]int64, len(limits))
	for workspaceID, limit := range limits {
		workspaceID = wsroot.Normalize(workspaceID)
		if limit > 0 {
			next[workspaceID] = limit
		}
	}
	g.quotaMu.Lock()
	g.workspaceUserTokenLimits = next
	g.quotaMu.Unlock()
}

func (g *Governor) workspaceUserTokenLimit(workspaceID string) int64 {
	g.quotaMu.RLock()
	defer g.quotaMu.RUnlock()
	return g.workspaceUserTokenLimits[wsroot.Normalize(workspaceID)]
}

func (g *Governor) quota() *quota.Policy {
	g.quotaMu.RLock()
	defer g.quotaMu.RUnlock()
	return g.quotaPolicy
}

// quotaSubject describes who is asking, for limit resolution.
type quotaSubject struct {
	OrganizationID string
	WorkspaceID    string
	Subject        string
	AgentID        string
	Provider       string
	Model          string
}

// applyQuotaPolicy tightens a ReservationPolicy with the resolved multi-level
// limits for one subject.
//
// Returns the policy unchanged when no quota policy is configured, so the
// single-tenant path never pays for a lookup it has no use for.
func (g *Governor) applyQuotaPolicy(policy ReservationPolicy, who quotaSubject) ReservationPolicy {
	resolved := g.quota()
	if resolved == nil {
		return policy
	}
	tightest := resolved.Tightest(quota.Subject{
		OrganizationID: who.OrganizationID,
		WorkspaceID:    wsroot.Normalize(who.WorkspaceID),
		PrincipalID:    who.Subject,
		AgentID:        who.AgentID,
		Provider:       who.Provider,
		Model:          who.Model,
	})

	// The "global" scopes in TryReserve are global WITHIN one workspace, so
	// the tightest resolved daily/monthly ceiling is exactly what they should
	// carry. A deployment-level entry and a workspace-level entry both land
	// here already collapsed by Tightest, with the narrower one winning only
	// when it is actually narrower.
	policy.GlobalDailyMicros = tighten(policy.GlobalDailyMicros, tightest.DailyMicros)
	policy.GlobalMonthlyMicros = tighten(policy.GlobalMonthlyMicros, tightest.MonthlyMicros)

	// Principal and agent ceilings are separate scopes in the transaction
	// because they are queried with different predicates. A resolved limit at
	// those levels tightens the corresponding one; a resolved limit at a
	// BROADER level has already been folded into the global scopes above, so
	// it is not double-counted here.
	if principal := resolved.Tightest(quota.Subject{PrincipalID: who.Subject}); principal.DailyMicros > 0 {
		policy.UserDailyMicros = tighten(policy.UserDailyMicros, principal.DailyMicros)
	}
	if principal := resolved.Tightest(quota.Subject{PrincipalID: who.Subject}); principal.DailyTokens > 0 {
		policy.UserTokenLimit = tighten(policy.UserTokenLimit, principal.DailyTokens)
	}
	if agent := resolved.Tightest(quota.Subject{AgentID: who.AgentID}); agent.DailyMicros > 0 {
		policy.AgentDailyMicros = tighten(policy.AgentDailyMicros, agent.DailyMicros)
	}
	if agent := resolved.Tightest(quota.Subject{AgentID: who.AgentID}); agent.DailyTokens > 0 {
		policy.AgentTokenLimit = tighten(policy.AgentTokenLimit, agent.DailyTokens)
	}
	return policy
}

// tighten keeps the smaller POSITIVE ceiling.
//
// Zero means "not limited", so it never wins — the same rule quota.tighter64
// applies, restated here because getting it wrong in either place turns an
// unconfigured limit into a ban on everything.
func tighten(current, candidate int64) int64 {
	if candidate <= 0 {
		return current
	}
	if current <= 0 || candidate < current {
		return candidate
	}
	return current
}

// QuotaPolicy returns the policy currently installed.
//
// Exported for the gateway's per-workspace limits (MU-030 criterion 1), which
// has to be able to assert that an edit reached the reservation path — a limit
// that is stored and not installed is one a customer believes in and nothing
// enforces.
func (g *Governor) QuotaPolicy() *quota.Policy { return g.quota() }
