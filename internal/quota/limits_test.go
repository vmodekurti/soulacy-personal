// limits_test.go — MU-024 criterion 1: limits at six levels with documented
// precedence, and the precedence is conjunctive rather than override.
package quota

import (
	"strings"
	"testing"
)

func policy(entries map[Scope]Limit) *Policy { return NewPolicy(entries) }

func subject() Subject {
	return Subject{
		OrganizationID: "org_a", WorkspaceID: "ws_a", PrincipalID: "usr_a",
		AgentID: "bot", Provider: "anthropic", Model: "claude-x",
	}
}

// THE property. The tempting reading of "precedence" is that the narrowest
// configured level wins and broader ones are ignored — under which a generous
// per-agent limit would let one agent spend past its organization's budget.
// A narrower scope is a SUBSET of a broader one, so its limit can only ever
// restrict further, never license.
func TestANarrowerLimitCannotLicenseSpendingPastABroaderOne(t *testing.T) {
	p := policy(map[Scope]Limit{
		{Level: LevelOrganization, ID: "org_a"}: {DailyMicros: 1_000},
		{Level: LevelAgent, ID: "bot"}:          {DailyMicros: 999_999},
	})
	tightest := p.Tightest(subject())
	if tightest.DailyMicros != 1_000 {
		t.Fatalf("effective daily = %d, want the organization's 1000 — the agent limit overrode it", tightest.DailyMicros)
	}
	// And the rejection can name who is actually out of budget. "You have
	// $0.00 remaining" is unactionable; "the organization's budget" is not.
	if tightest.DailyScope.Level != LevelOrganization {
		t.Fatalf("binding scope = %s, want organization", tightest.DailyScope)
	}
}

// The other direction: a narrower limit that IS tighter does bind.
func TestTheTightestLimitBindsWhicheverLevelItIsAt(t *testing.T) {
	p := policy(map[Scope]Limit{
		{Level: LevelDeployment}:                {DailyMicros: 1_000_000},
		{Level: LevelOrganization, ID: "org_a"}: {DailyMicros: 500_000},
		{Level: LevelWorkspace, ID: "ws_a"}:     {DailyMicros: 100_000},
		{Level: LevelAgent, ID: "bot"}:          {DailyMicros: 10_000},
	})
	tightest := p.Tightest(subject())
	if tightest.DailyMicros != 10_000 || tightest.DailyScope.Level != LevelAgent {
		t.Fatalf("got %d at %s, want 10000 at agent", tightest.DailyMicros, tightest.DailyScope)
	}
}

// Zero means "not limited here", not "limited to zero". A naive min() over
// unconfigured levels forbids everything.
func TestAnUnconfiguredLevelDoesNotForbidEverything(t *testing.T) {
	p := policy(map[Scope]Limit{
		{Level: LevelWorkspace, ID: "ws_a"}: {DailyMicros: 50_000},
	})
	tightest := p.Tightest(subject())
	if tightest.DailyMicros != 50_000 {
		t.Fatalf("daily = %d, want 50000", tightest.DailyMicros)
	}
	if tightest.MonthlyMicros != 0 {
		t.Fatalf("monthly = %d; an unconfigured dimension became a ceiling", tightest.MonthlyMicros)
	}
	if tightest.Concurrency != 0 {
		t.Fatalf("concurrency = %d; an unconfigured dimension became a ceiling", tightest.Concurrency)
	}
}

// Within one level, an entry naming the entity beats that level's default.
func TestAnEntityEntryBeatsItsLevelsDefault(t *testing.T) {
	p := policy(map[Scope]Limit{
		{Level: LevelWorkspace}:             {DailyMicros: 10_000},
		{Level: LevelWorkspace, ID: "ws_a"}: {DailyMicros: 90_000},
	})
	if got := p.Tightest(subject()).DailyMicros; got != 90_000 {
		t.Fatalf("daily = %d, want the workspace's own 90000", got)
	}
	// A workspace with no entry of its own falls back to the default.
	other := subject()
	other.WorkspaceID = "ws_unlisted"
	if got := p.Tightest(other).DailyMicros; got != 10_000 {
		t.Fatalf("unlisted workspace daily = %d, want the default 10000", got)
	}
}

// A workspace dimension is what the old global budget lacked: one tenant's
// runaway agent exhausted a single pool and every other tenant was refused
// over a budget they had not spent.
func TestTwoWorkspacesGetSeparateCeilings(t *testing.T) {
	p := policy(map[Scope]Limit{
		{Level: LevelDeployment}:            {DailyMicros: 1_000_000},
		{Level: LevelWorkspace, ID: "ws_a"}: {DailyMicros: 100_000},
		{Level: LevelWorkspace, ID: "ws_b"}: {DailyMicros: 200_000},
	})
	a, b := subject(), subject()
	b.WorkspaceID = "ws_b"
	if p.Tightest(a).DailyMicros == p.Tightest(b).DailyMicros {
		t.Fatal("two workspaces share one ceiling")
	}
	if p.Tightest(b).DailyMicros != 200_000 {
		t.Fatalf("ws_b = %d", p.Tightest(b).DailyMicros)
	}
}

