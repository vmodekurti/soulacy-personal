package gateway

import (
	"context"
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/session"
	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/message"
)

type sessionOwner struct {
	Principal   string
	WorkspaceID string
	AgentID     string
	Visibility  string
}

// readableBy mirrors session.Ownership.Readable so the cache and the durable
// store cannot answer the same question differently.
//
// Both workspaces are normalized first. session.Ownership.Readable refuses an
// empty workspace outright, which is right for a durable store that should
// never authorize without tenant context — but at this boundary an absent
// workspace means Personal's implicit one, the same reading agentScope and
// studioScope use. Without normalizing, an open Personal deployment would be
// denied access to its own conversations.
func (o sessionOwner) readableBy(workspaceID, principal string, admin bool) bool {
	return session.Ownership{
		WorkspaceID: wsroot.Normalize(o.WorkspaceID), Creator: o.Principal, Visibility: o.Visibility,
	}.Readable(wsroot.Normalize(workspaceID), principal, admin)
}

func requestPrincipal(c *fiber.Ctx) (runtime.Principal, bool) {
	if identity, ok := requestIdentity(c); ok {
		return runtime.Principal{
			Subject: identity.Subject(), OrganizationID: identity.OrganizationID(),
			WorkspaceID: identity.WorkspaceID(), MembershipID: identity.MembershipID(),
			Role: identity.Role(), Scopes: identity.Scopes(), CredentialID: identity.CredentialID(),
			RequestID: identity.RequestID(), Kind: identity.PrincipalKind(),
		}, true
	}
	cl := auth.ClaimsFromCtx(c)
	if cl == nil {
		return runtime.Principal{}, false
	}
	subject := strings.TrimSpace(cl.Subject)
	if subject == "" {
		subject = strings.TrimSpace(cl.Email)
	}
	return runtime.Principal{Subject: subject, Role: cl.Role, Scopes: append([]string(nil), cl.Scopes...)}, true
}

func withRequestPrincipal(c *fiber.Ctx, ctx context.Context) context.Context {
	ctx = withWorkspaceIdentity(c, ctx)
	if principal, ok := requestPrincipal(c); ok {
		return runtime.WithPrincipal(ctx, principal)
	}
	return ctx
}

// authorizedRequestContext returns Fiber's standard request context decorated
// with the verified workspace identity and runtime principal.
func authorizedRequestContext(c *fiber.Ctx) context.Context {
	return withRequestPrincipal(c, c.UserContext())
}

func authenticatedPrincipal(c *fiber.Ctx) (string, bool, bool) {
	if identity, ok := requestIdentity(c); ok {
		role := strings.TrimSpace(identity.Role())
		subject := strings.TrimSpace(identity.Subject())
		principal := role + ":" + subject
		if subject == "" {
			principal = ""
		}
		return principal, true, role == rbac.RoleOwner || role == rbac.RoleAdmin
	}
	cl := auth.ClaimsFromCtx(c)
	if cl == nil {
		return "", false, false
	}
	principal := strings.TrimSpace(cl.Subject)
	if principal == "" {
		principal = strings.TrimSpace(cl.Email)
	}
	if principal == "" {
		return "", true, false
	}
	role := strings.TrimSpace(cl.Role)
	return role + ":" + principal, true, role == rbac.RoleOwner || role == rbac.RoleAdmin
}

func websocketPrincipalFromCtx(c *fiber.Ctx) eventPrincipal {
	principal, authenticated, admin := authenticatedPrincipal(c)
	if identity, ok := requestIdentity(c); ok {
		return eventPrincipal{
			Principal: principal, WorkspaceID: identity.WorkspaceID(), Role: identity.Role(),
			Scopes: identity.Scopes(), Admin: admin, Authenticated: authenticated,
		}
	}
	cl := auth.ClaimsFromCtx(c)
	if cl == nil {
		return eventPrincipal{}
	}
	return eventPrincipal{
		Principal: principal, Role: strings.Clone(cl.Role), Scopes: append([]string(nil), cl.Scopes...),
		Admin: admin, Authenticated: authenticated,
	}
}

