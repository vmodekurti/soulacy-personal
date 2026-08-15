package gateway

import (
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/actionlog"
	"github.com/soulacy/soulacy/internal/agentmemory"
	"github.com/soulacy/soulacy/internal/auth/apikeys"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/storage"
	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/message"
	"strings"
	"time"
)

// actionScope binds the action log to one workspace.
//
// The action log was keyed by agent ID alone — both its per-agent JSONL files
// and every SQL predicate. Agent IDs are only unique within a workspace, so two
// tenants each running an agent called "assistant" appended to one file and
// each tail returned the other's runs. This type is the single place a handler
// reaches history through, so a read cannot accidentally be written without the
// tenant.
type actionScope struct {
	actions     storage.ActionLogBackend
	scoped      storage.WorkspaceActionLogBackend
	workspaceID string
}

// ErrActionLogNotTenantAware is returned when a non-personal workspace asks a
// backend that has no tenant-aware surface for history.
//
// The alternative — falling back to the unscoped call — would answer with the
// personal workspace's events, which is both wrong for the caller and a
// cross-tenant read. Failing is loud; the fallback would be silent.
var ErrActionLogNotTenantAware = errors.New("action log backend is not tenant-aware")

// actionLog returns the request's scoped view of the action log. With no
// verified identity this is the implicit personal workspace, which is the
// history those deployments have always seen.
func (s *Server) actionLog(c *fiber.Ctx) actionScope {
	workspaceID := wsroot.PersonalWorkspaceID
	if c != nil {
		if identity, ok := requestIdentity(c); ok {
			workspaceID = wsroot.Normalize(identity.WorkspaceID())
		}
	}
	return s.actionLogForWorkspace(workspaceID)
}

// actionLogForWorkspace scopes the action log without a request, for
// background work that already knows which tenant it is acting for.
func (s *Server) actionLogForWorkspace(workspaceID string) actionScope {
	scope := actionScope{workspaceID: wsroot.Normalize(workspaceID)}
	if s == nil || s.actions == nil {
		return scope
	}
	scope.actions = s.actions
	if scoped, ok := s.actions.(storage.WorkspaceActionLogBackend); ok {
		scope.scoped = scoped
	}
	return scope
}

// Available reports whether there is an action log at all. Action logging can
// be disabled, in which case handlers degrade rather than error.
func (a actionScope) Available() bool { return a.actions != nil }

// personal reports whether the unscoped calls are safe to use: they are, and
// only are, when this scope *is* the personal workspace.
func (a actionScope) personal() bool {
	return a.workspaceID == wsroot.PersonalWorkspaceID
}

// Tail returns the most recent events for one agent in this workspace.
func (a actionScope) Tail(agentID string, limit int) ([]message.Event, error) {
	if a.actions == nil {
		return nil, nil
	}
	if a.scoped != nil {
		return a.scoped.TailInWorkspace(a.workspaceID, agentID, limit)
	}
	if !a.personal() {
		return nil, ErrActionLogNotTenantAware
	}
	return a.actions.Tail(agentID, limit)
}

// tailFilterer and the querier interfaces are the optional read surfaces some backends
// offer. They are matched structurally because the frozen interface cannot
// name them.
// They are split into legacy and scoped pairs rather than one interface with
// both methods so that a single-tenant backend offering only the legacy shape
// keeps its filtering instead of silently degrading to an unfiltered tail.
type tailFilterer interface {
	TailFiltered(string, int, map[string]bool) ([]message.Event, error)
}

type workspaceTailFilterer interface {
	TailFilteredInWorkspace(string, string, int, map[string]bool) ([]message.Event, error)
}

type legacyEventQuerier interface {
	QueryEvents(string, string, int, map[string]bool) ([]message.Event, error)
	QueryFiltered(string, int, map[string]bool) ([]message.Event, error)
}

type workspaceEventQuerier interface {
	QueryEventsInWorkspace(string, string, string, int, map[string]bool) ([]message.Event, error)
	QueryFilteredInWorkspace(string, string, int, map[string]bool) ([]message.Event, error)
}

// TailFiltered is Tail, but only events whose type is in `allowed` count toward
// the limit, so a chatty run cannot crowd run-boundary events out of the
// window. Backends without the filtered surface fall back to a plain tail; the
// caller post-filters, as it always has.
func (a actionScope) TailFiltered(agentID string, limit int, allowed map[string]bool) ([]message.Event, error) {
	if a.actions == nil {
		return nil, nil
	}
	if len(allowed) == 0 {
		return a.Tail(agentID, limit)
	}
	if scoped, ok := a.actions.(workspaceTailFilterer); ok {
		return scoped.TailFilteredInWorkspace(a.workspaceID, agentID, limit, allowed)
	}
	if !a.personal() {
		return nil, ErrActionLogNotTenantAware
	}
	if legacy, ok := a.actions.(tailFilterer); ok {
		return legacy.TailFiltered(agentID, limit, allowed)
	}
	return a.Tail(agentID, limit)
}

