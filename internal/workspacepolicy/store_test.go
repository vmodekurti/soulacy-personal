package workspacepolicy

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "policies.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// Budgets are stored in MICROS, not as the float an owner typed. A budget
// compared against accumulated spend has to be exact, and float dollars
// accumulate rounding until "$25.00" is not $25.00 — which surfaces as a
// customer refused at $24.999999 with no explanation anyone can give.
func TestABudgetRoundTripsExactly(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	stored, err := store.Set(ctx, "ws_a", "usr_alice", Policy{DailyUSD: 25.55, MonthlyUSD: 500.01, DailyTokens: 1234})
	if err != nil {
		t.Fatal(err)
	}
	if stored.DailyUSD != 25.55 || stored.MonthlyUSD != 500.01 {
		t.Fatalf("round trip = %v/%v, want 25.55/500.01", stored.DailyUSD, stored.MonthlyUSD)
	}
	if stored.Limit().DailyMicros != 25_550_000 {
		t.Fatalf("daily micros = %d, want 25_550_000", stored.Limit().DailyMicros)
	}
	if stored.UpdatedBy != "usr_alice" || stored.UpdatedAt.IsZero() {
		t.Fatalf("provenance not recorded: %+v", stored)
	}
}

// One row per workspace, keyed by the tenant, so one workspace's policy cannot
// become a second row under another's id.
func TestAWorkspaceReadsOnlyItsOwnPolicy(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	if _, err := store.Set(ctx, "ws_a", "usr_a", Policy{DailyUSD: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(ctx, "ws_b", "usr_b", Policy{DailyUSD: 50}); err != nil {
		t.Fatal(err)
	}

	a, err := store.Get(ctx, "ws_a")
	if err != nil {
		t.Fatal(err)
	}
	if a.DailyUSD != 5 {
		t.Fatalf("ws_a daily = %v", a.DailyUSD)
	}
	// A workspace with nothing set gets a zero policy, which composes to "the
	// deployment's limits apply" — never to "zero allowed".
	none, err := store.Get(ctx, "ws_c")
	if err != nil {
		t.Fatal(err)
	}
	if none.DailyUSD != 0 || none.WorkspaceID != "ws_c" {
		t.Fatalf("an unset workspace = %+v", none)
	}
	if !none.Limit().IsZero() {
		t.Fatal("an unset workspace produced a non-zero limit, which would cap it at zero")
	}
}

// A second write replaces rather than accumulating, so clearing a field
// actually clears it.
func TestClearingAFieldActuallyClearsIt(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	if _, err := store.Set(ctx, "ws_a", "usr_a", Policy{DailyUSD: 5, ActionEvents: "720h"}); err != nil {
		t.Fatal(err)
	}
	cleared, err := store.Set(ctx, "ws_a", "usr_a", Policy{DailyUSD: 5})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.ActionEvents != "" {
		t.Fatalf("action_events = %q after being cleared", cleared.ActionEvents)
	}
	if cleared.DailyUSD != 5 {
		t.Fatalf("daily = %v — clearing one field cleared another", cleared.DailyUSD)
	}
}

func TestAnInvalidPolicyIsNotStored(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	if _, err := store.Set(ctx, "ws_a", "usr_a", Policy{ActionEvents: "-1h"}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("a negative retention was stored: %v", err)
	}
	got, err := store.Get(ctx, "ws_a")
	if err != nil {
		t.Fatal(err)
	}
	if got.ActionEvents != "" {
		t.Fatalf("the refused policy was written anyway: %+v", got)
	}
}

// MU-032 criterion 4: a workspace's policy goes with the workspace.
func TestPurgingAWorkspaceRemovesOnlyItsPolicy(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	if _, err := store.Set(ctx, "ws_a", "usr_a", Policy{DailyUSD: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(ctx, "ws_b", "usr_b", Policy{DailyUSD: 50}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.PurgeWorkspace(ctx, "ws_a")
	if err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("purged %d rows, want 1", rows)
	}
	if got, _ := store.Get(ctx, "ws_a"); got.DailyUSD != 0 {
		t.Fatal("the purged workspace still has a policy")
	}
	if got, _ := store.Get(ctx, "ws_b"); got.DailyUSD != 50 {
		t.Fatal("purging one workspace removed another's policy")
	}
}