// authorizeEvent prevents a WebSocket subscriber from observing another
// principal's prompts, tool arguments, results, or run progress.
//
// MU-026 criterion 7 names "sessionless events and administrative events"
// specifically, and the sessionless path is where this was wrong. An event with
// no session was authorized by asking whether the SUBSCRIBER's workspace lets
// their role read an agent with that ID — never whether the EVENT belonged to
// that workspace. Agent IDs are unique per workspace, not per deployment, so
// two tenants with a "support-bot" both satisfied the check and each received
// the other's sessionless events: tool progress, run status, errors.
//
// The workspace comparison below is therefore first and unconditional. It is
// not a refinement of the checks that follow; it is the boundary they were all
// implicitly assuming.
func (s *Server) authorizeEvent(principal eventPrincipal, event message.Event) bool {
	if principal.Admin && !s.authorizationRequired() {
		return true
	}
	if !principal.Authenticated || principal.Principal == "" {
		return false
	}
	// A STAMPED event may never cross a workspace boundary, whatever the
	// checks below would say. Engine.emit stamps the run's workspace, so this
	// covers every event produced by a run and is strictly stronger than what
	// was here before.
	//
	// Only applied when the event actually carries a workspace. An unstamped
	// event is not evidence of belonging to the personal workspace — it is
	// evidence that whoever emitted it predates the stamping, and for those the
	// session owner below is the durable, verified fact. Treating unstamped as
	// personal here would deny a workspace's own users their own sessions.
	if stamped := strings.TrimSpace(event.WorkspaceID); stamped != "" &&
		wsroot.Normalize(stamped) != wsroot.Normalize(principal.WorkspaceID) {
		return false
	}
	if event.SessionID != "" {
		owner, ok := s.lookupOwner(context.Background(), event.SessionID)
		if ok {
			return owner.readableBy(principal.WorkspaceID, principal.Principal, principal.Admin) &&
				(event.AgentID == "" || owner.AgentID == event.AgentID)
		}
		// Unknown session IDs are never broadcast to a non-admin subscriber.
		return false
	}
	if event.AgentID == "" {
		return false
	}
	// The sessionless path is where the gap was. With no session there is no
	// owner record to consult, so tenancy rested entirely on the RBAC call
	// below — which asks whether the SUBSCRIBER's workspace lets their role
	// read an agent with that ID, never whether the EVENT belonged to that
	// workspace. Agent IDs are unique per workspace, so two tenants with a
	// "support-bot" both satisfied it and each received the other's tool
	// progress, run status and errors.
	//
	// An unstamped sessionless event therefore has nothing establishing its
	// tenant at all, and is refused for any named workspace. Personal keeps
	// receiving it, which is what a single-tenant deployment has always seen.
	if strings.TrimSpace(event.WorkspaceID) == "" &&
		wsroot.Normalize(principal.WorkspaceID) != wsroot.PersonalWorkspaceID {
		return false
	}
	claims := &auth.Claims{Role: principal.Role, Scopes: principal.Scopes}
	if !claims.AllowsResource(rbac.ResourceChat) {
		return false
	}
	if s.rbacManager != nil {
		allowed, err := s.rbacManager.CanAccessAgentResourceInWorkspace(principal.WorkspaceID, principal.Role, event.AgentID, rbac.ResourceChat, rbac.ActionRead)
		return err == nil && allowed
	}
	return rbac.HasPermission(principal.Role, rbac.ResourceChat, rbac.ActionRead)
}

// ownershipStore returns the durable store, or nil when only the in-process
// map is available.
func (s *Server) ownershipStore() session.OwnershipStore {
	s.sessionOwnerMu.RLock()
	defer s.sessionOwnerMu.RUnlock()
	return s.sessionOwnership
}

func (s *Server) cacheOwner(sessionID string, owner sessionOwner) {
	s.sessionOwnerMu.Lock()
	defer s.sessionOwnerMu.Unlock()
	s.sessionOwners[strings.Clone(sessionID)] = owner
}

