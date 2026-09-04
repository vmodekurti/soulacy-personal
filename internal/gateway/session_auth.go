package gateway

import (
	"context"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/message"
)

type sessionOwner struct {
	Principal string
	AgentID   string
}

func requestPrincipal(c *fiber.Ctx) (runtime.Principal, bool) {
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
	if principal, ok := requestPrincipal(c); ok {
		return runtime.WithPrincipal(ctx, principal)
	}
	return ctx
}

func authenticatedPrincipal(c *fiber.Ctx) (string, bool, bool) {
	cl := auth.ClaimsFromCtx(c)
	if cl == nil {
		return "", false, false
	}
	if strings.EqualFold(cl.Role, "admin") {
		return "admin", true, true
	}
	principal := strings.TrimSpace(cl.Subject)
	if principal == "" {
		principal = strings.TrimSpace(cl.Email)
	}
	if principal == "" {
		return "", true, false
	}
	return cl.Role + ":" + principal, true, false
}

func websocketPrincipalFromCtx(c *fiber.Ctx) eventPrincipal {
	principal, authenticated, admin := authenticatedPrincipal(c)
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
func (s *Server) authorizeEvent(principal eventPrincipal, event message.Event) bool {
	if principal.Admin {
		return true
	}
	if !principal.Authenticated || principal.Principal == "" {
		return false
	}
	if event.SessionID != "" {
		s.sessionOwnerMu.RLock()
		owner, ok := s.sessionOwners[event.SessionID]
		s.sessionOwnerMu.RUnlock()
		if ok {
			return owner.Principal == principal.Principal && (event.AgentID == "" || owner.AgentID == event.AgentID)
		}
		// Unknown session IDs are never broadcast to a non-admin subscriber.
		return false
	}
	if event.AgentID == "" {
		return false
	}
	claims := &auth.Claims{Role: principal.Role, Scopes: principal.Scopes}
	if !claims.AllowsResource(rbac.ResourceChat) {
		return false
	}
	if s.rbacManager != nil {
		allowed, err := s.rbacManager.CanAccessAgentResource(principal.Role, event.AgentID, rbac.ResourceChat, rbac.ActionRead)
		return err == nil && allowed
	}
	return rbac.HasPermission(principal.Role, rbac.ResourceChat, rbac.ActionRead)
}

// claimSession atomically binds a session to the authenticated principal and
// agent on first use. Reuse by another principal or for another agent is hidden
// as not-found to avoid turning session IDs into an enumeration oracle.
func (s *Server) claimSession(c *fiber.Ctx, agentID, sessionID string) error {
	principal, authenticated, admin := authenticatedPrincipal(c)
	if !authenticated || admin {
		return nil
	}
	if principal == "" || strings.TrimSpace(agentID) == "" || strings.TrimSpace(sessionID) == "" {
		return fiber.NewError(fiber.StatusForbidden, "authenticated session identity is incomplete")
	}
	s.sessionOwnerMu.Lock()
	defer s.sessionOwnerMu.Unlock()
	owner, exists := s.sessionOwners[sessionID]
	if !exists {
		s.sessionOwners[strings.Clone(sessionID)] = sessionOwner{Principal: strings.Clone(principal), AgentID: strings.Clone(agentID)}
		return nil
	}
	if owner.Principal != principal || owner.AgentID != agentID {
		return fiber.NewError(fiber.StatusNotFound, "session not found")
	}
	return nil
}

func (s *Server) requireSession(c *fiber.Ctx, agentID, sessionID string) error {
	principal, authenticated, admin := authenticatedPrincipal(c)
	if !authenticated || admin {
		return nil
	}
	s.sessionOwnerMu.RLock()
	owner, exists := s.sessionOwners[sessionID]
	s.sessionOwnerMu.RUnlock()
	if !exists || owner.Principal != principal || (agentID != "" && owner.AgentID != agentID) {
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
