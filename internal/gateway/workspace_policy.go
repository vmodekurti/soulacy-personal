package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/costs"
	"github.com/soulacy/soulacy/internal/quota"
	"github.com/soulacy/soulacy/internal/requestctx"
	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/internal/workspacepolicy"
	"github.com/soulacy/soulacy/internal/wsroot"
)

func zapErr(err error) zap.Field { return zap.Error(err) }

// workspace_policy.go — MU-030 criterion 1 at the HTTP edge: a workspace owner
// or administrator sets budgets, quotas and retention for their own workspace.
//
// SAFE TO EXPOSE because of where the ceiling is enforced, not because of what
// this handler validates. `workspacepolicy.Compose` takes the smaller positive
// of the stored value and the operator's configured one, so an owner writing a
// large number gets whatever the deployment already allowed. This route could
// be replaced by an import, a template or a support tool tomorrow and the
// property would hold, because none of them can turn a stored number into an
// effective ceiling without going through the same composition.
//
// The response therefore reports BOTH: what this workspace asked for, and what
// is actually in force. An owner who sets $1,000,000 and sees $25 in
// `effective` has been told the truth immediately, rather than discovering it
// the first time a run is refused.

// SetWorkspacePolicyStore wires per-workspace limits. The routes answer 503
// until it is present.
func (s *Server) SetWorkspacePolicyStore(store *workspacepolicy.Store) {
	s.workspacePolicies = store
}

func (s *Server) registerWorkspacePolicyRoutes(api fiber.Router) {
	api.Get("/workspace/policy", s.handleGetWorkspacePolicy)
	// Recent auth on the write. Lowering your own budget is harmless; the
	// action worth gating is an unattended session being used to change what a
	// workspace may spend, which is quiet, persists after the session ends,
	// and is exactly what a stolen session is valuable for.
	api.Put("/workspace/policy", s.requireRecentAuth(),
		s.auditing("workspace.policy_set", "workspace", "", s.handleSetWorkspacePolicy))
}

func (s *Server) workspacePolicyResponse(c *fiber.Ctx, stored workspacepolicy.Policy) error {
	return c.JSON(fiber.Map{
		"policy": stored,
		// `effective` is what the reservation transaction will actually
		// enforce. Reporting only what was stored would let an owner who set a
		// number above the deployment's ceiling believe it took — and find out
		// the first time a run is refused, with nothing connecting the two.
		"effective": s.effectiveWorkspaceLimits(stored),
		"retention": s.effectiveWorkspaceRetention(stored),
	})
}

func (s *Server) effectiveWorkspaceLimits(stored workspacepolicy.Policy) fiber.Map {
	composed := workspacepolicy.Compose(s.configuredQuotaPolicy(), []workspacepolicy.Policy{stored})
	if composed == nil {
		return fiber.Map{"daily_usd": 0, "monthly_usd": 0, "daily_tokens": 0,
			"per_user_daily_tokens": s.effectivePerUserDailyTokens(stored), "concurrency": 0}
	}
	tightest := composed.Tightest(quotaSubjectForWorkspace(stored.WorkspaceID))
	return fiber.Map{
		"daily_usd":             float64(tightest.DailyMicros) / 1_000_000,
		"monthly_usd":           float64(tightest.MonthlyMicros) / 1_000_000,
		"daily_tokens":          tightest.DailyTokens,
		"per_user_daily_tokens": s.effectivePerUserDailyTokens(stored),
		"concurrency":           tightest.Concurrency,
	}
}

func (s *Server) effectivePerUserDailyTokens(stored workspacepolicy.Policy) int64 {
	configured := int64(s.config().RateLimit.PerUserTokensDay)
	if demo := s.config().PublicDemo; demo.Enabled && wsroot.Normalize(stored.WorkspaceID) != wsroot.Normalize(demo.WorkspaceID) {
		configured = 0
	}
	if stored.PerUserDailyTokens <= 0 {
		return configured
	}
	if configured <= 0 || stored.PerUserDailyTokens < configured {
		return stored.PerUserDailyTokens
	}
	return configured
}

func workspacePolicyAdmin(c *fiber.Ctx) (requestctx.Identity, error) {
	identity, ok := requestIdentity(c)
	if !ok || (identity.Role() != tenancy.RoleOwner && identity.Role() != tenancy.RoleAdmin) {
		return requestctx.Identity{}, fiber.NewError(fiber.StatusForbidden, "only a workspace owner or admin can manage workspace limits")
	}
	return identity, nil
}

func (s *Server) effectiveWorkspaceRetention(stored workspacepolicy.Policy) fiber.Map {
	deployment := workspacepolicy.Retention{
		ConversationHistory: parseConfiguredWindow(s.config(), func(r config.RetentionConfig) string { return r.ConversationHistory }),
		ActionEvents:        parseConfiguredWindow(s.config(), func(r config.RetentionConfig) string { return r.ActionEvents }),
		AuditLogs:           parseConfiguredWindow(s.config(), func(r config.RetentionConfig) string { return r.AuditLogs }),
	}
	effective := workspacepolicy.EffectiveRetention(deployment, stored)
	return fiber.Map{
		"conversation_history": durationString(effective.ConversationHistory),
		"action_events":        durationString(effective.ActionEvents),
		"audit_logs":           durationString(effective.AuditLogs),
	}
}