// lookupOwner resolves a session's owner, consulting the durable store on a
// cache miss. A miss is not an answer: a restart empties the cache and a second
// replica never filled it, so treating a miss as "no owner" would hand the
// session to whoever asked next.
func (s *Server) lookupOwner(ctx context.Context, sessionID string) (sessionOwner, bool) {
	s.sessionOwnerMu.RLock()
	cached, ok := s.sessionOwners[sessionID]
	s.sessionOwnerMu.RUnlock()
	if ok {
		return cached, true
	}
	store := s.ownershipStore()
	if store == nil {
		return sessionOwner{}, false
	}
	record, err := store.Lookup(ctx, sessionID)
	if err != nil {
		return sessionOwner{}, false
	}
	owner := sessionOwner{Principal: record.Creator, WorkspaceID: record.WorkspaceID, AgentID: record.AgentID, Visibility: record.Visibility}
	s.cacheOwner(sessionID, owner)
	return owner, true
}

// claimSession atomically binds a session to the authenticated principal and
// agent on first use. Reuse by another principal or for another agent is hidden
// as not-found to avoid turning session IDs into an enumeration oracle.
func (s *Server) claimSession(c *fiber.Ctx, agentID, sessionID string) error {
	principal, authenticated, admin := authenticatedPrincipal(c)
	if !authenticated || (admin && !s.authorizationRequired()) {
		return nil
	}
	workspaceID := ""
	if identity, ok := requestIdentity(c); ok {
		workspaceID = identity.WorkspaceID()
	}
	if principal == "" || strings.TrimSpace(agentID) == "" || strings.TrimSpace(sessionID) == "" {
		return fiber.NewError(fiber.StatusForbidden, "authenticated session identity is incomplete")
	}

	// The durable store decides. Its Claim is one transaction, so two
	// concurrent first-uses cannot both win — which a check-then-insert
	// against the cache would allow, landing the loser's messages in the
	// winner's conversation.
	if store := s.ownershipStore(); store != nil {
		record, err := store.Claim(detachedRequestContext(c), session.Ownership{
			SessionID: sessionID, WorkspaceID: workspaceID, AgentID: agentID, Creator: principal,
		})
		if err != nil {
			if errors.Is(err, session.ErrSessionClaimed) {
				return fiber.NewError(fiber.StatusNotFound, "session not found")
			}
			return fiber.NewError(fiber.StatusServiceUnavailable, "session ownership could not be recorded")
		}
		s.cacheOwner(sessionID, sessionOwner{Principal: record.Creator, WorkspaceID: record.WorkspaceID, AgentID: record.AgentID, Visibility: record.Visibility})
		return nil
	}

	s.sessionOwnerMu.Lock()
	defer s.sessionOwnerMu.Unlock()
	owner, exists := s.sessionOwners[sessionID]
	if !exists {
		s.sessionOwners[strings.Clone(sessionID)] = sessionOwner{Principal: strings.Clone(principal), WorkspaceID: strings.Clone(workspaceID), AgentID: strings.Clone(agentID), Visibility: session.VisibilityPrivate}
		return nil
	}
	if owner.Principal != principal || owner.WorkspaceID != workspaceID || owner.AgentID != agentID {
		return fiber.NewError(fiber.StatusNotFound, "session not found")
	}
	return nil
}

func (s *Server) requireSession(c *fiber.Ctx, agentID, sessionID string) error {
	principal, authenticated, admin := authenticatedPrincipal(c)
	if !authenticated || (admin && !s.authorizationRequired()) {
		return nil
	}
	workspaceID := ""
	if identity, ok := requestIdentity(c); ok {
		workspaceID = identity.WorkspaceID()
	}
	owner, exists := s.lookupOwner(detachedRequestContext(c), sessionID)
	if !exists || !owner.readableBy(workspaceID, principal, admin) || (agentID != "" && owner.AgentID != agentID) {
		return fiber.NewError(fiber.StatusNotFound, "session not found")
	}
	return nil
}

func (s *Server) requirePathSessionMW(sessionParam, agentParam string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		agentID := ""
		if agentParam != "" {
			agentID = c.Params(agentParam)
		}
		if err := s.requireSession(c, agentID, c.Params(sessionParam)); err != nil {
			return err
		}
		return c.Next()
	}
}