// The model level names a provider/model pair, and a limit there is about the
// model rather than about who is calling it.
func TestAModelLimitAppliesToThatModelOnly(t *testing.T) {
	p := policy(map[Scope]Limit{
		{Level: LevelModel, ID: "anthropic/claude-x"}: {DailyTokens: 1_000},
	})
	if got := p.Tightest(subject()).DailyTokens; got != 1_000 {
		t.Fatalf("tokens = %d, want the model limit", got)
	}
	cheaper := subject()
	cheaper.Model = "claude-cheap"
	if got := p.Tightest(cheaper).DailyTokens; got != 0 {
		t.Fatalf("a different model inherited the limit: %d", got)
	}
}

// Precedence is documented by being derivable: the resolution is broadest
// first, and every applicable level is listed.
func TestPrecedenceIsBroadestFirstAndConjunctive(t *testing.T) {
	p := policy(map[Scope]Limit{
		{Level: LevelDeployment}:                      {DailyMicros: 5},
		{Level: LevelOrganization, ID: "org_a"}:       {DailyMicros: 5},
		{Level: LevelWorkspace, ID: "ws_a"}:           {DailyMicros: 5},
		{Level: LevelPrincipal, ID: "usr_a"}:          {DailyMicros: 5},
		{Level: LevelAgent, ID: "bot"}:                {DailyMicros: 5},
		{Level: LevelModel, ID: "anthropic/claude-x"}: {DailyMicros: 5},
	})
	applicable := p.Resolve(subject())
	if len(applicable) != 6 {
		t.Fatalf("resolved %d levels, want all 6 — precedence is conjunctive", len(applicable))
	}
	for i, want := range Levels() {
		if applicable[i].Scope.Level != want {
			t.Fatalf("position %d is %s, want %s", i, applicable[i].Scope.Level, want)
		}
	}
	described := p.Describe(subject())
	if !strings.HasPrefix(described, "all of:") {
		t.Fatalf("description reads as an override rather than a conjunction: %q", described)
	}
	for _, level := range Levels() {
		if !strings.Contains(described, level.String()) {
			t.Fatalf("description omits %s: %q", level, described)
		}
	}
}

// A nil or empty policy constrains nothing — the personal-deployment answer.
func TestAnEmptyPolicyConstrainsNothing(t *testing.T) {
	var nilPolicy *Policy
	if got := nilPolicy.Resolve(subject()); got != nil {
		t.Fatalf("nil policy resolved %v", got)
	}
	empty := NewPolicy(nil)
	if tightest := empty.Tightest(subject()); tightest.DailyMicros != 0 || tightest.Concurrency != 0 {
		t.Fatalf("empty policy produced ceilings: %+v", tightest)
	}
	// A configured-but-all-zero entry is dropped rather than stored as a
	// ceiling of zero.
	zeroed := NewPolicy(map[Scope]Limit{{Level: LevelWorkspace, ID: "ws_a"}: {}})
	if got := zeroed.Tightest(subject()).DailyMicros; got != 0 {
		t.Fatalf("an all-zero limit became a ceiling: %d", got)
	}
}

// A level that limits ONE dimension must not thereby impose a zero ceiling on
// the others. This is the mutation a naive min() survives: with every level
// unconfigured for a dimension the answer is zero either way, so the bug only
// shows when a level limits something else.
func TestALevelLimitingOneDimensionDoesNotZeroTheRest(t *testing.T) {
	p := policy(map[Scope]Limit{
		{Level: LevelWorkspace, ID: "ws_a"}: {DailyMicros: 50_000},
		{Level: LevelAgent, ID: "bot"}:      {Concurrency: 2},
	})
	tightest := p.Tightest(subject())
	if tightest.DailyMicros != 50_000 {
		t.Fatalf("daily = %d — the agent's unset daily became a ceiling of zero", tightest.DailyMicros)
	}
	if tightest.Concurrency != 2 {
		t.Fatalf("concurrency = %d — the workspace's unset concurrency became a ceiling of zero", tightest.Concurrency)
	}
	if tightest.MonthlyMicros != 0 || tightest.DailyTokens != 0 {
		t.Fatalf("a dimension nobody configured became a ceiling: %+v", tightest)
	}
}
