// workspace_test.go — the cross-tenant isolation contract for accounting.
//
// Spend is unusual among the stores here: it is both confidential (it reveals
// another team's activity, model choices, and volume) and *rivalrous* (a
// shared ceiling means the busiest tenant starves the rest). So the failure
// mode is not only a leak — it is a denial of service, and a budget that
// silently stops being enforced.
package costs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/wsroot"
)

func newCostStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "costs.db")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func spend(t *testing.T, s *Store, workspace, subject, agentID string, micros int64) {
	t.Helper()
	if err := s.Record(context.Background(), UsageRecord{
		Workspace: workspace, Subject: subject, AgentID: agentID,
		Provider: "paid", Model: "model", TotalTokens: 100,
		CostMicros: micros, CostUSD: float64(micros) / 1e6,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record %s: %v", workspace, err)
	}
}

// Spend with no owner is spend that counts against no budget, so it is refused
// rather than filed under the empty workspace.
func TestAccountingWithoutAWorkspaceIsRefused(t *testing.T) {
	store, _ := newCostStore(t)
	ctx := context.Background()
	if err := store.Record(ctx, UsageRecord{AgentID: "bot", Provider: "paid", Model: "model", CostMicros: 10}); !errors.Is(err, ErrWorkspaceRequired) {
		t.Errorf("record without a workspace = %v", err)
	}
	if _, err := store.SumCostMicrosSince(ctx, "", time.Time{}); !errors.Is(err, ErrWorkspaceRequired) {
		t.Errorf("sum without a workspace = %v", err)
	}
	if err := store.Reserve(ctx, "", "r1", "usr", "bot", "paid", 10, 10, time.Now().Add(time.Minute)); !errors.Is(err, ErrWorkspaceRequired) {
		t.Errorf("reserve without a workspace = %v", err)
	}
	if _, err := store.Chargeback(ctx, "", time.Time{}, nil); !errors.Is(err, ErrWorkspaceRequired) {
		t.Errorf("chargeback without a workspace = %v", err)
	}
}

// Recorded spend is confidential: totals, per-agent breakdowns and chargeback
// rows all reveal what another team is doing.
func TestRecordedSpendIsInvisibleAcrossWorkspaces(t *testing.T) {
	store, _ := newCostStore(t)
	ctx := context.Background()
	spend(t, store, "ws_a", "usr_alice", "assistant", 1_000)
	spend(t, store, "ws_b", "usr_bob", "assistant", 9_000)

	for _, tc := range []struct {
		workspace string
		want      int64
	}{{"ws_a", 1_000}, {"ws_b", 9_000}} {
		total, err := store.SumCostMicrosSince(ctx, tc.workspace, time.Now().Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if total != tc.want {
			t.Errorf("%s total = %d, want its own %d", tc.workspace, total, tc.want)
		}
		rows, err := store.SumByAgent(ctx, tc.workspace, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].TotalTokens != 100 {
			t.Errorf("%s per-agent = %+v", tc.workspace, rows)
		}
		charge, err := store.Chargeback(ctx, tc.workspace, time.Now().Add(-time.Hour), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(charge) != 1 || charge[0].CostMicros != tc.want {
			t.Errorf("%s chargeback = %+v", tc.workspace, charge)
		}
		listed, err := store.ListUsage(ctx, tc.workspace, time.Now().Add(-time.Hour), 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(listed) != 1 || listed[0].Workspace != tc.workspace {
			t.Errorf("%s listing = %+v", tc.workspace, listed)
		}
	}
	// A workspace that has spent nothing sees nothing, not the deployment total.
	if total, err := store.SumCostMicrosSince(ctx, "ws_c", time.Now().Add(-time.Hour)); err != nil || total != 0 {
		t.Fatalf("an unrelated workspace saw %d micros: %v", total, err)
	}
}

// The rivalrous half: one tenant's recorded spend must not consume another
// tenant's ceiling. A shared budget would let the busiest tenant starve the
// rest — and make one tenant's activity observable to another as rejections.
func TestOneWorkspacesSpendDoesNotExhaustAnothersBudget(t *testing.T) {
	store, _ := newCostStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	policy := ReservationPolicy{
		Now:          now,
		DailyStart:   time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
		MonthlyStart: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC),
		// A ceiling that one tenant's spend alone would exhaust.
		GlobalDailyMicros: 1_500,
	}
	spend(t, store, "ws_a", "usr_alice", "assistant", 1_400)

	// ws_a is nearly out of headroom and must be rejected.
	err := store.TryReserve(ctx, "ws_a", "res-a", "usr_alice", "assistant", "paid", 1_000, 10, now.Add(time.Minute), policy)
	var rejected *ReservationRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("the spending workspace was admitted past its ceiling: %v", err)
	}
	// ws_b has spent nothing and must be admitted at the same instant.
	if err := store.TryReserve(ctx, "ws_b", "res-b", "usr_bob", "assistant", "paid", 1_000, 10, now.Add(time.Minute), policy); err != nil {
		t.Fatalf("another workspace's spend exhausted this one's budget: %v", err)
	}
}

// The bug this pins: a reservation row written without a workspace matches no
// scoped capacity read, so it is invisible to the very ceiling it is supposed
// to consume. The budget then silently stops being enforced — which is worse
// than failing, because nothing surfaces.
func TestInFlightReservationsConsumeTheirOwnWorkspacesCeiling(t *testing.T) {
	store, _ := newCostStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	policy := ReservationPolicy{
		Now:               now,
		DailyStart:        time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
		MonthlyStart:      time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC),
		GlobalDailyMicros: 1_500,
	}
	// First admission takes most of the ceiling and stays in flight.
	if err := store.TryReserve(ctx, "ws_a", "res-1", "usr_alice", "assistant", "paid", 1_400, 10, now.Add(time.Minute), policy); err != nil {
		t.Fatalf("first admission rejected: %v", err)
	}
	reserved, err := store.ReservedCostMicros(ctx, "ws_a", now)
	if err != nil {
		t.Fatal(err)
	}
	if reserved != 1_400 {
		t.Fatalf("the reservation is invisible to its own workspace: reserved = %d", reserved)
	}
	// The second admission in the same workspace must see it and be refused.
	err = store.TryReserve(ctx, "ws_a", "res-2", "usr_alice", "assistant", "paid", 1_000, 10, now.Add(time.Minute), policy)
	var rejected *ReservationRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("an in-flight reservation did not consume its own ceiling: %v", err)
	}
	// Another workspace sees none of it.
	if other, err := store.ReservedCostMicros(ctx, "ws_b", now); err != nil || other != 0 {
		t.Fatalf("another workspace saw %d micros reserved: %v", other, err)
	}
	if err := store.TryReserve(ctx, "ws_b", "res-3", "usr_bob", "assistant", "paid", 1_000, 10, now.Add(time.Minute), policy); err != nil {
		t.Fatalf("another workspace's in-flight reservation blocked this one: %v", err)
	}
}