// QueryEvents reads the durable history rather than the rolling file. The
// second return reports whether the backend supports durable queries at all,
// so a caller can say so instead of presenting an empty result as "no events".
func (a actionScope) QueryEvents(agentID, sessionID string, limit int, allowed map[string]bool) ([]message.Event, bool, error) {
	if a.actions == nil {
		return nil, false, nil
	}
	if scoped, ok := a.actions.(workspaceEventQuerier); ok {
		events, err := scoped.QueryEventsInWorkspace(a.workspaceID, agentID, sessionID, limit, allowed)
		return events, true, err
	}
	legacy, ok := a.actions.(legacyEventQuerier)
	if !ok {
		return nil, false, nil
	}
	if !a.personal() {
		return nil, true, ErrActionLogNotTenantAware
	}
	events, err := legacy.QueryEvents(agentID, sessionID, limit, allowed)
	return events, true, err
}

// QueryFiltered is QueryEvents for one agent across every session.
func (a actionScope) QueryFiltered(agentID string, limit int, allowed map[string]bool) ([]message.Event, bool, error) {
	if a.actions == nil {
		return nil, false, nil
	}
	if scoped, ok := a.actions.(workspaceEventQuerier); ok {
		events, err := scoped.QueryFilteredInWorkspace(a.workspaceID, agentID, limit, allowed)
		return events, true, err
	}
	legacy, ok := a.actions.(legacyEventQuerier)
	if !ok {
		return nil, false, nil
	}
	if !a.personal() {
		return nil, true, ErrActionLogNotTenantAware
	}
	events, err := legacy.QueryFiltered(agentID, limit, allowed)
	return events, true, err
}

// learningStore returns the request workspace's reviewable proposal store, or
// nil when learning is disabled or the store could not be created.
//
// Proposals are candidate rules that change agent behaviour once accepted, so
// one tenant's review queue must not be readable — let alone acceptable — from
// another. Isolation is by file: a proposal ID belonging to another tenant is
// not in the file this caller reads, so an update returns "not found", which
// is the same answer a genuinely missing ID gives. IDs cannot be probed.
func (s *Server) learningStore(c *fiber.Ctx) *learning.Store {
	if s == nil || s.engine == nil {
		return nil
	}
	workspaceID := wsroot.PersonalWorkspaceID
	if c != nil {
		if identity, ok := requestIdentity(c); ok {
			workspaceID = wsroot.Normalize(identity.WorkspaceID())
		}
	}
	return s.engine.LearningStoreInWorkspace(workspaceID)
}

// historyScope is the tenant and person a conversation-history request acts
// for. Conversation history is user-private and holds message content itself,
// so cross-session reads need both halves of its scope key.
//
// With no verified identity this is the personal workspace and the empty
// subject — which is precisely what a single-user installation's rows carry,
// so those deployments read back exactly what they always did.
func (s *Server) historyScope(c *fiber.Ctx) (workspaceID, subject string) {
	if c == nil {
		return wsroot.PersonalWorkspaceID, ""
	}
	if identity, ok := requestIdentity(c); ok {
		return wsroot.Normalize(identity.WorkspaceID()), identity.Subject()
	}
	return wsroot.PersonalWorkspaceID, ""
}

// costWorkspace is the tenant an accounting request acts in.
//
// Every cost read is workspace-scoped now, including the ones that look
// deployment-wide: spend is both confidential (it reveals another team's
// activity and model choices) and rivalrous (a shared ceiling means the
// busiest tenant starves the rest).
func (s *Server) costWorkspace(c *fiber.Ctx) string {
	if c != nil {
		if identity, ok := requestIdentity(c); ok {
			return wsroot.Normalize(identity.WorkspaceID())
		}
	}
	return wsroot.PersonalWorkspaceID
}

// dlqScope is the tenant a dead-letter request acts in.
//
// The dead-letter queue reads like operations data — it is served from
// /admin/dlq behind a config-read grant — but each row carries the original
// job payload, which for an agent run is the user's prompt. It is the failed
// half of the same conversation the history store scopes, so it is scoped the
// same way, and an admin grant in one workspace does not become a window into
// another's failed prompts.
//
// Unlike history this needs no subject: a parked job belongs to the workspace
// that has to decide whether to retry it, and the person who triggered it may
// well have left the team by the time anyone looks.
func (s *Server) dlqScope(c *fiber.Ctx) string {
	if c != nil {
		if identity, ok := requestIdentity(c); ok {
			return wsroot.Normalize(identity.WorkspaceID())
		}
	}
	return wsroot.PersonalWorkspaceID
}

// brainMemory is the three-layer long-term memory of the tenant a request acts
// for.
//
// Its contents are as private as anything in the product: episodic records are
// verbatim task inputs and replies, procedural rules are the operating
// instructions a team wrote for its own agents, and the rulebook lock is a
// control that refuses writes rather than a fact that can be read. Scoping is
// by store instance, not by an argument, because every method on
// CompositeStore is keyed by agent ID alone — there is no place to put a
// predicate even if a caller remembered to.
func (s *Server) brainMemory(c *fiber.Ctx) *agentmemory.CompositeStore {
	if s == nil || s.engine == nil {
		return nil
	}
	if c != nil {
		if identity, ok := requestIdentity(c); ok {
			return s.engine.BrainStoreInWorkspace(wsroot.Normalize(identity.WorkspaceID()))
		}
	}
	return s.engine.BrainStoreInWorkspace(wsroot.PersonalWorkspaceID)
}

