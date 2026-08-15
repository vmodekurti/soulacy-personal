package gateway

import (
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/internal/storage"
	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/message"
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
