package costs

import (
	"strings"

	"github.com/soulacy/soulacy/internal/quota"
)

// quotaconfig.go — turning the YAML into a resolved policy.
//
// Kept beside the governor rather than in internal/config so the mapping from
// "what an operator wrote" to "what the reservation transaction enforces" is
// one function, in the package that consumes it.

// LevelLimits is one level's per-entity configuration. The empty key is that
// level's default.
type LevelLimits map[string]quota.Limit

// BuildPolicy assembles a quota.Policy from per-level maps.
//
// Entities whose limit constrains nothing are dropped by quota.NewPolicy, so
// an operator can leave a key present with all-zero values without accidentally
// forbidding everything for that entity.
func BuildPolicy(deployment quota.Limit, organizations, workspaces, principals, agents, models LevelLimits) *quota.Policy {
	entries := map[quota.Scope]quota.Limit{}
	if !deployment.IsZero() {
		entries[quota.Scope{Level: quota.LevelDeployment}] = deployment
	}
	for level, configured := range map[quota.Level]LevelLimits{
		quota.LevelOrganization: organizations,
		quota.LevelWorkspace:    workspaces,
		quota.LevelPrincipal:    principals,
		quota.LevelAgent:        agents,
		quota.LevelModel:        models,
	} {
		for id, limit := range configured {
			entries[quota.Scope{Level: level, ID: strings.TrimSpace(id)}] = limit
		}
	}
	if len(entries) == 0 {
		// nil rather than an empty policy: the governor short-circuits on nil,
		// so a deployment that configured nothing pays for no lookup at all.
		return nil
	}
	return quota.NewPolicy(entries)
}

// DollarsToLimit converts an operator's USD figures into the micro-dollar
// units the reservation transaction uses.
func DollarsToLimit(dailyUSD, monthlyUSD float64, dailyTokens int64, concurrency int) quota.Limit {
	return quota.Limit{
		DailyMicros:   dollarsToMicros(dailyUSD),
		MonthlyMicros: dollarsToMicros(monthlyUSD),
		DailyTokens:   dailyTokens,
		Concurrency:   concurrency,
	}
}
