// tokenquota_test.go — the daily token quota, at the place that enforces it.
//
// `ratelimit.per_user_tokens_day` and `per_agent_tokens_day` were implemented
// TWICE. internal/ratelimit kept in-memory 24h buckets, a recorder, two
// sweepers and two middlewares mounted on every chat route — and nothing
// anywhere called the recorder, so every bucket was permanently zero and both
// middlewares compared zero against the limit and allowed the request.
//
// That version had eleven passing tests. Every one of them filled a bucket by
// hand and then asserted the middleware refused, which is the failure shape
// that recurs on this branch: testing a helper directly rather than the
// production call site. Eleven green tests, and the quota did not exist.
//
// So this test does not fill anything by hand. It records usage the way the
// engine does, through the store, and then asks the reservation path — the one
// an LLM call actually goes through — whether the next call is admitted.
package costs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func quotaStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func dailyPolicy(now time.Time, userTokens, agentTokens int64) ReservationPolicy {
	return ReservationPolicy{
		Now:              now,
		DailyStart:       time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
		MonthlyStart:     time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC),
		TokenWindowStart: now.Add(-24 * time.Hour),
		UserTokenLimit:   userTokens,
		AgentTokenLimit:  agentTokens,
	}
}

// Recorded usage counts against the quota. The version this replaces failed
// exactly here: usage was recorded in one place and the limit checked against
// another, so the two never met.
func TestRecordedUsageIsWhatTheDailyTokenQuotaCountsAgainst(t *testing.T) {
	store := quotaStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := store.Record(ctx, UsageRecord{
		Workspace: "ws_a", Subject: "usr_alice", AgentID: "support-bot",
		Provider: "openai", Model: "gpt-4o-mini", TotalTokens: 900,
	}); err != nil {
		t.Fatal(err)
	}

	// Under the limit: admitted.
	if err := store.TryReserve(ctx, "ws_a", "res-1", "usr_alice", "support-bot", "openai",
		0, 50, now.Add(time.Minute), dailyPolicy(now, 1000, 0)); err != nil {
		t.Fatalf("a call inside the quota was refused: %v", err)
	}
	_ = store.Release(ctx, "ws_a", "res-1")

	// Over it: refused, and refused as a quota rejection rather than as a
	// generic error, so a caller can tell "you are out of budget" from "the
	// database is unreachable".
	err := store.TryReserve(ctx, "ws_a", "res-2", "usr_alice", "support-bot", "openai",
		0, 500, now.Add(time.Minute), dailyPolicy(now, 1000, 0))
	var rejected *ReservationRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("a call past the daily token quota returned %v, want a reservation rejection", err)
	}
}

// The reservation is what makes this different from the bucket it replaces.
// A read-then-allow check lets N concurrent requests all see the same
// under-limit total and all proceed; that is the moment a quota is tested, and
// the only one where the two designs differ.
func TestAnInFlightReservationCountsBeforeItsTokensAreRecorded(t *testing.T) {
	store := quotaStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Nothing recorded yet. The first call reserves most of the budget.
	if err := store.TryReserve(ctx, "ws_a", "res-1", "usr_alice", "support-bot", "openai",
		0, 900, now.Add(time.Minute), dailyPolicy(now, 1000, 0)); err != nil {
		t.Fatalf("the first call was refused: %v", err)
	}

	// A second concurrent call sees the RESERVATION, not just recorded usage.
	err := store.TryReserve(ctx, "ws_a", "res-2", "usr_alice", "support-bot", "openai",
		0, 500, now.Add(time.Minute), dailyPolicy(now, 1000, 0))
	var rejected *ReservationRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("a concurrent call was admitted past the quota (%v) — the check is "+
			"read-then-allow rather than a reservation", err)
	}

	// Releasing the first returns the budget, so a failed call does not
	// permanently consume a quota it never spent.
	if err := store.Release(ctx, "ws_a", "res-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.TryReserve(ctx, "ws_a", "res-3", "usr_alice", "support-bot", "openai",
		0, 500, now.Add(time.Minute), dailyPolicy(now, 1000, 0)); err != nil {
		t.Fatalf("the budget was not returned by Release: %v", err)
	}
}

// The quota is per workspace. Agent IDs are unique per workspace, not per
// deployment, so a quota keyed by agent alone charged two tenants' identically
// named agents to one budget — and whichever was busier exhausted the other's.
func TestOneWorkspacesTokenSpendDoesNotExhaustAnothers(t *testing.T) {
	store := quotaStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := store.Record(ctx, UsageRecord{
		Workspace: "ws_a", Subject: "usr_a", AgentID: "support-bot",
		Provider: "openai", Model: "gpt-4o-mini", TotalTokens: 5000,
	}); err != nil {
		t.Fatal(err)
	}

	policy := dailyPolicy(now, 0, 1000)
	if err := store.TryReserve(ctx, "ws_a", "res-a", "usr_a", "support-bot", "openai",
		0, 10, now.Add(time.Minute), policy); err == nil {
		t.Fatal("ws_a was admitted past its own exhausted agent quota")
	}
	if err := store.TryReserve(ctx, "ws_b", "res-b", "usr_b", "support-bot", "openai",
		0, 10, now.Add(time.Minute), policy); err != nil {
		t.Fatalf("ws_b was refused because ANOTHER workspace's same-named agent spent its quota: %v", err)
	}
}

// A quota of zero is off, which is what every existing deployment has. Product
// invariant 7: an installation that never configured one must not acquire one.
func TestAZeroQuotaAdmitsEverything(t *testing.T) {
	store := quotaStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := store.Record(ctx, UsageRecord{
		Workspace: "ws_a", Subject: "usr_a", AgentID: "bot",
		Provider: "openai", Model: "gpt-4o-mini", TotalTokens: 10_000_000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.TryReserve(ctx, "ws_a", "res-1", "usr_a", "bot", "openai",
		0, 1_000_000, now.Add(time.Minute), dailyPolicy(now, 0, 0)); err != nil {
		t.Fatalf("an unconfigured quota refused a call: %v", err)
	}
}

func TestPublicDemoPerUserQuotaDoesNotThrottleOrdinaryWorkspaces(t *testing.T) {
	cfg := GovernanceConfig{PerUserTokensDay: 50_000, PerUserTokensWorkspaceID: "ws_demo"}
	if got := configuredPerUserTokenLimit(cfg, "ws_demo"); got != 50_000 {
		t.Fatalf("demo quota = %d, want 50000", got)
	}
	if got := configuredPerUserTokenLimit(cfg, "ws_otg"); got != 0 {
		t.Fatalf("ordinary workspace inherited demo quota %d", got)
	}
	if got := configuredPerUserTokenLimit(GovernanceConfig{PerUserTokensDay: 75_000}, "ws_otg"); got != 75_000 {
		t.Fatalf("unscoped deployment quota = %d, want 75000", got)
	}
}

func TestWorkspaceAdministratorPerUserQuotaIsPublishedByWorkspace(t *testing.T) {
	g := NewGovernor(quotaStore(t), nil, GovernanceConfig{})
	g.SetWorkspaceUserTokenLimits(map[string]int64{"ws_otg": 500_000})
	if got := g.workspaceUserTokenLimit("ws_otg"); got != 500_000 {
		t.Fatalf("workspace quota = %d, want 500000", got)
	}
	if got := g.workspaceUserTokenLimit("ws_other"); got != 0 {
		t.Fatalf("unconfigured workspace quota = %d, want unlimited", got)
	}
}
