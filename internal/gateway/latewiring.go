package gateway

import "github.com/gofiber/fiber/v2"

// requireAuthEngine resolves the auth engine when the request arrives. The
// route table is built before App.wireAuth calls SetAuth, so auth routes must
// not be conditionally registered from the engine's build-time value.
func (s *Server) requireAuthEngine(pick func(*Server) fiber.Handler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if s.authEngine == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable,
				"authentication is not configured on this deployment; set auth.mode and restart")
		}
		return pick(s)(c)
	}
}

// authStack resolves and memoises the request authentication middleware
// lazily. Capturing authHandler while buildApp runs permanently captures the
// legacy static-key fallback because SetAuth has not run yet.
func (s *Server) authStack() fiber.Handler {
	if h := s.authStackCache.Load(); h != nil {
		return *h
	}
	h := s.authHandler()
	if !s.authStackCache.CompareAndSwap(nil, &h) {
		if won := s.authStackCache.Load(); won != nil {
			return *won
		}
	}
	return h
}
