// quotapolicy_test.go — MU-024 criterion 1: limits configured at six levels
// reach the reservation transaction, and tighten the flat config rather than
// replacing it.
package costs

import (
	"testing"

	"github.com/soulacy/soulacy/internal/quota"
)

func governorWithPolicy(t *testing.T, entries map[quota.Scope]quota.Limit) *Governor {
	t.Helper()
	g := &Governor{}
	g.SetQuotaPolicy(quota.NewPolicy(entries))
	return g
}

func flatPolicy() ReservationPolicy {
	return ReservationPolicy{
		GlobalDailyMicros: 1_000_000, GlobalMonthlyMicros: 10_000_000,
		UserDailyMicros: 500_000, AgentDailyMicros: 500_000,
		UserTokenLimit: 100_000, AgentTokenLimit: 100_000,
	}
}

// The values used to come from one flat process-wide config applied
// identically to every tenant: an operator could not give one customer a
// larger budget than another, nor express an organization ceiling at all.
func TestAWorkspaceEntryTightensTheDeploymentCeiling(t *testing.T) {
	g := governorWithPolicy(t, map[quota.Scope]quota.Limit{
		{Level: quota.LevelWorkspace, ID: "ws_small"}: {DailyMicros: 10_000},
	})
	small := g.applyQuotaPolicy(flatPolicy(), quotaSubject{WorkspaceID: "ws_small"})
	if small.GlobalDailyMicros != 10_000 {
		t.Fatalf("ws_small daily = %d, want its own 10000", small.GlobalDailyMicros)
	}
	// A workspace with no entry keeps the deployment-wide value.
	other := g.applyQuotaPolicy(flatPolicy(), quotaSubject{WorkspaceID: "ws_other"})
	if other.GlobalDailyMicros != 1_000_000 {
		t.Fatalf("ws_other daily = %d, want the flat 1000000", other.GlobalDailyMicros)
	}
}

// THE combining rule. A per-workspace entry must not raise a limit above the
// deployment-wide one the operator set — that is the "narrower scope licenses
// more" inversion the precedence design rejects.
func TestAPolicyValueNeverLoosensTheOperatorsConfig(t *testing.T) {
	g := governorWithPolicy(t, map[quota.Scope]quota.Limit{
		{Level: quota.LevelWorkspace, ID: "ws_greedy"}:  {DailyMicros: 999_999_999},
		{Level: quota.LevelPrincipal, ID: "usr_greedy"}: {DailyMicros: 999_999_999},
		{Level: quota.LevelAgent, ID: "bot_greedy"}:     {DailyMicros: 999_999_999},
	})
	got := g.applyQuotaPolicy(flatPolicy(), quotaSubject{
		WorkspaceID: "ws_greedy", Subject: "usr_greedy", AgentID: "bot_greedy",
	})
	flat := flatPolicy()
	if got.GlobalDailyMicros != flat.GlobalDailyMicros {
		t.Fatalf("a workspace entry raised the deployment ceiling: %d", got.GlobalDailyMicros)
	}
	if got.UserDailyMicros != flat.UserDailyMicros {
		t.Fatalf("a principal entry raised the user ceiling: %d", got.UserDailyMicros)
	}
	if got.AgentDailyMicros != flat.AgentDailyMicros {
		t.Fatalf("an agent entry raised the agent ceiling: %d", got.AgentDailyMicros)
	}
}

// An organization ceiling binds every workspace under it, which is the level
// the flat config had no way to express.
func TestAnOrganizationCeilingBindsItsWorkspaces(t *testing.T) {
	g := governorWithPolicy(t, map[quota.Scope]quota.Limit{
		{Level: quota.LevelOrganization, ID: "org_a"}: {DailyMicros: 5_000},
		{Level: quota.LevelWorkspace, ID: "ws_a"}:     {DailyMicros: 50_000},
	})
	got := g.applyQuotaPolicy(flatPolicy(), quotaSubject{OrganizationID: "org_a", WorkspaceID: "ws_a"})
	if got.GlobalDailyMicros != 5_000 {
		t.Fatalf("daily = %d — the workspace's larger entry escaped its organization", got.GlobalDailyMicros)
	}
}