func parseConfiguredWindow(cfg *config.Config, pick func(config.RetentionConfig) string) time.Duration {
	if cfg == nil {
		return 0
	}
	parsed, err := time.ParseDuration(pick(cfg.Runtime.Retention))
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

// durationString renders an unset window as "" rather than "0s", because
// downstream "0" means retention DISABLED and a reader should not have to know
// that to interpret this field.
func durationString(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.String()
}

func (s *Server) handleGetWorkspacePolicy(c *fiber.Ctx) error {
	identity, err := workspacePolicyAdmin(c)
	if err != nil {
		return err
	}
	if s.workspacePolicies == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "per-workspace policy is unavailable on this deployment")
	}
	stored, err := s.workspacePolicies.Get(c.UserContext(), identity.WorkspaceID())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "the workspace policy could not be read")
	}
	return s.workspacePolicyResponse(c, stored)
}

func (s *Server) handleSetWorkspacePolicy(c *fiber.Ctx) error {
	identity, err := workspacePolicyAdmin(c)
	if err != nil {
		return err
	}
	if s.workspacePolicies == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "per-workspace policy is unavailable on this deployment")
	}
	var req workspacepolicy.Policy
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body")
	}
	// The workspace comes from the VERIFIED identity, never from the body — a
	// body-supplied id would let an owner of one workspace set another's
	// budget, and the write would look entirely ordinary in the audit trail
	// because it is an owner writing a policy.
	//
	// ONE guard, not two. The obvious version also assigns
	// `req.WorkspaceID = identity.WorkspaceID()` before this call; mutation
	// testing shows the pair is mutually redundant — remove either and the
	// other still holds — which makes both untestable and neither obviously
	// load-bearing. The argument here is the guard, because it is the value
	// Store.Set keys by, and Set echoes back what it actually wrote rather
	// than what it was handed.
	stored, err := s.workspacePolicies.Set(c.UserContext(), identity.WorkspaceID(), identity.Subject(), req)
	if err != nil {
		if errors.Is(err, workspacepolicy.ErrInvalidPolicy) {
			return s.errMsg(c, fiber.StatusBadRequest, err.Error())
		}
		return s.errMsg(c, fiber.StatusServiceUnavailable, "the workspace policy could not be saved")
	}
	s.reloadQuotaPolicy(c.UserContext())
	return s.workspacePolicyResponse(c, stored)
}

// configuredQuotaPolicy rebuilds the operator's YAML policy.
//
// Rebuilt from config rather than read back from the governor, because the
// governor holds the COMPOSED policy — configured plus stored — and composing
// that with the stored entries again would fold a workspace's own limit into
// itself. Idempotent today, since tightening a value against itself is a
// no-op, but it would stop being idempotent the moment any level's rule became
// anything other than min().
func (s *Server) configuredQuotaPolicy() *quota.Policy {
	if s == nil || s.config() == nil {
		return nil
	}
	q := s.config().Costs.Quotas
	toLevel := func(configured map[string]config.QuotaLimit) costs.LevelLimits {
		out := costs.LevelLimits{}
		for id, limit := range configured {
			out[id] = costs.DollarsToLimit(limit.DailyUSD, limit.MonthlyUSD, limit.DailyTokens, limit.Concurrency)
		}
		return out
	}
	return costs.BuildPolicy(
		costs.DollarsToLimit(q.Deployment.DailyUSD, q.Deployment.MonthlyUSD, q.Deployment.DailyTokens, q.Deployment.Concurrency),
		toLevel(q.Organizations), toLevel(q.Workspaces), toLevel(q.Principals),
		toLevel(q.Agents), toLevel(q.Models))
}

func quotaSubjectForWorkspace(workspaceID string) quota.Subject {
	return quota.Subject{WorkspaceID: wsroot.Normalize(workspaceID)}
}

// reloadQuotaPolicy recomposes the governor's policy from config plus every
// stored workspace entry.
//
// Recomposed WHOLE rather than patched for the one workspace that changed. A
// patch would have to know how to remove an entry a workspace cleared, and the
// version of that which forgets is a workspace that keeps a limit it deleted —
// visible to nobody, because the API reports what is stored.
func (s *Server) reloadQuotaPolicy(ctx context.Context) {
	if s == nil || s.costGovernor == nil {
		return
	}
	var stored []workspacepolicy.Policy
	if s.workspacePolicies != nil {
		loaded, err := s.workspacePolicies.All(ctx)
		if err != nil {
			// Leaving the previous policy in place beats installing one built
			// from a failed read: an empty result would silently drop every
			// workspace's self-imposed ceiling at once.
			s.logger().Error("per-workspace limits could not be reloaded; keeping the previous policy", zapErr(err))
			return
		}
		stored = loaded
	}
	s.costGovernor.SetQuotaPolicy(workspacepolicy.Compose(s.configuredQuotaPolicy(), stored))
	perUser := make(map[string]int64, len(stored))
	for _, policy := range stored {
		if policy.PerUserDailyTokens > 0 {
			perUser[policy.WorkspaceID] = policy.PerUserDailyTokens
		}
	}
	s.costGovernor.SetWorkspaceUserTokenLimits(perUser)
}

// SetCostGovernor wires the reservation path a composed quota policy is
// installed into, so a per-workspace limit takes effect without a restart.
func (s *Server) SetCostGovernor(governor *costs.Governor) {
	s.costGovernor = governor
}

// ReloadWorkspaceQuotaPolicy recomposes the effective quota policy from config
// plus every stored per-workspace entry.
//
// Exported so application wiring can call it at STARTUP as well as after an
// edit. Without the startup call a restart loses every workspace's
// self-imposed ceiling until somebody happens to save one — and a limit that
// quietly stops applying is worse than one that was never set, because nobody
// is watching for its absence.
func (s *Server) ReloadWorkspaceQuotaPolicy(ctx context.Context) {
	s.reloadQuotaPolicy(ctx)
}
