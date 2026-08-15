package gateway

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/costs"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// costScope binds the accounting store to one workspace.
//
// It exists for the same reason actionScope does, and for one more: spend is
// rivalrous. A read that forgets the tenant does not only show one team another
// team's numbers — the same omission on the enforcement path lets one tenant
// consume another's ceiling. Routing every handler through this type means a
// new cost query cannot be written without naming whose money it is.
type costScope struct {
	store       *costs.Store
	workspaceID string
}

// costs returns the request's scoped view of accounting.
func (s *Server) costs(c *fiber.Ctx) costScope {
	return costScope{store: s.costStore, workspaceID: s.costWorkspace(c)}
}

// costsForWorkspace scopes accounting without a request, for background work
// that already knows which tenant it is acting for.
func (s *Server) costsForWorkspace(workspaceID string) costScope {
	return costScope{store: s.costStore, workspaceID: wsroot.Normalize(workspaceID)}
}

// Available reports whether cost tracking is wired at all.
func (a costScope) Available() bool { return a.store != nil }

func (a costScope) SumByAgent(ctx context.Context, since time.Time) ([]costs.AgentCost, error) {
	return a.store.SumByAgent(ctx, a.workspaceID, since)
}

func (a costScope) SumBySession(ctx context.Context, agentID string, since time.Time) ([]costs.SessionCost, error) {
	return a.store.SumBySession(ctx, a.workspaceID, agentID, since)
}

func (a costScope) ListUsage(ctx context.Context, since time.Time, limit int) ([]costs.UsageRecord, error) {
	return a.store.ListUsage(ctx, a.workspaceID, since, limit)
}

func (a costScope) Chargeback(ctx context.Context, since time.Time, groupBy []string) ([]costs.ChargebackRow, error) {
	return a.store.Chargeback(ctx, a.workspaceID, since, groupBy)
}

func (a costScope) StatsSince(ctx context.Context, since time.Time) (costs.UsageStats, error) {
	return a.store.StatsSince(ctx, a.workspaceID, since)
}

func (a costScope) ReservedCostMicros(ctx context.Context, now time.Time) (int64, error) {
	return a.store.ReservedCostMicros(ctx, a.workspaceID, now)
}

// ListReconciliations and ReconcileProvider are deliberately unscoped: a
// reconciliation compares against the provider's invoice, and providers bill
// the deployment rather than the tenant. They are exposed here so handlers
// still reach accounting through one type, not because they carry a workspace.
func (a costScope) ListReconciliations(ctx context.Context, limit int) ([]costs.Reconciliation, error) {
	return a.store.ListReconciliations(ctx, limit)
}

func (a costScope) ReconcileProvider(ctx context.Context, provider string, start, end time.Time, actualMicros int64, source string) (costs.Reconciliation, error) {
	return a.store.ReconcileProvider(ctx, provider, start, end, actualMicros, source)
}

// SessionMetrics is per-session run detail. It resolves through the session
// store's own authorization, so it takes no workspace of its own.
func (a costScope) SessionMetrics(ctx context.Context, sessionID string) (costs.SessionMetrics, bool, error) {
	return a.store.SessionMetrics(ctx, sessionID)
}
