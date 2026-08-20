// compose_test.go — one property matters more than the rest: a stored entry
// cannot raise a limit.
//
// Everything else in this package is bookkeeping. This is the reason the
// feature is safe to expose to a workspace owner at all, and it is enforced by
// the COMPOSITION rather than by a validation on write — so these tests drive
// Compose, which is the only path from a stored number to an effective one.
package workspacepolicy

import (
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/quota"
)

func configuredPolicy(workspaceID string, limit quota.Limit) *quota.Policy {
	return quota.NewPolicy(map[quota.Scope]quota.Limit{
		{Level: quota.LevelWorkspace, ID: workspaceID}: limit,
	})
}

func effective(t *testing.T, policy *quota.Policy, workspaceID string) quota.Tightest {
	t.Helper()
	if policy == nil {
		return quota.Tightest{}
	}
	return policy.Tightest(quota.Subject{WorkspaceID: workspaceID})
}

// THE PROPERTY. A workspace owner writing a huge number gets what the operator
// already allowed and not a penny more.
func TestAStoredPolicyCannotRaiseAConfiguredCeiling(t *testing.T) {
	configured := configuredPolicy("ws_a", quota.Limit{
		DailyMicros: 25_000_000, MonthlyMicros: 500_000_000, DailyTokens: 1_000_000, Concurrency: 4,
	})
	greedy := Policy{
		WorkspaceID: "ws_a",
		DailyUSD:    1_000_000, MonthlyUSD: 1_000_000, DailyTokens: 999_999_999, Concurrency: 4096,
	}

	got := effective(t, Compose(configured, []Policy{greedy}), "ws_a")
	if got.DailyMicros != 25_000_000 {
		t.Errorf("daily = %d, want the configured 25_000_000 — a stored entry raised it", got.DailyMicros)
	}
	if got.MonthlyMicros != 500_000_000 {
		t.Errorf("monthly = %d, want the configured 500_000_000", got.MonthlyMicros)
	}
	if got.DailyTokens != 1_000_000 {
		t.Errorf("tokens = %d, want the configured 1_000_000", got.DailyTokens)
	}
	if got.Concurrency != 4 {
		t.Errorf("concurrency = %d, want the configured 4", got.Concurrency)
	}
}

// And the direction that IS allowed: a workspace restricting itself further.
func TestAStoredPolicyCanTightenBelowTheConfiguredCeiling(t *testing.T) {
	configured := configuredPolicy("ws_a", quota.Limit{DailyMicros: 25_000_000, Concurrency: 8})
	modest := Policy{WorkspaceID: "ws_a", DailyUSD: 5, Concurrency: 2}

	got := effective(t, Compose(configured, []Policy{modest}), "ws_a")
	if got.DailyMicros != 5_000_000 {
		t.Fatalf("daily = %d, want the stored 5_000_000", got.DailyMicros)
	}
	if got.Concurrency != 2 {
		t.Fatalf("concurrency = %d, want the stored 2", got.Concurrency)
	}
}

// An UNSET field must not erase the other side's ceiling. Zero means unlimited
// on both sides, so a naive min() would let a workspace clear its daily budget
// by setting only a concurrency limit — which is the loosening this whole file
// exists to prevent, arriving through the back door.
func TestAnUnsetFieldDoesNotEraseTheOtherSidesCeiling(t *testing.T) {
	configured := configuredPolicy("ws_a", quota.Limit{DailyMicros: 25_000_000, DailyTokens: 500})
	partial := Policy{WorkspaceID: "ws_a", Concurrency: 2}

	got := effective(t, Compose(configured, []Policy{partial}), "ws_a")
	if got.DailyMicros != 25_000_000 {
		t.Errorf("an unset stored budget erased the configured one: %d", got.DailyMicros)
	}
	if got.DailyTokens != 500 {
		t.Errorf("an unset stored token limit erased the configured one: %d", got.DailyTokens)
	}
	if got.Concurrency != 2 {
		t.Errorf("concurrency = %d, want the stored 2", got.Concurrency)
	}
}

