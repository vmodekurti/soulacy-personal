package gateway

import "github.com/gofiber/fiber/v2"

// latewiring.go — a route table must not depend on wiring ORDER.
//
// THE BUG. buildApp runs once, inside New. SetAuth and SetRBAC are called by
// the wiring AFTERWARDS and neither rebuilds the router. So every route
// registered inside `if s.authEngine != nil { … }` was registered against a
// nil engine — which is to say, never registered at all.
//
// Nothing 404s, which is why it went unnoticed for so long: an unregistered
// /api/v1 path falls through to the authenticated group, so the token
// exchange, the refresh, the logout and all seven OIDC routes answered
// 401 "invalid or missing API key". The one thing you need in order to GET a
// credential required a credential.
//
// The visible symptoms, all one cause: a team-mode login that fails with a key
// the operator is certain is right; an SSO button that never appears because
// the GUI asks /auth/oidc/config whether OIDC is enabled and gets 401; a GUI
// that retries /auth/refresh on every 401 and is refused every time.
//
// The codebase already knew: newTestGatewayWithRBAC rebuilds the app by hand,
// with a comment explaining that routes registered under a nil manager are not
// there. The workaround lived in the tests and never made it to production.
//
// THE FIX IS NOT TO REBUILD. Rebuilding on each setter makes the route table a
// function of call order, which is the same class of bug with a longer fuse:
// whichever setter runs last decides what exists. Instead the routes are
// registered UNCONDITIONALLY and refuse at request time when their dependency
// is absent — which is what every other handler in this file already does
// (`if s.dlqStore == nil { … }`, `if s.credVault == nil { … }`). The route
// table becomes a static fact about the build, and availability becomes a
// runtime answer.

// requireAuthEngine wraps a handler that needs the auth engine, resolving it at
// REQUEST time so the route exists regardless of when SetAuth ran.
//
// The message names the mode rather than the missing object: an operator
// reading "authentication is not configured" can act on it, where "auth engine
// is nil" is an invitation to read our source.
func (s *Server) requireAuthEngine(pick func(*Server) fiber.Handler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if s.authEngine == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable,
				"authentication is not configured on this deployment; set auth.mode and restart")
		}
		return pick(s)(c)
	}
}

// requireRBACManager is the same for the RBAC management routes.
func (s *Server) requireRBACManager(pick func(*Server) fiber.Handler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if s.rbacManager == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable,
				"role management is not available on this deployment")
		}
		return pick(s)(c)
	}
}

// authStack resolves the request-authentication middleware LAZILY, on the
// first request, instead of when buildApp assembles the router.
//
// THE SECOND HALF OF THE SAME BUG. Registering the auth routes
// unconditionally (above) fixed obtaining a credential. It did not fix using
// one. `api := app.Group("/api/v1", s.authWithPluginTokens(), …)` runs inside
// buildApp, and authWithPluginTokens captured `inner := s.authHandler()` —
// which reads s.authEngine, still nil at that instant. So every authenticated
// route in the deployment was guarded by legacyAuthMiddleware forever, and the
// engine's JWT, OIDC and managed sk_ key verification never ran on any
// request. A team-mode user could complete the token exchange and then be
// refused by every call they made with the token they had just been issued.
//
// Worse where server.api_key is unset, which is the normal shape of an
// SSO-only team deployment: legacyAuthMiddleware then returns a pass-through
// that calls c.Next() with NO claims at all. The request is not rejected — it
// arrives at workspaceContextMW unauthenticated, and what the operator sees is
// an unexplained failure rather than a login failure.
//
// Memoised rather than resolved per request because the nil-engine branch of
// legacyAuthMiddleware logs a warning as it resolves; per-request resolution
// would turn that one honest warning into a line per request. Memoised in an
// atomic.Pointer rather than a sync.Once because SetAuth must be able to
// invalidate it — see SetAuth, which stores nil.
func (s *Server) authStack() fiber.Handler {
	if h := s.authStackCache.Load(); h != nil {
		return *h
	}
	h := s.authHandler()
	// CompareAndSwap, not Store: a concurrent first request may already have
	// resolved and published one, and two live copies of the legacy fallback
	// would mean two warnings. Losing the race is fine — reload and use the
	// winner's, which is resolved from the same state.
	if !s.authStackCache.CompareAndSwap(nil, &h) {
		if won := s.authStackCache.Load(); won != nil {
			return *won
		}
	}
	return h
}