// A release must free only its own workspace's headroom.
func TestReleaseCannotFreeAnotherWorkspacesReservation(t *testing.T) {
	store, _ := newCostStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.Reserve(ctx, "ws_a", "res-1", "usr_alice", "assistant", "paid", 500, 10, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(ctx, "ws_b", "res-1"); err != nil {
		t.Fatal(err)
	}
	reserved, err := store.ReservedCostMicros(ctx, "ws_a", now)
	if err != nil {
		t.Fatal(err)
	}
	if reserved != 500 {
		t.Fatalf("another workspace's release freed this one's reservation: reserved = %d", reserved)
	}
	if err := store.Release(ctx, "ws_a", "res-1"); err != nil {
		t.Fatal(err)
	}
	if reserved, err := store.ReservedCostMicros(ctx, "ws_a", now); err != nil || reserved != 0 {
		t.Fatalf("the owner's release did not take effect: %d %v", reserved, err)
	}
}

// A costs.db written before tenancy keeps working: rows are assigned to the
// personal workspace, which is what a single-user installation's spend was.
// Getting this wrong would not leak — it would stop the ceiling being
// enforced, silently.
func TestExistingAccountingMigratesToPersonal(t *testing.T) {
	store, path := newCostStore(t)
	ctx := context.Background()
	spend(t, store, wsroot.PersonalWorkspaceID, "", "assistant", 2_000)
	if err := store.Reserve(ctx, wsroot.PersonalWorkspaceID, "res-1", "", "assistant", "paid", 500, 10, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Simulate rows from before the column was used.
	for _, table := range []string{"token_usage", "cost_reservations"} {
		if _, err := store.db.Exec(`UPDATE ` + table + ` SET workspace = ''`); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	total, err := reopened.SumCostMicrosSince(ctx, wsroot.PersonalWorkspaceID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if total != 2_000 {
		t.Fatalf("pre-existing spend stopped counting after migration: %d", total)
	}
	reserved, err := reopened.ReservedCostMicros(ctx, wsroot.PersonalWorkspaceID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if reserved != 500 {
		t.Fatalf("a pre-existing reservation stopped consuming the ceiling: %d", reserved)
	}
}