// With nothing configured, a stored entry is the only ceiling — which is a
// tightening, not a loosening: no configured limit means no configured
// ceiling.
func TestWithNothingConfiguredAStoredEntryIsTheOnlyCeiling(t *testing.T) {
	got := effective(t, Compose(nil, []Policy{{WorkspaceID: "ws_a", DailyUSD: 5}}), "ws_a")
	if got.DailyMicros != 5_000_000 {
		t.Fatalf("daily = %d, want the stored 5_000_000", got.DailyMicros)
	}
	// And a deployment with nothing anywhere gets nil, so the governor
	// short-circuits and pays for no lookup.
	if Compose(nil, nil) != nil {
		t.Fatal("an empty composition returned a policy rather than nil")
	}
}

// One workspace's stored entry must not become another's ceiling.
func TestAStoredPolicyBindsOnlyItsOwnWorkspace(t *testing.T) {
	composed := Compose(nil, []Policy{
		{WorkspaceID: "ws_a", DailyUSD: 5},
		{WorkspaceID: "ws_b", DailyUSD: 50},
	})
	if got := effective(t, composed, "ws_a"); got.DailyMicros != 5_000_000 {
		t.Errorf("ws_a daily = %d", got.DailyMicros)
	}
	if got := effective(t, composed, "ws_b"); got.DailyMicros != 50_000_000 {
		t.Errorf("ws_b daily = %d — another workspace's entry bound it", got.DailyMicros)
	}
	if got := effective(t, composed, "ws_c"); got.DailyMicros != 0 {
		t.Errorf("ws_c inherited a limit from another workspace: %d", got.DailyMicros)
	}
}

// Retention goes the same direction, for a sharper reason: letting a tenant
// LENGTHEN retention imposes storage cost — and in a jurisdiction with a
// deletion obligation, legal exposure — on an operator who chose a shorter
// window deliberately.
func TestAWorkspaceMayShortenRetentionButNeverLengthenIt(t *testing.T) {
	deployment := Retention{
		ConversationHistory: 30 * 24 * time.Hour,
		ActionEvents:        90 * 24 * time.Hour,
		AuditLogs:           365 * 24 * time.Hour,
	}
	got := EffectiveRetention(deployment, Policy{
		ConversationHistory: "168h",  // shorter — applies
		ActionEvents:        "8760h", // longer — refused
		AuditLogs:           "",      // unset — deployment's applies
	})
	if got.ConversationHistory != 168*time.Hour {
		t.Errorf("conversation history = %v, want the shorter stored 168h", got.ConversationHistory)
	}
	if got.ActionEvents != deployment.ActionEvents {
		t.Errorf("action events = %v — a workspace lengthened retention", got.ActionEvents)
	}
	if got.AuditLogs != deployment.AuditLogs {
		t.Errorf("audit logs = %v, want the deployment's", got.AuditLogs)
	}
}

// A corrupt or non-positive stored window is IGNORED, not treated as zero.
// Zero reads downstream as "retention disabled", so treating it as a value
// would silently turn pruning off for that tenant — the exact inverse of what
// somebody setting a retention policy wants.
func TestACorruptStoredRetentionFallsBackRatherThanDisablingPruning(t *testing.T) {
	deployment := Retention{ActionEvents: 90 * 24 * time.Hour}
	for _, bad := range []string{"-1h", "0s", "not a duration", "90"} {
		got := EffectiveRetention(deployment, Policy{ActionEvents: bad})
		if got.ActionEvents != deployment.ActionEvents {
			t.Errorf("stored %q produced %v — pruning was disabled by a bad value", bad, got.ActionEvents)
		}
	}
}

// Validate refuses what Compose would otherwise have to defend against, so an
// owner is told at the point of writing rather than discovering it later.
func TestValidateRefusesNegativeAndNonPositiveWindows(t *testing.T) {
	for _, bad := range []Policy{
		{DailyUSD: -1},
		{MonthlyUSD: -1},
		{DailyTokens: -1},
		{Concurrency: -1},
		{ActionEvents: "-1h"},
		{ActionEvents: "0s"},
		{ConversationHistory: "nope"},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("policy %+v was accepted", bad)
		}
	}
	valid := Policy{DailyUSD: 5, ActionEvents: "720h", ConversationHistory: "168h"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a valid policy was refused: %v", err)
	}
	// The refusal has to say WHICH field and why, or an owner clears the whole
	// form to find out.
	err := Policy{ActionEvents: "-1h"}.Validate()
	if !strings.Contains(err.Error(), "action_events") {
		t.Fatalf("the refusal does not name the field: %v", err)
	}
}