// skillCatalog is the skill inventory of the tenant a request acts for
// (MU-017 criterion 1).
//
// Skills are executable instructions an agent follows, not metadata about one,
// so a shared inventory is not a disclosure problem — installing a skill in
// one workspace would add it to every agent in the deployment, and two
// tenants' same-named skills would be resolved by scan order rather than by
// ownership.
//
// Platform directories stay visible to every workspace as read-only templates.
// A workspace's own directory is scanned last, so it may shadow a platform
// skill by name without being able to modify the platform copy.
func (s *Server) skillCatalog(c *fiber.Ctx) runtime.SkillLoader {
	if s == nil {
		return nil
	}
	return s.skillCatalogForWorkspace(s.requestWorkspace(c))
}

// skillCatalogForWorkspace is the same view without a request, for code paths
// that already know the tenant — a package import running under an agentScope,
// or a background install acting for one workspace.
func (s *Server) skillCatalogForWorkspace(workspaceID string) runtime.SkillLoader {
	if s == nil {
		return nil
	}
	if s.skillStores != nil {
		if loader := s.skillStores.For(workspaceID); loader != nil {
			return loader
		}
		return nil
	}
	return s.skillLoader
}

// requestWorkspace is the tenant a request acts in, defaulting to personal.
func (s *Server) requestWorkspace(c *fiber.Ctx) string {
	if c != nil {
		if identity, ok := requestIdentity(c); ok {
			return wsroot.Normalize(identity.WorkspaceID())
		}
	}
	return wsroot.PersonalWorkspaceID
}

// platformMetricsMW restricts the raw Prometheus endpoint in multi-user
// deployments.
//
// The metric families carry an `agent` label, and an agent ID is tenant data:
// it names what another team is building, and its rate reveals how much they
// are using it. The registry is process-wide and rendered in one pass, so
// there is no per-caller view of it — which leaves who may read it as the
// control. `metrics:read` is held by owner, admin, and developer, and
// developer is a *workspace* role; the raw endpoint is a deployment-operations
// surface.
//
// Personal is deliberately untouched: with one workspace the labels identify
// nobody, and product invariant 7 says a single-user install must not notice
// the storage layer became tenant-aware. Per-tenant numbers are served by the
// cost and run endpoints, which are workspace-scoped.
func (s *Server) platformMetricsMW() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if s.cfg == nil || !config.IsMultiUserMode(s.cfg.DeploymentMode()) {
			return c.Next()
		}
		role := ""
		if identity, ok := requestIdentity(c); ok {
			role = strings.ToLower(strings.TrimSpace(identity.Role()))
		}
		if role != rbac.RoleOwner && role != rbac.RoleAdmin {
			return s.errMsg(c, fiber.StatusForbidden,
				"raw platform metrics are restricted to workspace owners and admins; use the cost and run endpoints for this workspace's figures")
		}
		return c.Next()
	}
}

// OpsSummary returns this workspace's run-reliability rollup. Backends without
// a tenant-aware surface are single-tenant, so the unscoped call is correct
// there and only there.
func (a actionScope) OpsSummary(since time.Time, window string, limit int) (actionlog.OpsSummary, error) {
	if a.actions == nil {
		return actionlog.OpsSummary{}, nil
	}
	if scoped, ok := a.actions.(interface {
		OpsSummaryInWorkspace(string, time.Time, string, int) (actionlog.OpsSummary, error)
	}); ok {
		return scoped.OpsSummaryInWorkspace(a.workspaceID, since, window, limit)
	}
	legacy, ok := a.actions.(opsSummarizer)
	if !ok {
		return actionlog.OpsSummary{}, ErrOpsSummaryUnsupported
	}
	if !a.personal() {
		return actionlog.OpsSummary{}, ErrActionLogNotTenantAware
	}
	return legacy.OpsSummary(since, window, limit)
}

// ErrOpsSummaryUnsupported distinguishes "this backend cannot roll up runs"
// from "the rollup failed", so a handler can say which.
var ErrOpsSummaryUnsupported = errors.New("action log backend does not support ops summaries")

// credentialAPI builds the credential handler for this deployment.
//
// In multi-user mode an unverified request must carry no authority: the
// handler's own fallback for "no resolvable workspace identity" is otherwise
// to show and manage every credential in the installation, which is the one
// place a fail-open default is unarguable. Personal keeps the fallback,
// because there is exactly one tenant and the deployment predates workspace
// identity entirely.
func (s *Server) credentialAPI() *apikeys.API {
	requireIdentity := s.cfg != nil && config.IsMultiUserMode(s.cfg.DeploymentMode())
	return apikeys.NewScopedAPI(s.apiKeyStore, s.log, requireIdentity)
}