// A model-level limit reaches the transaction too.
func TestAModelCeilingBinds(t *testing.T) {
	g := governorWithPolicy(t, map[quota.Scope]quota.Limit{
		{Level: quota.LevelModel, ID: "anthropic/expensive"}: {DailyMicros: 100},
	})
	expensive := g.applyQuotaPolicy(flatPolicy(), quotaSubject{Provider: "anthropic", Model: "expensive"})
	if expensive.GlobalDailyMicros != 100 {
		t.Fatalf("expensive model daily = %d", expensive.GlobalDailyMicros)
	}
	cheap := g.applyQuotaPolicy(flatPolicy(), quotaSubject{Provider: "anthropic", Model: "cheap"})
	if cheap.GlobalDailyMicros != 1_000_000 {
		t.Fatalf("a different model inherited the limit: %d", cheap.GlobalDailyMicros)
	}
}

// No policy configured must be byte-identical to the flat behaviour — that is
// what a personal deployment has always had.
func TestWithNoPolicyTheFlatConfigIsUntouched(t *testing.T) {
	g := &Governor{}
	got := g.applyQuotaPolicy(flatPolicy(), quotaSubject{WorkspaceID: "ws_a", Subject: "usr_a", AgentID: "bot"})
	if got != flatPolicy() {
		t.Fatalf("an unconfigured governor altered the policy: %+v", got)
	}
}

// A level limiting one dimension must not zero the others. Same rule as
// quota.tighter64, restated here — getting it wrong in either place turns an
// unconfigured limit into a ban on everything.
func TestALevelLimitingOneDimensionDoesNotZeroTheRest(t *testing.T) {
	g := governorWithPolicy(t, map[quota.Scope]quota.Limit{
		{Level: quota.LevelWorkspace, ID: "ws_a"}: {DailyMicros: 1_000},
	})
	got := g.applyQuotaPolicy(flatPolicy(), quotaSubject{WorkspaceID: "ws_a"})
	if got.GlobalMonthlyMicros != 10_000_000 {
		t.Fatalf("monthly = %d — an unset dimension became a ceiling of zero", got.GlobalMonthlyMicros)
	}
	if got.UserTokenLimit != 100_000 || got.AgentTokenLimit != 100_000 {
		t.Fatalf("token limits were zeroed: %+v", got)
	}
}

// Token ceilings resolve at their own levels.
func TestTokenCeilingsResolvePerPrincipalAndAgent(t *testing.T) {
	g := governorWithPolicy(t, map[quota.Scope]quota.Limit{
		{Level: quota.LevelPrincipal, ID: "usr_a"}: {DailyTokens: 5},
		{Level: quota.LevelAgent, ID: "bot"}:       {DailyTokens: 7},
	})
	got := g.applyQuotaPolicy(flatPolicy(), quotaSubject{Subject: "usr_a", AgentID: "bot"})
	if got.UserTokenLimit != 5 {
		t.Fatalf("user tokens = %d", got.UserTokenLimit)
	}
	if got.AgentTokenLimit != 7 {
		t.Fatalf("agent tokens = %d", got.AgentTokenLimit)
	}
}

// BuildPolicy returns nil when nothing is configured, so the governor
// short-circuits and a deployment that configured no quotas pays for no
// lookup at all.
func TestBuildPolicyIsNilWhenNothingIsConfigured(t *testing.T) {
	if got := BuildPolicy(quota.Limit{}, nil, nil, nil, nil, nil); got != nil {
		t.Fatalf("an unconfigured deployment built a policy: %+v", got)
	}
	// An entity present with all-zero values is dropped rather than becoming a
	// ceiling of zero — an operator leaving a key in place must not forbid
	// everything for that entity.
	zeroed := BuildPolicy(quota.Limit{}, nil, LevelLimits{"ws_a": {}}, nil, nil, nil)
	if zeroed != nil {
		if tightest := zeroed.Tightest(quota.Subject{WorkspaceID: "ws_a"}); tightest.DailyMicros != 0 {
			t.Fatalf("an all-zero entry became a ceiling: %d", tightest.DailyMicros)
		}
	}
}

// The empty key is a level's default, so "every workspace gets $5" is
// expressible alongside per-workspace overrides.
func TestTheEmptyKeyIsALevelDefault(t *testing.T) {
	policy := BuildPolicy(quota.Limit{},
		nil,
		LevelLimits{"": DollarsToLimit(5, 0, 0, 0), "ws_big": DollarsToLimit(50, 0, 0, 0)},
		nil, nil, nil)
	if policy == nil {
		t.Fatal("no policy built")
	}
	if got := policy.Tightest(quota.Subject{WorkspaceID: "ws_unlisted"}).DailyMicros; got != 5_000_000 {
		t.Fatalf("unlisted workspace = %d, want the $5 default", got)
	}
	if got := policy.Tightest(quota.Subject{WorkspaceID: "ws_big"}).DailyMicros; got != 50_000_000 {
		t.Fatalf("ws_big = %d, want its own $50", got)
	}
}
